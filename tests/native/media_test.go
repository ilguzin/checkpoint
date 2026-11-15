//go:build nativeintegration

package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type access struct {
	Meeting string `json:"meeting"`
	Key     string `json:"api_key"`
	Secret  string `json:"api_secret"`
}
type session struct{ Token, Session, Identity string }

func post(t *testing.T, path, secret string, in any, want int, out any) {
	t.Helper()
	body, _ := json.Marshal(in)
	r, _ := http.NewRequest("POST", "http://127.0.0.1:18180"+path, bytes.NewReader(body))
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	client := http.Client{Timeout: 10 * time.Second}
	res, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	if res.StatusCode != want {
		t.Fatalf("%s: status %d, want %d", path, res.StatusCode, want)
	}
	if out != nil {
		if e = json.NewDecoder(res.Body).Decode(out); e != nil {
			t.Fatal(e)
		}
	}
}
func wait(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestScreenSourceEnforcedByLiveKit(t *testing.T) {
	b, e := os.ReadFile("../../.dev/native/access.json")
	if e != nil {
		t.Fatal(e)
	}
	var cfg access
	if e = json.Unmarshal(b, &cfg); e != nil {
		t.Fatal(e)
	}
	var a, bSession session
	post(t, "/api/join", "", map[string]string{"name": "SDK screen owner", "invite": cfg.Meeting}, 200, &a)
	post(t, "/api/join", "", map[string]string{"name": "SDK other speaker", "invite": cfg.Meeting}, 200, &bSession)
	defer post(t, "/api/leave", a.Session, map[string]any{}, 200, nil)
	defer post(t, "/api/leave", bSession.Session, map[string]any{}, 200, nil)
	ar, e := lksdk.ConnectToRoomWithToken("ws://127.0.0.1:18181", a.Token, nil, lksdk.WithConnectTimeout(10*time.Second),lksdk.WithDisableTURN())
	if e != nil {
		t.Fatal(e)
	}
	defer ar.Disconnect()
	br, e := lksdk.ConnectToRoomWithToken("ws://127.0.0.1:18181", bSession.Token, nil, lksdk.WithConnectTimeout(10*time.Second),lksdk.WithDisableTURN())
	if e != nil {
		t.Fatal(e)
	}
	defer br.Disconnect()
	service := lksdk.NewRoomServiceClient("http://127.0.0.1:7880", cfg.Key, cfg.Secret)
	current := func(id string) *livekit.ParticipantInfo {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		res, e := service.GetParticipant(ctx, &livekit.RoomParticipantIdentity{Room: "m0a-meeting", Identity: id})
		if e != nil {
			return nil
		}
		return res
	}
	wait(t, func() bool { p := current(a.Identity); return p != nil && p.Permission.CanPublish })
	publish := func(room *lksdk.Room, label string) (*lksdk.LocalTrackPublication, error) {
		track, e := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, label, "screen")
		if e != nil {
			return nil, e
		}
		return room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: label, Source: livekit.TrackSource_SCREEN_SHARE})
	}
	if _, e = publish(br, "unauthorized-screen"); e == nil {
		t.Fatal("LiveKit accepted screen without a lease")
	}
	post(t, "/api/screen", a.Session, map[string]bool{"claim": true}, 200, nil)
	pub, e := publish(ar, "authorized-screen")
	if e != nil {
		t.Fatal(e)
	}
	post(t, "/api/screen", bSession.Session, map[string]bool{"claim": true}, 409, nil)
	post(t, "/api/screen", a.Session, map[string]bool{"claim": false}, 200, nil)
	wait(t, func() bool {
		p := current(a.Identity)
		if p == nil {
			return false
		}
		for _, track := range p.Tracks {
			if track.Sid == pub.SID() {
				return false
			}
		}
		return true
	})
	post(t, "/api/screen", bSession.Session, map[string]bool{"claim": true}, 200, nil)
	if _, e = publish(br, "next-screen"); e != nil {
		t.Fatal(fmt.Errorf("second owner could not publish after confirmed revoke: %w", e))
	}
}
