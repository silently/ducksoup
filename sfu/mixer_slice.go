package sfu

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/creamlab/ducksoup/gst"
	"github.com/creamlab/ducksoup/helpers"
	"github.com/creamlab/ducksoup/sequencing"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog"
)

const (
	defaultInterpolatorStep = 30
	maxInterpolatorDuration = 5000
	encoderPeriod           = 1000
	statsPeriod             = 3000
	logPeriod               = 7300
	diffThreshold           = 10
)

type mixerSlice struct {
	sync.Mutex
	fromPs *peerServer
	r      *room
	kind   string
	// webrtc
	input    *webrtc.TrackRemote
	output   *webrtc.TrackLocalStaticRTP
	receiver *webrtc.RTPReceiver
	// processing
	pipeline          *gst.Pipeline
	interpolatorIndex map[string]*sequencing.LinearInterpolator
	// controller
	senderControllerIndex map[string]*senderController // per user id
	optimalBitrate        uint64
	encoderTicker         *time.Ticker
	// stats
	statsTicker   *time.Ticker
	logTicker     *time.Ticker
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

func newMixerSlice(ps *peerServer, remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) (slice *mixerSlice, err error) {
	// create a new mixerSlice with:
	// - the same codec format as the incoming/remote one
	// - a unique server-side trackId, but won't be reused in the browser, see https://developer.mozilla.org/en-US/docs/Web/API/MediaStreamTrack/id
	// - a streamId shared among peerServer tracks (audio/video)
	// newId := uuid.New().String()
	kind := remoteTrack.Kind().String()
	if kind != "audio" && kind != "video" {
		return nil, errors.New("invalid kind")
	}

	newId := remoteTrack.ID()
	localTrack, err := webrtc.NewTrackLocalStaticRTP(remoteTrack.Codec().RTPCodecCapability, newId, ps.streamId)

	if err != nil {
		return
	}

	slice = &mixerSlice{
		fromPs: ps,
		r:      ps.r,
		kind:   kind,
		// webrtc
		input:    remoteTrack,
		output:   localTrack,
		receiver: receiver, // TODO read RTCP?
		// processing
		pipeline:          ps.pipeline,
		interpolatorIndex: make(map[string]*sequencing.LinearInterpolator),
		// controller
		senderControllerIndex: map[string]*senderController{},
		encoderTicker:         time.NewTicker(encoderPeriod * time.Millisecond),
		// stats
		statsTicker: time.NewTicker(statsPeriod * time.Millisecond),
		logTicker:   time.NewTicker(logPeriod * time.Millisecond),
		lastStats:   time.Now(),
		// status
		endCh: make(chan struct{}),
	}
	return
}

func (s *mixerSlice) logError() *zerolog.Event {
	return s.r.logError().Str("fromUser", s.fromPs.userId)
}

func (s *mixerSlice) logInfo() *zerolog.Event {
	return s.r.logInfo().Str("fromUser", s.fromPs.userId)
}

func (s *mixerSlice) logTrace() *zerolog.Event {
	return s.r.logTrace().Str("fromUser", s.fromPs.userId)
}

// Same ID as output track
func (s *mixerSlice) ID() string {
	return s.output.ID()
}

func (s *mixerSlice) addSender(sender *webrtc.RTPSender, toUserId string) {
	params := sender.GetParameters()

	if len(params.Encodings) == 1 {
		sc := newSenderController(sender, s, toUserId)
		s.Lock()
		s.senderControllerIndex[toUserId] = sc
		s.Unlock()
		go sc.runListener()
	} else {
		s.logError().Str("toUser", toUserId).Msg("[slice] can't add sender: wrong number of encoding parameters")
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
	s.statsTicker.Stop()
	s.encoderTicker.Stop()
	s.logTicker.Stop()
	close(s.endCh)
}

func (s *mixerSlice) loop() {
	pipeline, room, pc, userId := s.fromPs.pipeline, s.fromPs.r, s.fromPs.pc, s.fromPs.userId

	// returns a callback to push buffer to
	outputFiles := pipeline.BindTrack(s.kind, s)
	if s.kind == "video" {
		pipeline.BindPLICallback(func() {
			pc.throttledPLIRequest(200)
		})
	}
	if outputFiles != nil {
		room.addFiles(userId, outputFiles)
	}
	go s.runTickers()
	// go s.runReceiverListener()

	defer func() {
		s.logInfo().Msgf("[slice] stopped %s track %s", s.kind, s.ID())
		s.stop()
	}()

	buf := make([]byte, defaultMTU)
	for {
		select {
		case <-room.endCh:
			// trial is over, no need to trigger signaling on every closing track
			return
		case <-s.fromPs.closedCh:
			// peer may quit early (for instance page refresh), other peers need to be updated
			return
		default:
			i, _, err := s.input.Read(buf)
			if err != nil {
				return
			}
			s.pipeline.PushRTP(s.kind, buf[:i])
			// for stats
			go s.scanInput(buf, i)
		}
	}
}

func (s *mixerSlice) runTickers() {
	// update encoding bitrate on tick and according to minimum controller rate
	go func() {
		for range s.encoderTicker.C {
			if len(s.senderControllerIndex) > 0 {
				rates := []uint64{}
				for _, sc := range s.senderControllerIndex {
					rates = append(rates, sc.optimalBitrate)
				}
				newPotentialRate := minUint64(rates)
				if s.pipeline != nil && newPotentialRate > 0 {
					// skip updating previous value and encoding rate too often
					diff := helpers.AbsPercentageDiff(s.optimalBitrate, newPotentialRate)
					if diff > diffThreshold { // works also for diff being Inf+ (when updating from 0, diff is Inf+)
						s.Lock()
						s.optimalBitrate = newPotentialRate
						s.Unlock()
						s.pipeline.SetEncodingRate(s.kind, newPotentialRate)
						// format and log
						display := fmt.Sprintf("%v kbit/s", newPotentialRate/1000)
						s.logTrace().Msgf("[slice] %s target bitrate: %s", s.kind, display)
					}
				}
			}
		}
	}()

	go func() {
		for tickTime := range s.statsTicker.C {
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
			displayInputBitrateKbs := s.inputBitrate / 1000
			displayOutputBitrateKbs := s.outputBitrate / 1000
			s.logTrace().Msgf("[slice] %s input bitrate: %v kbit/s", s.output.Kind().String(), displayInputBitrateKbs)
			s.logTrace().Msgf("[slice] %s output bitrate: %v kbit/s", s.output.Kind().String(), displayOutputBitrateKbs)
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
// 					s.logError().Err(err).Msg("can't read RTCP packet")
// 				}
// 				return
// 			}
// 			// TODO: send to rtpjitterbuffer sink_rtcp
// 			//s.pipeline.PushRTCP(s.kind, buf[:i])

// 			packets, err := rtcp.Unmarshal(buf[:i])
// 			if err != nil {
// 				s.logError().Err(err).Msg("can't unmarshal RTCP packet")
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
