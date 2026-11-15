//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type access struct{ Key, Secret, Meeting, Host, Viewer string }
type session struct{ Token, Session, Identity, Room string }

var client = &http.Client{Timeout: 8 * time.Second}

func call(t *testing.T, path, secret string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	r, err := http.NewRequest("POST", "http://127.0.0.1:18180"+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+secret)
	res, err := client.Do(r)
	if err != nil {
		t.Fatal("API request failed")
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal("invalid API response")
		}
	}
	return res.StatusCode
}
func join(t *testing.T, invite, name string) session {
	t.Helper()
	var s session
	if code := call(t, "/api/join", "", map[string]string{"invite": invite, "name": name}, &s); code != 200 {
		t.Fatalf("join status %d", code)
	}
	return s
}
func wait(t *testing.T, description string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(description)
}
func connect(t *testing.T, token string, callback *lksdk.RoomCallback) *lksdk.Room {
	t.Helper()
	r, err := lksdk.ConnectToRoomWithToken("ws://127.0.0.1:18181", token, callback, lksdk.WithConnectTimeout(10*time.Second))
	if err != nil {
		t.Fatal("LiveKit connect failed (inspect private container logs)")
	}
	t.Cleanup(func() { r.Disconnect() })
	return r
}
func canPublish(room *lksdk.Room) bool {
	p := room.LocalParticipant.Permissions()
	return p != nil && p.CanPublish
}
func track(t *testing.T) *lksdk.LocalTrack {
	t.Helper()
	tr, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}
func compose(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"compose", "--env-file", ".dev/compose.env", "-f", "deploy/dev/compose.yaml"}, args...)...)
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compose %v failed: %s", args, out)
	}
}
func health(component string) string {
	res, err := client.Get("http://127.0.0.1:18182/api/health")
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var data map[string]struct{ State string }
	_ = json.NewDecoder(res.Body).Decode(&data)
	return data[component].State
}

func TestLiveKitPermissionsMediaAndRestarts(t *testing.T) {
	b, err := os.ReadFile("../../.dev/access.json")
	if err != nil {
		t.Fatal("run scripts/dev.sh prepare/up first")
	}
	var cfg access
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	host := join(t, cfg.Host, "Integration host")
	viewer := join(t, cfg.Viewer, "Integration viewer")
	var packets atomic.Int64
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(tr *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		go func() {
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				packets.Add(1)
			}
		}()
	}
	hr := connect(t, host.Token, nil)
	vr := connect(t, viewer.Token, cb)
	wait(t, "host grant not applied", func() bool { return canPublish(hr) })
	if canPublish(vr) {
		t.Fatal("viewer allowed to publish")
	}
	rooms := lksdk.NewRoomServiceClient("http://127.0.0.1:17880", cfg.Key, cfg.Secret)
	info, err := rooms.GetParticipant(context.Background(), &livekit.RoomParticipantIdentity{Room: viewer.Room, Identity: viewer.Identity})
	if err != nil || info.Permission.CanPublish {
		t.Fatal("LiveKit did not enforce subscribe-only permissions")
	}
	denied := track(t)
	if _, err = vr.LocalParticipant.PublishTrack(denied, &lksdk.TrackPublicationOptions{Name: "forbidden", Source: livekit.TrackSource_MICROPHONE}); err == nil {
		t.Fatal("viewer published a track")
	}
	tr := track(t)
	if _, err = hr.LocalParticipant.PublishTrack(tr, &lksdk.TrackPublicationOptions{Name: "test-audio", Source: livekit.TrackSource_MICROPHONE}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = tr.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}, nil)
			}
		}
	}()
	wait(t, "no RTP received through LiveKit", func() bool { return packets.Load() > 20 })
	t.Log("real Opus RTP received; viewer publication denied by LiveKit permissions")
	set := func(publish bool) {
		t.Helper()
		if code := call(t, "/api/permissions", host.Session, map[string]any{"identity": viewer.Identity, "canPublish": publish}, nil); code != 200 {
			t.Fatalf("permission status %d", code)
		}
		wait(t, "role did not propagate", func() bool { return canPublish(vr) == publish })
	}
	set(true)
	promoted := track(t)
	pub, err := vr.LocalParticipant.PublishTrack(promoted, &lksdk.TrackPublicationOptions{Name: "allowed", Source: livekit.TrackSource_MICROPHONE})
	if err != nil {
		t.Fatal("promoted participant cannot publish")
	}
	_ = vr.LocalParticipant.UnpublishTrack(pub.SID())
	set(false)
	oldElevatedToken, err := auth.NewAccessToken(cfg.Key, cfg.Secret).SetIdentity(viewer.Identity).SetName("Integration viewer").SetValidFor(5 * time.Minute).SetVideoGrant(&auth.VideoGrant{RoomJoin: true, Room: viewer.Room, CanPublish: ptr(true), CanSubscribe: ptr(true)}).ToJWT()
	if err != nil {
		t.Fatal(err)
	}
	vr.Disconnect()
	vr = connect(t, oldElevatedToken, nil)
	time.Sleep(2 * time.Second)
	if canPublish(vr) {
		t.Fatal("old elevated token restored publication")
	}
	t.Log("promotion/demotion and replay of an elevated signed token passed")
	if os.Getenv("CHECKPOINT_TEST_RESTARTS") != "1" {
		t.Log("restart checks skipped; set CHECKPOINT_TEST_RESTARTS=1 on isolated stand")
		return
	}
	// Reconnect receiver to count media while only core is unavailable.
	vr.Disconnect()
	vr = connect(t, viewer.Token, cb)
	before := packets.Load()
	wait(t, "receiver did not resume", func() bool { return packets.Load() > before+20 })
	t.Cleanup(func() { compose(t, "start", "core", "livekit") })
	compose(t, "stop", "-t", "1", "core")
	before = packets.Load()
	wait(t, "Observer did not show independent core failure", func() bool { return health("core") == "outage" && health("signaling") == "operational" })
	if packets.Load() <= before+20 {
		t.Fatal("media did not continue during core outage")
	}
	compose(t, "start", "core")
	wait(t, "core not recovered", func() bool { return health("core") == "operational" })
	var refreshed session
	if code := call(t, "/api/session", viewer.Session, map[string]string{}, &refreshed); code != 200 {
		t.Fatal("session did not survive core restart")
	}
	check := connect(t, refreshed.Token, nil)
	if canPublish(check) {
		t.Fatal("role lost on restart")
	}
	t.Log("Observer survived core stop; RTP continued; saved session and demotion survived restart")
	compose(t, "stop", "-t", "1", "livekit")
	wait(t, "Observer did not show LiveKit failure", func() bool { return health("signaling") == "outage" && health("core") == "operational" })
	compose(t, "start", "livekit")
	wait(t, "LiveKit not recovered", func() bool { return health("signaling") == "operational" })
	connect(t, refreshed.Token, nil)
	t.Log("LiveKit outage detected; fresh connection succeeded after restart")
}
func ptr(v bool) *bool { return &v }
