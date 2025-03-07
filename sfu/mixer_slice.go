package sfu

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/ducksouplab/ducksoup/config"
	"github.com/ducksouplab/ducksoup/env"
	"github.com/ducksouplab/ducksoup/gst"
	"github.com/ducksouplab/ducksoup/plot"
	"github.com/ducksouplab/ducksoup/sequencing"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog"
)

const (
	defaultInterpolatorStep = 30
	statsPeriod             = 1000
	diffThreshold           = 10
	// DISABLED: inputToOutputMaxFactor (too much artefact)
	// when reducing inputToOutputMaxFactor, ensure the EncoderControlPeriod is not too low
	// inputToOutputMaxFactor is only meant as a guard, and should not impact the output bitrate
	// too much
	// inputToOutputMaxFactor = 2
)

var plotBuffersRecordingModes = []string{"forced", "free", "reenc"}

type mixerSlice struct {
	sync.Mutex
	fromPs       *peerServer
	i            *interaction
	kind         string
	streamConfig config.SFUStream
	// webrtc
	input    *webrtc.TrackRemote
	output   *webrtc.TrackLocalStaticRTP
	receiver *webrtc.RTPReceiver
	// processing
	pipeline          *gst.Pipeline
	interpolatorIndex map[string]*sequencing.LinearInterpolator
	// controller
	senderControllerIndex map[string]*senderController // per user id
	targetBitrate         int
	// plots
	plotBuffers bool
	// stats
	lastStats     time.Time
	inputBits     int
	outputBits    int
	inputBitrate  int
	outputBitrate int
	// baseRTPTimestampIn uint32
	// status
	doneCh chan struct{} // stop processing when track is removed
	// analysic
	plot *plot.SlicePlot
}

// helpers

func minInt(v []int) (min int) {
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

// Creates a new mixerSlice with:
// - the same codec format as the incoming/remote one
// - a unique server-side trackId, but won't be reused in the browser, see https://developer.mozilla.org/en-US/docs/Web/API/MediaStreamTrack/id
// - a streamId shared among peerServer tracks (audio/video)
// newId := uuid.New().String()
func newMixerSlice(ps *peerServer, remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) (ms *mixerSlice, err error) {

	kind := remoteTrack.Kind().String()
	var streamConfig config.SFUStream
	if kind == "video" {
		streamConfig = config.SFU.Video
	} else if kind == "audio" {
		streamConfig = config.SFU.Audio
	} else {
		err := errors.New("invalid kind")
		ms.logError().Str("context", "track").Err(err).Msg("new_mixer_slice_failed")
		return nil, err
	}

	newId := remoteTrack.ID()
	localTrack, err := webrtc.NewTrackLocalStaticRTP(remoteTrack.Codec().RTPCodecCapability, newId, ps.streamId)

	if err != nil {
		ms.logError().Str("context", "track").Err(err).Msg("new_mixer_slice_failed")
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
		// plots
		plotBuffers: slices.Contains(plotBuffersRecordingModes, ps.i.jp.RecordingMode),
		// stats
		lastStats: time.Now(),
		// status
		doneCh: make(chan struct{}),
	}
	// analysis
	if env.GeneratePlots {
		ms.plot = plot.NewSlicePlot(ms, kind, ms.plotBuffers, ps.userId, ps.i.DataFolder()+"/plots")
	}

	return
}

func (ms *mixerSlice) Done() chan struct{} {
	return ms.doneCh
}

func (ms *mixerSlice) logError() *zerolog.Event {
	return ms.i.logger.Error().Str("context", "track").Str("user", ms.fromPs.userId)
}

func (ms *mixerSlice) logInfo() *zerolog.Event {
	return ms.i.logger.Info().Str("context", "track").Str("user", ms.fromPs.userId)
}

func (ms *mixerSlice) logDebug() *zerolog.Event {
	return ms.i.logger.Debug().Str("context", "track").Str("user", ms.fromPs.userId)
}

func (ms *mixerSlice) logTrace() *zerolog.Event {
	return ms.i.logger.Trace().Str("context", "track").Str("user", ms.fromPs.userId)
}

// Same ID as output track
func (ms *mixerSlice) ID() string {
	return ms.output.ID()
}

func (ms *mixerSlice) addSender(pc *peerConn, sender *webrtc.RTPSender) {
	params := sender.GetParameters()

	toUserId := pc.userId
	if len(params.Encodings) == 1 {
		sc := newSenderController(pc, ms, sender)
		ms.Lock()
		ms.senderControllerIndex[toUserId] = sc
		ms.Unlock()
		go sc.loop()
	} else {
		ms.logError().Str("toUser", toUserId).Str("cause", "wrong number of encoding parameters").Msg("add_sender_failed")
	}
}

func (l *mixerSlice) updateInputBits(n int) {
	// previously func (l *mixerSlice) scanInput(buf []byte, n int)
	// packet := &rtp.Packet{}
	// packet.Unmarshal(buf)

	// estimation (x8 for bytes) not taking int account headers
	// it seems using MarshalSize (like for outputBits below) does not give the right numbers due to packet 0-padding (so there's not need Unmarshalling bug)

	l.Lock()
	l.inputBits += n * 8
	l.Unlock()
}

func (ms *mixerSlice) Write(buf []byte) (err error) {
	n, err := ms.output.Write(buf)

	if err == nil {
		ms.Lock()
		// supposed constant 12 bytes header
		ms.outputBits += (n - 12) * 8
		ms.Unlock()
	}

	return
}

func (ms *mixerSlice) close() {
	ms.pipeline.Stop()
	close(ms.doneCh)
	ms.logInfo().Str("track", ms.ID()).Str("kind", ms.kind).Msg("out_track_stopped")
}

func (ms *mixerSlice) loop() {
	defer ms.close()

	pipeline, i, userId := ms.fromPs.pipeline, ms.fromPs.i, ms.fromPs.userId

	// gives pipeline a track to write to
	pipeline.BindTrackAutoStart(ms.kind, ms)
	// wait for audio and video
	<-pipeline.Started()
	i.start() // first pipeline started starts the interaction

	if ms.kind == "audio" { // add once
		i.addFiles(userId, pipeline.RecordingFiles) // for reference
	}

	go ms.loopReadRTCP()
	if ms.kind == "video" {
		go ms.loopEncoderController()
		if ms.plotBuffers {
			go ms.loopBufferPlots()
		}
	}
	go ms.loopStats()
	if env.GeneratePlots {
		go ms.plot.Loop()
	}

	// main loop start
	buf := make([]byte, config.SFU.Common.MTU)

	if ms.fromPs.jp.RecordingMode == "bypass" {
	bypass:
		for {
			select {
			case <-ms.fromPs.isDone():
				// interaction OR peer is done
				break bypass
			default:
				n, _, err := ms.input.Read(buf)
				if err != nil {
					break bypass
				}
				ms.Write(buf[:n])
			}
		}
	} else {
	toPipeline:
		for {
			select {
			case <-ms.fromPs.isDone():
				// interaction OR peer is done
				break toPipeline
			default:
				n, _, err := ms.input.Read(buf)
				if err != nil {
					break toPipeline
				}
				ms.pipeline.PushRTP(ms.kind, buf[:n])
				// for stats
				go ms.updateInputBits(n)
				// time

				// plot rtp timestamp
				// r := &rtp.Packet{}
				// if err := r.Unmarshal(buf[:n]); err == nil {
				// 	if ms.baseRTPTimestampIn == 0 {
				// 		ms.baseRTPTimestampIn = r.Timestamp
				// 	} else {
				// 		elapsedRTP := (r.Timestamp - ms.baseRTPTimestampIn) * 1000 / ms.input.Codec().ClockRate
				// 		sinceStart := time.Since(i.startedAt).Milliseconds()
				// 		ms.plot.AddRtpDiffIn(sinceStart - int64(elapsedRTP))
				// 	}
				// }
			}
		}
	}
}

func (ms *mixerSlice) updateTargetBitrates(targetBitrate int) {
	ms.Lock()
	ms.targetBitrate = targetBitrate
	ms.Unlock()
	ms.pipeline.SetEncodingBitrate(ms.kind, targetBitrate)
	// format and log
	msg := fmt.Sprintf("%s_target_bitrate_updated", ms.kind)
	ms.logInfo().Int("value", targetBitrate/1000).Str("unit", "kbit/s").Msg(msg)
	// plot
	if env.GeneratePlots {
		ms.plot.AddTarget(targetBitrate)
	}
}

// func (ms *mixerSlice) checkOutputBitrate() {
// 	if ms.kind == "video" {
// 		ms.Lock()
// 		if ms.outputBitrate < ms.streamConfig.MinBitrate {
// 			ms.fromPs.pc.managedPLIRequest("output_bitrate_is_too_low")
// 		}
// 		ms.Unlock()
// 	}
// }

func (ms *mixerSlice) loopReadRTCP() {
	for {
		select {
		case <-ms.Done():
			return
		default:
			// ReadRTCP unmarshals but also split packets and run interceptors
			packets, _, err := ms.receiver.ReadRTCP()
			if err != nil {
				if err != io.EOF && err != io.ErrClosedPipe {
					ms.logError().Err(err).Msg("rtcp_on_receiver_failed")
				}
				return
			}

			for _, packet := range packets {
				if buf, err := packet.Marshal(); err == nil {
					ms.pipeline.PushRTCP(ms.kind, buf)
				}
				ms.logTrace().Str("type", fmt.Sprintf("%T", packet)).Str("packet", fmt.Sprintf("%+v", packet)).Msg("received_rtcp_on_receiver")
			}
		}
	}
}

func (ms *mixerSlice) loopEncoderController() {
	// sleep a bit to be closer to latest update from sender controller,
	// (if encoderControlPeriod is a multiple of gccPeriod)
	time.Sleep(50 * time.Millisecond)
	// update encoding bitrate on tick and according to minimum controller rate
	encoderTicker := time.NewTicker(time.Duration(config.SFU.Common.EncoderControlPeriod) * time.Millisecond)
	defer encoderTicker.Stop()
	for {
		select {
		case <-ms.Done():
			return
		case <-encoderTicker.C:
			if len(ms.senderControllerIndex) > 0 {
				rates := []int{}
				for _, sc := range ms.senderControllerIndex {
					if ms.kind == "video" {
						rates = append(rates, sc.optimalRate())
					}
				}
				// DISABLED no need to encode more than inputToOutputMaxFactor times the inputBitrate
				// inputDependentRate := int(inputToOutputMaxFactor * (float64(ms.inputBitrate)))
				// rates = append(rates, inputDependentRate)
				// END DISABLED
				newPotentialRate := minInt(rates)

				if ms.pipeline != nil && newPotentialRate > 0 {
					ms.updateTargetBitrates(newPotentialRate)

					// DISABLED throttling update
					// // skip updating previous value and encoding rate too often
					// ms.Lock()
					// diff := helpers.AbsPercentageDiff(ms.targetBitrate, newPotentialRate)
					// ms.Unlock()
					// // diffIsBigEnough: works also for diff being Inf+ (when updating from 0, diff is Inf+)
					// diffIsBigEnough := diff > diffThreshold
					// diffToMax := diff > 0 && (newPotentialRate == ms.streamConfig.MaxBitrate)
					// if diffIsBigEnough || diffToMax {
					// 	go ms.updateTargetBitrates(newPotentialRate)
					// }
					// END DISABLED
				}
			}
		}
	}
}

func (ms *mixerSlice) loopBufferPlots() {
	ticker := time.NewTicker(time.Duration(config.SFU.Common.EncoderControlPeriod) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ms.Done():
			return
		case <-ticker.C:
			names := []string{"video_rtp_src", "video_queue_bef_depay", "video_queue_bef_drymux", "video_queue_bef_wetmux", "video_queue_bef_sink"}
			if len(ms.fromPs.jp.VideoFx) > 0 {
				names = []string{"video_rtp_src", "video_queue_bef_drymux", "video_queue_bef_drymux", "video_queue_bef_fx", "video_queue_aft_fx", "video_queue_bef_dec", "video_queue_aft_dec", "video_queue_bef_wetmux", "video_queue_bef_sink"}
			}
			for _, n := range names {
				l := ms.pipeline.GetCurrentLevelTime(n) / 1000000 // ns -> ms
				ms.plot.AddCurrentLevelTime(n, l)
				ms.logInfo().Str("name", n).Uint64("value", l).Msg("poll_current_level_time")
			}
		}
	}
}

func (ms *mixerSlice) loopStats() {
	statsTicker := time.NewTicker(statsPeriod * time.Millisecond)
	defer statsTicker.Stop()
	for {
		select {
		case <-ms.Done():
			return
		case tickTime := <-statsTicker.C:
			ms.Lock()
			sinceLastTick := tickTime.Sub(ms.lastStats).Seconds()
			if sinceLastTick == 0 {
				break
			}
			// update bitrates
			ms.inputBitrate = int(float64(ms.inputBits) / sinceLastTick)
			ms.outputBitrate = int(float64(ms.outputBits) / sinceLastTick)
			// plot
			if env.GeneratePlots {
				ms.plot.AddInput(ms.inputBitrate)
				ms.plot.AddOutput(ms.outputBitrate)
			}

			// reset cumulative bits and lastStats
			ms.inputBits = 0
			ms.outputBits = 0
			ms.lastStats = tickTime
			// may send a PLI if too low -> disabled since does not solve the encoding crash
			//ms.checkOutputBitrate()
			// log
			displayInputBitrateKbs := uint64(ms.inputBitrate / 1000)
			displayOutputBitrateKbs := uint64(ms.outputBitrate / 1000)
			displayOutputTargetBitrateKbs := uint64(ms.targetBitrate / 1000)

			inputMsg := fmt.Sprintf("%s_in_bitrate", ms.output.Kind().String())
			outputMsg := fmt.Sprintf("%s_out_bitrate", ms.output.Kind().String())
			ms.Unlock()

			ms.logDebug().Uint64("value", displayInputBitrateKbs).Str("unit", "kbit/s").Msg(inputMsg)
			ms.logDebug().Uint64("value", displayOutputBitrateKbs).Uint64("target", displayOutputTargetBitrateKbs).Str("unit", "kbit/s").Msg(outputMsg)
		}
	}
}
