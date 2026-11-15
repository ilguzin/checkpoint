package minutes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/at-wat/ebml-go/mkvcore"
	"github.com/at-wat/ebml-go/webm"
	"github.com/livekit/media-sdk/jitter"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/unix"
)

// Audio stays Opus in timestamped WebM segments while capturing. FFmpeg is only
// used for final mix/ASR conversion; neither codec nor RTP transport is reinvented.
type capture struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	manifest Manifest
	dir      string
	sync     *synchronizer.Synchronizer
	guard    *guard
	stopping bool
	failure  string
	tracks   map[*webrtc.TrackRemote]bool
}
type syncedFile struct{ *os.File }

func (f syncedFile) Close() error {
	e := f.Sync()
	c := f.File.Close()
	if e != nil {
		return e
	}
	return c
}
func runRecording(ctx context.Context, client *Client, t Task) error {
	g := newGuard(ctx, client, t)
	defer g.close()
	dir := taskDir(t)
	if t.Run.State == "finalizing" {
		var m Manifest
		b, e := os.ReadFile(filepath.Join(dir, "capture.json"))
		if e != nil {
			return g.result(Result{Error: "capture_missing"})
		}
		if json.Unmarshal(b, &m) != nil {
			return g.result(Result{Error: "invalid_capture"})
		}
		m.Partial = true
		m.Reason = t.Run.StopReason
		if t.Run.Ended > 0 {
			m.Ended = t.Run.Ended
		}
		if m.Ended <= m.Started {
			m.Ended = m.Started + 1
		}
		return finishRecording(g, t, m)
	}
	if e := os.MkdirAll(filepath.Join(dir, "sources"), 0700); e != nil {
		return g.result(Result{Error: "storage_unavailable"})
	}
	start := time.Now()
	cap := &capture{dir: dir, guard: g, tracks: map[*webrtc.TrackRemote]bool{}, manifest: Manifest{Run: t.Run.ID, Started: start.Unix(), Sources: []Source{}}, sync: synchronizer.NewSynchronizerWithOptions(synchronizer.WithSenderReportSyncMode(synchronizer.SenderReportSyncModeRebase))}
	cap.sync.SetMediaRunningTime(func() (time.Duration, bool) { return time.Since(start), true })
	if e := AtomicJSON(filepath.Join(dir, "capture.json"), cap.manifest); e != nil {
		return g.result(Result{Error: "storage_unavailable"})
	}
	cb := lksdk.NewRoomCallback()
	cb.OnTrackSubscribed = func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
		if pub.Source() != livekitMicrophone || track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		cap.mu.Lock()
		if cap.stopping {
			cap.mu.Unlock()
			return
		}
		if len(cap.tracks) >= t.Config.MaxSources || len(cap.manifest.Sources) >= t.Config.MaxSources*8 {
			cap.failure = "source_limit"
			cap.mu.Unlock()
			return
		}
		cap.tracks[track] = true
		cap.wg.Add(1)
		cap.mu.Unlock()
		go cap.track(g.ctx, t, track, pub, p)
	}
	cb.OnDisconnected = func() {
		cap.mu.Lock()
		if !cap.stopping {
			cap.failure = "recorder_interrupted"
		}
		cap.mu.Unlock()
	}
	// A local recorder may use the host network while Core uses a Compose DNS
	// address. Keep the room-scoped token, but allow its signalling route to differ.
	serverURL := t.URL
	if localURL := os.Getenv("CHECKPOINT_RECORDER_LIVEKIT_URL"); localURL != "" {
		serverURL = localURL
	}
	room, e := lksdk.ConnectToRoomWithToken(serverURL, t.Token, cb, lksdk.WithAutoSubscribe(true), lksdk.WithConnectTimeout(8*time.Second))
	if e != nil {
		return g.result(Result{Error: "connect_failed"})
	}
	g.setPhase("recording", start.Unix(), "")
	// Start is only published after LiveKit confirms the recorder connection.
	var directive Directive
	e = client.Call(g.ctx, "/heartbeat", Heartbeat{Run: t.Run.ID, Lease: t.Lease, Phase: "recording", Started: start.Unix()}, &directive)
	if e != nil {
		room.Disconnect()
		return e
	}
	g.mu.Lock()
	g.directive = directive
	g.mu.Unlock()
	reason := "manual"
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastSpace := time.Now()
	localDeadline := start.Add(time.Duration(t.Config.ConfirmationSeconds) * time.Second)
	appliedWindow := 0
loop:
	for {
		select {
		case <-g.ctx.Done():
			reason = "shutdown"
			break loop
		case <-ticker.C:
		}
		d := g.snapshot()
		if d.Stop {
			break
		}
		if d.Window > appliedWindow && !time.Now().Before(localDeadline) {
			reason = "confirmation_timeout"
			break
		}
		if d.Window == appliedWindow+1 {
			localDeadline = localDeadline.Add(time.Duration(t.Config.ConfirmationSeconds) * time.Second)
			appliedWindow = d.Window
		}
		if !time.Now().Before(localDeadline) {
			reason = "confirmation_timeout"
			break
		}
		if t.Config.MaxRunSeconds > 0 && time.Since(start) >= time.Duration(t.Config.MaxRunSeconds)*time.Second {
			reason = "max_duration"
			break
		}
		cap.mu.Lock()
		failure := cap.failure
		cap.mu.Unlock()
		if failure != "" {
			reason = failure
			break
		}
		if time.Since(lastSpace) >= 2*time.Second {
			lastSpace = time.Now()
			var fs unix.Statfs_t
			if unix.Statfs(dir, &fs) != nil || fs.Bavail*uint64(fs.Bsize) < t.Config.MinFreeBytes {
				reason = "storage_limit"
				break
			}
			size, e := directoryBytes(t.Config.Storage)
			if e != nil || size >= t.Config.MaxStorageBytes {
				reason = "storage_limit"
				break
			}
		}
	}
	cap.mu.Lock()
	cap.stopping = true
	for tr := range cap.tracks {
		_ = tr.SetReadDeadline(time.Now())
	}
	cap.mu.Unlock()
	room.Disconnect()
	cap.wg.Wait()
	cap.sync.End()
	cap.mu.Lock()
	m := cap.manifest
	cap.mu.Unlock()
	m.Ended = max(time.Now().Unix(), m.Started+1)
	m.Reason = reason
	m.Partial = reason == "shutdown" || reason == "recorder_interrupted" || reason == "source_limit"
	if e := AtomicJSON(filepath.Join(dir, "capture.json"), m); e != nil {
		return g.result(Result{Error: "manifest_write_failed"})
	}
	g.setPhase("finalizing", start.Unix(), reason)
	// Persist automatic stop reason before finalization. Failure leaves durable
	// capture metadata for the next recorder's recovery lease.
	var ignored Directive
	if e = client.Call(context.Background(), "/heartbeat", Heartbeat{Run: t.Run.ID, Lease: t.Lease, Phase: "finalizing", Started: start.Unix(), Reason: reason}, &ignored); e != nil {
		return e
	}
	return finishRecording(g, t, m)
}

const livekitMicrophone = 2

func (c *capture) track(ctx context.Context, t Task, track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
	defer c.wg.Done()
	defer func() { c.mu.Lock(); delete(c.tracks, track); c.mu.Unlock() }()
	if !strings.EqualFold(track.Codec().MimeType, "audio/opus") {
		c.mu.Lock()
		c.failure = "unsupported_codec"
		c.mu.Unlock()
		return
	}
	syncTrack := c.sync.AddTrack(track, p.Identity())
	defer c.sync.RemoveTrack(track.ID())
	pub.OnRTCP(c.sync.OnRTCP)
	c.mu.Lock()
	index := len(c.manifest.Sources)
	id := fmt.Sprintf("source-%04d", index)
	source := Source{ID: id, Identity: p.Identity(), Name: p.Name(), Track: pub.SID(), File: "sources/" + id + ".webm"}
	if original, ok := c.guard.snapshot().Sources[p.Identity()]; ok {
		source.UserID = original.UserID
		source.Name = original.Name
	}
	c.manifest.Sources = append(c.manifest.Sources, source)
	c.persistLocked()
	c.mu.Unlock()
	f, e := os.OpenFile(filepath.Join(c.dir, source.File), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		c.fail("storage_limit")
		return
	}
	// OpusHead: version 1, stereo, no pre-skip, 48 kHz, mapping family 0.
	head := []byte{'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 2, 0, 0, 0x80, 0xbb, 0, 0, 0, 0, 0}
	writers, e := webm.NewSimpleBlockWriter(syncedFile{f}, []webm.TrackEntry{{TrackNumber: 1, TrackUID: 1, CodecID: "A_OPUS", CodecPrivate: head, TrackType: 2, SeekPreRoll: 80000000, Audio: &webm.Audio{SamplingFrequency: 48000, Channels: 2}}}, mkvcore.WithOnErrorHandler(func(err error) { c.fail("storage_limit") }), mkvcore.WithOnFatalHandler(func(err error) { c.fail("storage_limit") }))
	if e != nil {
		f.Close()
		c.fail("storage_limit")
		return
	}
	defer writers[0].Close()
	builder := samplebuilder.New(32, &codecs.OpusPacket{}, 48000, samplebuilder.WithPacketDroppedHandler(func() { source.Dropped++ }))
	first := true
	lastSync := time.Now()
	lastPTS := int64(-1)
	write := func(packets []*rtp.Packet) {
		for _, packet := range packets {
			pts, e := syncTrack.GetPTS(jitter.ExtPacket{Packet: packet, ReceivedAt: time.Now()})
			if e != nil {
				source.Dropped++
				continue
			}
			// Synchronizer's epoch is the first media packet, not the recorder join.
			ms := pts.Milliseconds() + (c.sync.GetStartedAt()/int64(time.Millisecond) - c.manifest.Started*1000)
			if ms < 0 || ms < lastPTS {
				source.Dropped++
				continue
			}
			if _, e = writers[0].Write(true, ms, packet.Payload); e != nil {
				c.fail("storage_limit")
				return
			}
			lastPTS = ms
			if source.Packets == 0 {
				source.StartMS = ms
			}
			source.EndMS = ms + 20
			source.Packets++
		}
	}
	for ctx.Err() == nil {
		_ = track.SetReadDeadline(time.Now().Add(time.Second))
		packet, _, err := track.ReadRTP()
		if err != nil {
			c.mu.Lock()
			stop := c.stopping
			c.mu.Unlock()
			if stop || ctx.Err() != nil {
				break
			}
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				continue
			}
			break
		}
		if first {
			syncTrack.Initialize(packet)
			first = false
		}
		builder.Push(packet)
		write(builder.PopPackets())
		if time.Since(lastSync) > time.Second {
			_ = f.Sync()
			c.mu.Lock()
			c.manifest.Sources[index] = source
			c.persistLocked()
			c.mu.Unlock()
			lastSync = time.Now()
		}
	}
	write(builder.ForcePopPackets())
	c.mu.Lock()
	c.manifest.Sources[index] = source
	c.persistLocked()
	c.mu.Unlock()
}
func (c *capture) fail(reason string) { c.mu.Lock(); c.failure = reason; c.mu.Unlock() }
func (c *capture) persistLocked() {
	if AtomicJSON(filepath.Join(c.dir, "capture.json"), c.manifest) != nil {
		c.failure = "storage_limit"
	}
}
func finishRecording(g *guard, t Task, m Manifest) error {
	g.setPhase("finalizing", m.Started, m.Reason)
	if g.ctx.Err() != nil {
		return g.ctx.Err()
	}
	// An existing final manifest means a crash occurred between fsync and the
	// database commit. Re-submit it without generating a duplicate processing job.
	var saved Manifest
	if b, e := os.ReadFile(filepath.Join(taskDir(t), "manifest.json")); e == nil && json.Unmarshal(b, &saved) == nil && saved.Run == t.Run.ID && saved.MixBytes > 0 {
		return publishCapture(g, t, saved)
	}
	if e := Mix(g.ctx, t.Config, taskDir(t), &m); e != nil {
		return g.result(Result{Error: "mix_failed"})
	}
	if t.Run.MinutesRequested {
		for i := range m.Sources {
			src := &m.Sources[i]
			if src.Packets == 0 {
				continue
			}
			file, err := os.Open(filepath.Join(taskDir(t), src.File))
			if err != nil {
				return err
			}
			digest := sha256.New()
			src.Bytes, err = io.Copy(digest, file)
			file.Close()
			if err != nil {
				return err
			}
			src.SHA256 = hex.EncodeToString(digest.Sum(nil))
		}
	}
	if e := AtomicJSON(filepath.Join(taskDir(t), "manifest.json"), m); e != nil {
		return e
	}
	return publishCapture(g, t, m)
}
func Mix(ctx context.Context, c Config, dir string, m *Manifest) error {
	duration := max(1, m.Ended-m.Started)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-copyts"}
	filters := []string{}
	inputs := []string{}
	for _, src := range m.Sources {
		if src.Packets == 0 {
			continue
		}
		if !validSourcePath(src.File) {
			return errors.New("invalid source path")
		}
		args = append(args, "-threads", "1", "-itsoffset", fmt.Sprintf("%.3f", float64(src.StartMS)/1000), "-i", filepath.Join(dir, src.File))
		i := len(inputs)
		name := fmt.Sprintf("a%d", i)
		filters = append(filters, fmt.Sprintf("[%d:a]aresample=48000:async=1:first_pts=0,apad,atrim=duration=%d[%s]", i, duration, name))
		inputs = append(inputs, "["+name+"]")
	}
	if len(inputs) == 0 {
		args = append(args, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
	} else {
		filters = append(filters, fmt.Sprintf("%samix=inputs=%d:normalize=0:duration=longest,alimiter=limit=0.95:level=false[out]", strings.Join(inputs, ""), len(inputs)))
		args = append(args, "-filter_complex_threads", "1", "-filter_complex", strings.Join(filters, ";"), "-map", "[out]")
	}
	tmp := filepath.Join(dir, "mix.pending.m4a")
	args = append(args, "-t", fmtInt64(duration), "-threads", "1", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", tmp)
	if e := runCommand(ctx, c.FFmpeg, args...); e != nil {
		return e
	}
	f, e := os.OpenFile(tmp, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	h := sha256.New()
	n, e := io.Copy(h, f)
	f.Close()
	if e != nil {
		return e
	}
	if e = os.Chmod(tmp, 0600); e != nil {
		return e
	}
	if e = os.Rename(tmp, filepath.Join(dir, "mix.m4a")); e != nil {
		return e
	}
	m.MixBytes = n
	m.MixHash = hex.EncodeToString(h.Sum(nil))
	return nil
}
func validSourcePath(s string) bool {
	return filepath.Dir(s) == "sources" && strings.HasPrefix(filepath.Base(s), "source-") && filepath.Ext(s) == ".webm"
}
func directoryBytes(dir string) (int64, error) {
	var size int64
	e := filepath.WalkDir(dir, func(_ string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type().IsRegular() {
			st, e := d.Info()
			if e != nil {
				return e
			}
			size += st.Size()
		}
		return nil
	})
	return size, e
}
func fmtInt64(n int64) string { return fmt.Sprintf("%d", n) }
func runCommand(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "OMP_NUM_THREADS=2", "OPENBLAS_NUM_THREADS=1")
	cmd.WaitDelay = 3 * time.Second
	return cmd.Run()
}

func publishCapture(g *guard, t Task, m Manifest) error {
	names := []string{"mix.m4a", "manifest.json"}
	if t.Run.MinutesRequested {
		for _, src := range m.Sources {
			if src.Packets > 0 {
				names = append(names, src.File)
			}
		}
	}
	for _, name := range names {
		if e := g.client.Upload(g.ctx, t, filepath.Join(taskDir(t), name)); e != nil {
			return e
		}
	}
	if e := g.result(Result{Phase: "saved", Manifest: &m}); e != nil {
		return e
	}
	return os.RemoveAll(taskDir(t))
}
