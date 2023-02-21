package sfu

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ducksouplab/ducksoup/gst"
	"github.com/ducksouplab/ducksoup/helpers"
	"github.com/ducksouplab/ducksoup/sequencing"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog"
)

const (
	defaultInterpolatorStep = 30
	maxInterpolatorDuration = 5000
	encoderPeriod           = 1250
	statsPeriod             = 3000
	diffThreshold           = 10
)

type mixerSlice struct {
	sync.Mutex
	fromPs       *peerServer
	i            *interaction
	kind         string
	streamConfig sfuStream
	// webrtc
	input    *webrtc.TrackRemote
	output   *webrtc.TrackLocalStaticRTP
	receiver *webrtc.RTPReceiver
	// processing
	pipeline          *gst.Pipeline
	interpolatorIndex map[string]*sequencing.LinearInterpolator
	// controller
	senderControllerIndex map[string]*senderController // per user id
	targetBitrate         uint64
	// stats
	lastStats     time.Time
	inputBits     uint64
	outputBits    uint64
	inputBitrate  uint64
	outputBitrate uint64
	// status
	endCh chan struct{} // stop processing when track is removed
}

// helpers

func minUint64(v []uint64) (min uint64) {
	if len(v) > 0 {
		min = v[0]
	}
	for i := 1; i < len(v); i++ {
		if v[i] < min {
			min = v[i]
		}
	}
	return
}

func newMixerSlice(ps *peerServer, remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) (ms *mixerSlice, err error) {
	// create a new mixerSlice with:
	// - the same codec format as the incoming/remote one
	// - a unique server-side trackId, but won't be reused in the browser, see https://developer.mozilla.org/en-US/docs/Web/API/MediaStreamTrack/id
	// - a streamId shared among peerServer tracks (audio/video)
	// newId := uuid.New().String()

	kind := remoteTrack.Kind().String()
	var streamConfig sfuStream
	if kind == "video" {
		streamConfig = config.Video
	} else if kind == "audio" {
		streamConfig = config.Audio
	} else {
		return nil, errors.New("invalid kind")
	}

	newId := remoteTrack.ID()
	localTrack, err := webrtc.NewTrackLocalStaticRTP(remoteTrack.Codec().RTPCodecCapability, newId, ps.streamId)

	if err != nil {
		return
	}

	ms = &mixerSlice{
		fromPs:       ps,
		i:            ps.i,
		kind:         kind,
		streamConfig: streamConfig,
		// webrtc
		input:    remoteTrack,
		output:   localTrack,
		receiver: receiver, // TODO read RTCP?
		// processing
		pipeline:          ps.pipeline,
		interpolatorIndex: make(map[string]*sequencing.LinearInterpolator),
		// controller
		senderControllerIndex: map[string]*senderController{},
		// stats
		lastStats: time.Now(),
		// status
		endCh: make(chan struct{}),
	}

	return
}

func (s *mixerSlice) logError() *zerolog.Event {
	return s.i.logger.Error().Str("context", "track").Str("user", s.fromPs.userId)
}

func (s *mixerSlice) logInfo() *zerolog.Event {
	return s.i.logger.Info().Str("context", "track").Str("user", s.fromPs.userId)
}

func (s *mixerSlice) logDebug() *zerolog.Event {
	return s.i.logger.Debug().Str("context", "track").Str("user", s.fromPs.userId)
}

// Same ID as output track
func (s *mixerSlice) ID() string {
	return s.output.ID()
}

func (s *mixerSlice) addSender(pc *peerConn, sender *webrtc.RTPSender) {
	params := sender.GetParameters()

	toUserId := pc.userId
	if len(params.Encodings) == 1 {
		sc := newSenderController(pc, s, sender)
		s.Lock()
		s.senderControllerIndex[toUserId] = sc
		s.Unlock()
		go sc.loop()
	} else {
		s.logError().Str("toUser", toUserId).Str("cause", "wrong number of encoding parameters").Msg("add_sender_failed")
	}
}

func (l *mixerSlice) scanInput(buf []byte, n int) {
	packet := &rtp.Packet{}
	packet.Unmarshal(buf)

	l.Lock()
	// estimation (x8 for bytes) not taking int account headers
	// it seems using MarshalSize (like for outputBits below) does not give the right numbers due to packet 0-padding
	l.inputBits += uint64(n) * 8
	l.Unlock()
}

func (s *mixerSlice) Write(buf []byte) (err error) {
	packet := &rtp.Packet{}
	packet.Unmarshal(buf)
	err = s.output.WriteRTP(packet)

	if err == nil {
		go func() {
			outputBits := (packet.MarshalSize() - packet.Header.MarshalSize()) * 8
			s.Lock()
			s.outputBits += uint64(outputBits)
			s.Unlock()
		}()
	}

	return
}

func (s *mixerSlice) stop() {
	s.pipeline.Stop()
	close(s.endCh)
	s.logInfo().Str("track", s.ID()).Str("kind", s.kind).Msg("out_track_stopped")
}

func (s *mixerSlice) loop() {
	pipeline, i, userId := s.fromPs.pipeline, s.fromPs.i, s.fromPs.userId

	// gives pipeline a track to write to
	pipeline.BindTrackAutoStart(s.kind, s)
	// wait for audio and video
	<-pipeline.ReadyCh

	s.initializeBitrates()
	i.addFiles(userId, pipeline.OutputFiles()) // for reference
	go s.runTickers()
	// go s.runReceiverListener()

	// loop start
	buf := make([]byte, config.Common.MTU)
pushToPipeline:
	for {
		select {
		case <-i.endCh:
			// trial is over, no need to trigger signaling on every closing track
			break pushToPipeline
		case <-s.fromPs.closedCh:
			// peer may quit early (for instance page refresh), other peers need to be updated
			break pushToPipeline
		default:
			n, _, err := s.input.Read(buf)
			if err != nil {
				break pushToPipeline
			}
			s.pipeline.PushRTP(s.kind, buf[:n])
			// for stats
			go s.scanInput(buf, n)
		}
	}
	// loop end
	s.stop()
}

func (s *mixerSlice) initializeBitrates() {
	// pipeline is started once (either by the audio or video slice) but both
	// media types need to be initialized*
	s.pipeline.SetEncodingRate("audio", config.Audio.DefaultBitrate)
	s.pipeline.SetEncodingRate("video", config.Video.DefaultBitrate)
	// log
	s.logInfo().Uint64("value", config.Audio.DefaultBitrate/1000).Str("unit", "kbit/s").Msg("audio_target_bitrate_initialized")
	s.logInfo().Uint64("value", config.Video.DefaultBitrate/1000).Str("unit", "kbit/s").Msg("video_target_bitrate_initialized")
}

func (s *mixerSlice) updateTargetBitrates(newPotentialRate uint64) {
	s.Lock()
	s.targetBitrate = newPotentialRate
	s.Unlock()
	s.pipeline.SetEncodingRate(s.kind, newPotentialRate)
	// format and log
	msg := fmt.Sprintf("%s_target_bitrate_updated", s.kind)
	s.logInfo().Uint64("value", newPotentialRate/1000).Str("unit", "kbit/s").Msg(msg)
}

func (s *mixerSlice) runTickers() {
	// update encoding bitrate on tick and according to minimum controller rate
	go func() {
		encoderTicker := time.NewTicker(encoderPeriod * time.Millisecond)
		defer encoderTicker.Stop()
		for {
			select {
			case <-s.endCh:
				return
			case <-encoderTicker.C:
				if len(s.senderControllerIndex) > 0 {
					rates := []uint64{}
					for _, sc := range s.senderControllerIndex {
						rates = append(rates, sc.optimalBitrate)
					}
					newPotentialRate := minUint64(rates)
					if s.pipeline != nil && newPotentialRate > 0 {
						// skip updating previous value and encoding rate too often
						diff := helpers.AbsPercentageDiff(s.targetBitrate, newPotentialRate)
						// diffIsBigEnough: works also for diff being Inf+ (when updating from 0, diff is Inf+)
						diffIsBigEnough := diff > diffThreshold
						diffToMax := diff > 0 && (newPotentialRate == s.streamConfig.MaxBitrate)
						if diffIsBigEnough || diffToMax {
							go s.updateTargetBitrates(newPotentialRate)
						}
					}
				}
			}
		}
	}()

	go func() {
		statsTicker := time.NewTicker(statsPeriod * time.Millisecond)
		defer statsTicker.Stop()
		for {
			select {
			case <-s.endCh:
				return
			case tickTime := <-statsTicker.C:
				s.Lock()
				elapsed := tickTime.Sub(s.lastStats).Seconds()
				// update bitrates
				s.inputBitrate = s.inputBits / uint64(elapsed)
				s.outputBitrate = s.outputBits / uint64(elapsed)
				// reset cumulative bits and lastStats
				s.inputBits = 0
				s.outputBits = 0
				s.lastStats = tickTime
				s.Unlock()
				// log
				displayInputBitrateKbs := uint64(s.inputBitrate / 1000)
				displayOutputBitrateKbs := uint64(s.outputBitrate / 1000)
				displayOutputTargetBitrateKbs := uint64(s.targetBitrate / 1000)

				inputMsg := fmt.Sprintf("%s_in_bitrate", s.output.Kind().String())
				outputMsg := fmt.Sprintf("%s_out_bitrate", s.output.Kind().String())

				s.logDebug().Uint64("value", displayInputBitrateKbs).Str("unit", "kbit/s").Msg(inputMsg)
				s.logDebug().Uint64("value", displayOutputBitrateKbs).Uint64("target", displayOutputTargetBitrateKbs).Str("unit", "kbit/s").Msg(outputMsg)
			}
		}
	}()
}

// func (s *mixerSlice) runReceiverListener() {
// 	buf := make([]byte, defaultMTU)

// 	for {
// 		select {
// 		case <-s.endCh:
// 			return
// 		default:
// 			i, _, err := s.receiver.Read(buf)
// 			if err != nil {
// 				if err != io.EOF && err != io.ErrClosedPipe {
// 					s.logError().Err(err).Msg("read_received_rtcp_failed")
// 				}
// 				return
// 			}
// 			// TODO: send to rtpjitterbuffer sink_rtcp
// 			//s.pipeline.PushRTCP(s.kind, buf[:i])

// 			packets, err := rtcp.Unmarshal(buf[:i])
// 			if err != nil {
// 				s.logError().Err(err).Msg("unmarshal_received_rtcp_failed")
// 				continue
// 			}

// 			for _, packet := range packets {
// 				switch rtcpPacket := packet.(type) {
// 				case *rtcp.SourceDescription:
// 				// case *rtcp.SenderReport:
// 				// 	log.Println(rtcpPacket)
// 				// case *rtcp.ReceiverEstimatedMaximumBitrate:
// 				// 	log.Println(rtcpPacket)
// 				default:
// 					//s.logInfo().Msgf("%T %+v", rtcpPacket, rtcpPacket)
// 				}
// 			}
// 		}
// 	}
// }
