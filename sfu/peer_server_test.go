package sfu

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/ducksouplab/ducksoup/types"
	"github.com/silently/wsmock"
)

var durationUnit = 100 * time.Millisecond

func messageInWithPayload(kind string, payload any) messageIn {
	m, _ := json.Marshal(payload)
	return messageIn{kind, string(m)}
}

func TestRunPeerServer_Join_Failure(t *testing.T) {
	t.Run("fails when first message isn't of kind 'join'", func(t *testing.T) {
		conn, rec := wsmock.NewGorillaMockAndRecorder(t)
		go RunPeerServer("http://origin.test", conn)
		conn.Send(messageIn{"unknown", ""})
		rec.NewAssertion().NextToBe(messageOut{Kind: "error-join"})
		rec.RunAssertions(durationUnit)
	})

	t.Run("fails when join message does not contain an interactionName", func(t *testing.T) {
		conn, rec := wsmock.NewGorillaMockAndRecorder(t)
		go RunPeerServer("http://origin.test", conn)
		conn.Send(messageInWithPayload("join", types.JoinPayload{UserId: "user1"}))
		rec.NewAssertion().NextToBe(messageOut{Kind: "error-join"})
		rec.RunAssertions(durationUnit)
	})

	t.Run("fails when join message does not contain a userId", func(t *testing.T) {
		conn, rec := wsmock.NewGorillaMockAndRecorder(t)
		go RunPeerServer("http://origin.test", conn)
		conn.Send(messageInWithPayload("join", types.JoinPayload{InteractionName: "interaction1"}))
		rec.NewAssertion().NextToBe(messageOut{Kind: "error-join"})
		rec.RunAssertions(durationUnit)
	})
}

func TestRunPeerServer_Join_Success(t *testing.T) {
	t.Run("receives 'joined', 'offer' with 2 tracks and 'candidate' after successful join", func(t *testing.T) {
		conn, rec := wsmock.NewGorillaMockAndRecorder(t)
		go RunPeerServer("http://origin.test", conn)
		conn.Send(messageInWithPayload("join", types.JoinPayload{
			InteractionName: "interaction2",
			UserId:          "user2",
		}))
		rec.NewAssertion().
			OneToContain("joined").
			OneToMatch(regexp.MustCompile("offer.*rtpmap.*rtpmap")) // 2 rtpmap => 2 tracks
		rec.NewAssertion().
			OneToMatch(regexp.MustCompile("candidate.*srflx"))

		// log messages
		// rec.NewAssertion().With(func(end bool, latest any, all []any) (done bool, passed bool, err string) {
		// 	log.Printf("[rec] message received: %+v\n", latest)
		// 	return end, true, ""
		// })

		rec.RunAssertions(10 * durationUnit)
	})
}
