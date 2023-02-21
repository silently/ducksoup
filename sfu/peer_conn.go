package sfu

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/ducksouplab/ducksoup/engine"
	"github.com/ducksouplab/ducksoup/types"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog"
)

// too many PLI may be requested when interaction starts
// (new peer joins, encoder detecting poor quality... to be investigated)
// that's why we throttle PLI request with initialPLIMinInterval and
// later with mainPLIMinInterval
const initialPLIMinInterval = 3 * time.Second
const mainPLIMinInterval = 1 * time.Second
const changePLIMinIntervalAfter = 7 * time.Second

// New type created mostly to extend webrtc.PeerConnection with additional methods
type peerConn struct {
	sync.Mutex
	*webrtc.PeerConnection
	userId         string
	i              *interaction
	lastPLI        time.Time
	pliMinInterval time.Duration
	ccEstimator    cc.BandwidthEstimator
}

// API

func newPionPeerConn(join types.JoinPayload, i *interaction) (ppc *webrtc.PeerConnection, ccEstimator cc.BandwidthEstimator, err error) {
	// create RTC API
	estimatorCh := make(chan cc.BandwidthEstimator, 1)
	api, err := engine.NewWebRTCAPI(estimatorCh)
	if err != nil {
		return
	}
	// configure and create a new RTCPeerConnection
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
			{
				URLs: []string{"stun:stun3.l.google.com:19302"},
			},
			{
				URLs: []string{"stun:stun.stunprotocol.org:3478"},
			},
		},
	}
	ppc, err = api.NewPeerConnection(config)
	// Wait until our Bandwidth Estimator has been created
	ccEstimator = <-estimatorCh
	return
}

func (pc *peerConn) prepareInTracks(join types.JoinPayload) (err error) {
	// accept one audio
	_, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	})
	if err != nil {
		pc.logError().Err(err).Msg("add_audio_transceiver_failed")
		return
	}

	// accept one video
	videoTransceiver, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	})
	if err != nil {
		pc.logError().Err(err).Msg("add_video_transceiver_failed")
		return
	}

	// force codec preference if H264 (so VP8 won't prevail)
	if join.VideoFormat == "H264" {
		err = videoTransceiver.SetCodecPreferences(engine.H264Codecs)
		if err != nil {
			pc.logError().Err(err).Msg("set_codec_preferences_failed")
			return
		}
	}
	return
}

func newPeerConn(join types.JoinPayload, i *interaction) (pc *peerConn, err error) {
	ppc, ccEstimator, err := newPionPeerConn(join, i)
	if err != nil {
		// pc is not created for now so we use the interaction logger
		i.logger.Error().Err(err).Str("user", join.UserId)
		return
	}

	// initial lastPLI far enough in the past
	lastPLI := time.Now().Add(-2 * initialPLIMinInterval)

	pc = &peerConn{sync.Mutex{}, ppc, join.UserId, i, lastPLI, initialPLIMinInterval, ccEstimator}

	// after an initial delay, change the minimum PLI interval
	go func() {
		<-time.After(changePLIMinIntervalAfter)
		pc.pliMinInterval = mainPLIMinInterval
	}()

	err = pc.prepareInTracks(join)
	return
}

func (pc *peerConn) logError() *zerolog.Event {
	return pc.i.logger.Error().Str("context", "signaling").Str("user", pc.userId)
}

func (pc *peerConn) logInfo() *zerolog.Event {
	return pc.i.logger.Info().Str("context", "signaling").Str("user", pc.userId)
}

func (pc *peerConn) logDebug() *zerolog.Event {
	return pc.i.logger.Debug().Str("context", "signaling").Str("user", pc.userId)
}

// pc callbacks trigger actions handled by ws or interaction or pc itself
func (pc *peerConn) handleCallbacks(ps *peerServer) {
	// trickle ICE. Emit server candidate to client
	pc.OnICECandidate(func(i *webrtc.ICECandidate) {
		if i == nil {
			// see https://pkg.go.dev/github.com/pion/webrtc/v3#PeerConnection.OnICECandidate
			return
		}

		candidateString, err := json.Marshal(i.ToJSON())
		if err != nil {
			pc.logError().Err(err).Msg("marshal_candidate_failed")
			return
		}

		ps.ws.sendWithPayload("candidate", string(candidateString))
	})

	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		ssrc := uint32(remoteTrack.SSRC())
		ps.i.addSSRC(ssrc, remoteTrack.Kind().String(), ps.userId)

		pc.logDebug().Str("context", "track").Str("kind", remoteTrack.Kind().String()).Uint32("ssrc", ssrc).Str("track", remoteTrack.ID()).Str("mime", remoteTrack.Codec().RTPCodecCapability.MimeType).Msg("in_track_received")
		ps.i.runMixerSliceFromRemote(ps, remoteTrack, receiver)
	})

	// if PeerConnection is closed remove it from global list
	pc.OnConnectionStateChange(func(p webrtc.PeerConnectionState) {
		pc.logInfo().Str("value", p.String()).Msg("connection_state_changed")
		switch p {
		case webrtc.PeerConnectionStateFailed:
			if err := pc.Close(); err != nil {
				pc.logError().Err(err).Msg("peer_connection_state_failed")
			}
		case webrtc.PeerConnectionStateClosed:
			ps.close("peer_connection_closed")
		}
	})

	// for logging

	pc.OnSignalingStateChange(func(state webrtc.SignalingState) {
		pc.logInfo().Str("value", state.String()).Msg("signaling_state_changed")
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		pc.logInfo().Str("value", state.String()).Msg("ice_connection_state_changed")
	})

	pc.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		pc.logInfo().Str("value", state.String()).Msg("ice_gathering_state_changed")
	})

	pc.OnNegotiationNeeded(func() {
		pc.logInfo().Msg("negotiation_needed")
	})

	// Debug: send periodic PLIs
	// ticker := time.NewTicker(2 * time.Second)
	// // defer ticker stop?
	// go func() {
	// 	for range ticker.C {
	// 		pc.forcedPLIRequest()
	// 	}
	// }()
}

func (pc *peerConn) writePLI(track *webrtc.TrackRemote, cause string) (err error) {
	err = pc.WriteRTCP([]rtcp.Packet{
		&rtcp.PictureLossIndication{
			MediaSSRC: uint32(track.SSRC()),
		},
	})
	if err != nil {
		pc.logError().Err(err).Str("context", "track").Msg("send_pli_failed")
	} else {
		pc.Lock()
		pc.lastPLI = time.Now()
		pc.Unlock()
		pc.logInfo().Str("context", "track").Str("cause", cause).Msg("pli_sent")
	}
	return
}

// func (pc *peerConn) forcedPLIRequest() {
// 	pc.Lock()
// 	defer pc.Unlock()

// 	for _, receiver := range pc.GetReceivers() {
// 		track := receiver.Track()
// 		if track != nil && track.Kind().String() == "video" {
// 			pc.writePLI(track)
// 		}
// 	}
// }

func (pc *peerConn) throttledPLIRequest(cause string) {
	pc.Lock()
	defer pc.Unlock()

	for _, receiver := range pc.GetReceivers() {
		track := receiver.Track()
		if track != nil && track.Kind().String() == "video" {
			durationSinceLastPLI := time.Since(pc.lastPLI)
			if durationSinceLastPLI < pc.pliMinInterval {
				// throttle: don't send too many PLIs
				pc.logInfo().Str("context", "track").Str("cause", cause).Msg("pli_skipped")
			} else {
				go pc.writePLI(track, cause)
			}
		}
	}
}
