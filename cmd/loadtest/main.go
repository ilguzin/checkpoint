// Loadtest is a separate workstation tool; it is not included in the VPS binary.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type access struct {
	PublicURL string `json:"publicURL"`
	Meeting   string `json:"meeting"`
	Host      string `json:"host"`
	Viewer    string `json:"viewer"`
}
type session struct{ Token, Session, Identity, ServerURL string }
type participant struct {
	room    *lksdk.Room
	session session
	packets atomic.Int64
	bytes   atomic.Int64
	first   atomic.Int64
	start   time.Time
}
type result struct {
	Scenario         string   `json:"scenario"`
	Participants     int      `json:"participants"`
	Publishers       int      `json:"publishers"`
	DurationSeconds  int      `json:"durationSeconds"`
	Successful       int      `json:"successful"`
	Failed           int      `json:"failed"`
	JoinP95MS        float64  `json:"joinP95Ms"`
	FirstRTPP95MS    float64  `json:"firstRtpP95Ms"`
	Packets          int64    `json:"receivedRtpPackets"`
	Bytes            int64    `json:"receivedRtpBytes"`
	ReceivingClients int      `json:"receivingClients"`
	Errors           []string `json:"errors,omitempty"`
}

var cfg access
var client = &http.Client{Timeout: 15 * time.Second}

func post(path, secret string, body any, out any) error {
	data, _ := json.Marshal(body)
	r, _ := http.NewRequest("POST", cfg.PublicURL+path, bytes.NewReader(data))
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	res, e := client.Do(r)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s HTTP %d", path, res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}
func connect(invite string, index int) (*participant, error) {
	p := &participant{start: time.Now()}
	if e := post("/api/join", "", map[string]string{"name": fmt.Sprintf("Load %03d", index), "invite": invite}, &p.session); e != nil {
		return nil, e
	}
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		go func() {
			for {
				packet, _, e := track.ReadRTP()
				if e != nil {
					return
				}
				p.packets.Add(1)
				p.bytes.Add(int64(packet.MarshalSize()))
				p.first.CompareAndSwap(0, time.Since(p.start).Milliseconds()+1)
			}
		}()
	}
	var e error
	p.room, e = lksdk.ConnectToRoomWithToken(p.session.ServerURL, p.session.Token, cb, lksdk.WithConnectTimeout(15*time.Second), lksdk.WithDisableTURN())
	if e != nil {
		_ = post("/api/leave", p.session.Session, map[string]any{}, nil)
		return nil, e
	}
	return p, nil
}
func closeParticipant(p *participant) {
	_ = post("/api/leave", p.session.Session, map[string]any{}, nil)
	p.room.Disconnect()
}
func percentile(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[(len(values)-1)*95/100]
}
func videoFrames(path string) ([][]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if len(b) < 32 || string(b[:4]) != "DKIF" {
		return nil, fmt.Errorf("VP8 IVF fixture required")
	}
	b = b[32:]
	var out [][]byte
	for len(b) >= 12 {
		size := int(binary.LittleEndian.Uint32(b[:4]))
		b = b[12:]
		if size > len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		out = append(out, b[:size])
		b = b[size:]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty video")
	}
	return out, nil
}
func publish(ctx context.Context, p *participant, frames [][]byte, source livekit.TrackSource) error {
	mime, clock, channels := webrtc.MimeTypeVP8, uint32(90000), uint16(0)
	duration := time.Second / 15
	if source == livekit.TrackSource_MICROPHONE {
		mime, clock, channels = webrtc.MimeTypeOpus, 48000, 2
		duration = 20 * time.Millisecond
		frames = [][]byte{{0xf8, 0xff, 0xfe}}
	}
	track, e := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: mime, ClockRate: clock, Channels: channels}, fmt.Sprint(source), "load")
	if e != nil {
		return e
	}
	// Wait for authoritative permission reconciliation after the subscribe-only join.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var out struct{ CanPublish bool }
		if e := post("/api/session", p.session.Session, map[string]any{}, &out); e == nil && out.CanPublish {
			time.Sleep(1200 * time.Millisecond)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, e = p.room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "Synthetic load", Source: source}); e != nil {
		return e
	}
	go func() {
		tick := time.NewTicker(duration)
		defer tick.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if track.WriteSample(media.Sample{Data: frames[i%len(frames)], Duration: duration}) != nil {
					return
				}
				i++
			}
		}
	}()
	return nil
}
func scenario(name string, count, publishers, seconds int, frames [][]byte) result {
	out := result{Scenario: name, Participants: count, Publishers: publishers, DurationSeconds: seconds}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var people []*participant
	var joins []float64
	for i := 0; i < count; i++ {
		invite := cfg.Meeting
		if name == "webinar" {
			invite = cfg.Viewer
			if i < publishers {
				invite = cfg.Host
			}
		}
		p, e := connect(invite, i)
		if e != nil {
			out.Failed++
			out.Errors = append(out.Errors, e.Error())
			continue
		}
		out.Successful++
		joins = append(joins, float64(time.Since(p.start).Milliseconds()))
		people = append(people, p)
		if i < publishers {
			if e = publish(ctx, p, frames, livekit.TrackSource_CAMERA); e != nil {
				out.Errors = append(out.Errors, "camera publication failed")
			}
			if e = publish(ctx, p, nil, livekit.TrackSource_MICROPHONE); e != nil {
				out.Errors = append(out.Errors, "audio publication failed")
			}
		}
	}
	if name == "meeting" && len(people) > 0 {
		p := people[0]
		if e := post("/api/screen", p.session.Session, map[string]bool{"claim": true}, nil); e == nil {
			if e = publish(ctx, p, frames, livekit.TrackSource_SCREEN_SHARE); e != nil {
				out.Errors = append(out.Errors, "screen publication failed")
			}
			go func() {
				timer := time.NewTicker(10 * time.Second)
				defer timer.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-timer.C:
						_ = post("/api/screen", p.session.Session, map[string]bool{"claim": true}, nil)
					}
				}
			}()
		} else {
			out.Errors = append(out.Errors, "screen lease failed")
		}
	}
	time.Sleep(time.Duration(seconds) * time.Second)
	cancel()
	var first []float64
	for _, p := range people {
		out.Packets += p.packets.Load()
		out.Bytes += p.bytes.Load()
		if p.first.Load() > 0 {
			out.ReceivingClients++
			first = append(first, float64(p.first.Load()-1))
		}
	}
	var wg sync.WaitGroup
	for _, p := range people {
		wg.Add(1)
		go func(p *participant) { defer wg.Done(); closeParticipant(p) }(p)
	}
	wg.Wait()
	out.JoinP95MS = percentile(joins)
	out.FirstRTPP95MS = percentile(first)
	return out
}
func attempts(count int) result {
	out := result{Scenario: "join-series", Participants: count}
	var times []float64
	for i := 0; i < count; i++ {
		start := time.Now()
		p, e := connect(cfg.Meeting, i)
		if e != nil {
			out.Failed++
			continue
		}
		out.Successful++
		times = append(times, float64(time.Since(start).Milliseconds()))
		closeParticipant(p)
	}
	out.JoinP95MS = percentile(times)
	return out
}
func main() {
	scenarioName := flag.String("scenario", "all", "all, meeting, webinar, or joins")
	accessPath := flag.String("access", ".dev/native/access.json", "private endpoint/invitation JSON")
	video := flag.String("video", ".dev/load/video.ivf", "synthetic VP8 IVF")
	output := flag.String("output", ".dev/load/report.json", "aggregate report")
	seconds := flag.Int("seconds", 30, "hold each scenario for this many seconds (1–14400)")
	joins := flag.Int("joins", 200, "sequential connection attempts")
	flag.Parse()
	if *seconds < 1 || *seconds > 14400 || *joins < 1 || *joins > 10000 {
		panic("invalid bounded duration/attempt count")
	}
	lksdk.SetLogger(logger.LogRLogger(logr.Discard()))
	data, e := os.ReadFile(*accessPath)
	if e != nil {
		panic(e)
	}
	if e = json.Unmarshal(data, &cfg); e != nil {
		panic(e)
	}
	frames, e := videoFrames(*video)
	if e != nil {
		panic(e)
	}
	started := time.Now().UTC()
	var results []result
	switch *scenarioName {
	case "all":
		results = []result{scenario("meeting", 20, 20, *seconds, frames), scenario("webinar", 103, 3, *seconds, frames), attempts(*joins)}
	case "meeting":
		results = []result{scenario("meeting", 20, 20, *seconds, frames)}
	case "webinar":
		results = []result{scenario("webinar", 103, 3, *seconds, frames)}
	case "joins":
		results = []result{attempts(*joins)}
	default:
		panic("scenario must be all, meeting, webinar, or joins")
	}
	report := map[string]any{"started": started, "finished": time.Now().UTC(), "scope": "synthetic transport load; deployment and generator context recorded separately", "video": "VP8 640x360 15fps, approximately 250 kbit/s, no simulcast", "audio": "synthetic Opus silence, 20ms packets", "results": results}
	body, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(*output, append(body, '\n'), 0644); e != nil {
		panic(e)
	}
	fmt.Println(string(body))
	for _, r := range results {
		if r.Failed > 0 || len(r.Errors) > 0 || (r.Scenario != "join-series" && r.ReceivingClients < r.Successful) {
			os.Exit(1)
		}
	}
}
