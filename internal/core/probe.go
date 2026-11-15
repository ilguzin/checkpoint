package core

import (
	"net/http"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// A probe emits encoded synthetic silence, never captures a device, and uses an
// isolated expiring room. Public callers cannot choose a room or media grant.
func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	if !s.probeLimiter.allow(r) {
		fail(w, 429, "Connection checks are busy; try again in a minute")
		return
	}
	select {
	case s.probes <- struct{}{}:
	default:
		fail(w, 429, "Connection checks are busy")
		return
	}
	room := "probe-" + randomID()
	sender, _, err := s.store.Add("Connection check", room, false, true)
	if err != nil {
		<-s.probes
		fail(w, 503, "Connection check unavailable")
		return
	}
	viewer, secret, err := s.store.Add("Checking connection", room, false, false)
	if err != nil {
		_ = s.store.Leave(sender.Identity)
		<-s.probes
		fail(w, 503, "Connection check unavailable")
		return
	}
	_, err = s.store.db.Exec("UPDATE sessions SET expires=unixepoch()+45 WHERE room_id=?", room)
	if err != nil {
		<-s.probes
		fail(w, 503, "Connection check unavailable")
		return
	}
	token, err := s.token(sender, true)
	if err != nil {
		<-s.probes
		fail(w, 503, "Connection check unavailable")
		return
	}
	connection, err := lksdk.ConnectToRoomWithToken(s.cfg.LiveKitURL, token, nil, lksdk.WithConnectTimeout(5*time.Second), lksdk.WithDisableTURN())
	if err != nil {
		_ = s.store.Leave(sender.Identity)
		_ = s.store.Leave(viewer.Identity)
		<-s.probes
		fail(w, 503, "Call service unavailable")
		return
	}
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "connection-check", "synthetic")
	if err == nil {
		_, err = connection.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "Synthetic silence", Source: livekit.TrackSource_MICROPHONE})
	}
	if err != nil {
		connection.Disconnect()
		<-s.probes
		fail(w, 503, "Media check unavailable")
		return
	}
	go func() {
		defer func() {
			connection.Disconnect()
			_ = s.store.Leave(sender.Identity)
			_ = s.store.Leave(viewer.Identity)
			<-s.probes
		}()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-deadline.C:
				return
			case <-tick.C:
				if e := track.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}); e != nil {
					return
				}
			}
		}
	}()
	s.respondSession(w, viewer, secret)
}
