package core

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Opt in with CHECKPOINT_M6C_INTEGRATION=1 and the isolated LiveKit fixture.
func TestM6CRecordProcessAndDeleteIntegration(t *testing.T) {
	if os.Getenv("CHECKPOINT_M6C_INTEGRATION") == "" {
		t.Skip("isolated LiveKit/FFmpeg required")
	}
	s, owner, room, host, c := recordingSetup(t)
	s.cfg.APIKey = "m6c-test-key"
	s.cfg.APISecret = "this-is-an-isolated-test-key-not-for-production-123456789"
	s.cfg.LiveKitURL = "http://127.0.0.1:19880"
	s.rooms = lksdk.NewRoomServiceClient(s.cfg.LiveKitURL, s.cfg.APIKey, s.cfg.APISecret)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transcribe" {
			_ = json.NewEncoder(w).Encode(map[string]any{"language": "english", "segments": []any{map[string]any{"start": 0.1, "end": 1.2, "text": "Release the test on Friday."}}})
			return
		}
		var req struct{ Messages []struct{ Content string } }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid generation input")
		}
		body := `{"summary":[{"text":"Release the test on Friday.","sources":["S000001"]}],"context":[],"discussion":[],"decisions":[],"actions":[],"questions":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": body}, "finish_reason": "stop"}}})
	}))
	defer provider.Close()
	c.Transcription.Endpoint = provider.URL + "/transcribe"
	c.Generator.Endpoint = provider.URL + "/generate"
	c.Language = "en"
	if os.Getenv("CHECKPOINT_M6C_LANGUAGE") == "ru" {
		c.Language = "ru"
	}
	c.Socket = filepath.Join(t.TempDir(), "core.sock")
	c.EmptySeconds = 5
	c.MinFreeBytes = 64 << 20
	if os.Getenv("CHECKPOINT_M6C_REAL") == "1" {
		c.Transcription.Adapter = "faster-whisper"
		c.Transcription.Python = "/work/.dev/m6c/venv/bin/python"
		c.Transcription.Script = "/work/scripts/transcribe.py"
		c.Transcription.Model = "/work/.dev/m6c/runtime/models/whisper"
		c.Transcription.Revision = "08e178d48790749d25932bbc082711ddcfdfbc4f"
		c.Generator.Endpoint = "http://127.0.0.1:18089/v1/chat/completions"
		c.Generator.Model = "Qwen3-4B-Q4_K_M"
		c.Generator.Revision = "bc640142c66e1fdd12af0bd68f40445458f3869b"
	}
	b, _ := json.Marshal(c)
	if e := os.WriteFile(s.cfg.MinutesConfig, b, 0600); e != nil {
		t.Fatal(e)
	}
	listener, e := net.Listen("unix", c.Socket)
	if e != nil {
		t.Fatal(e)
	}
	mediaHandler := s.MediaHandler(strings.Repeat("r", 64), strings.Repeat("p", 64))
	workerHTTP := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if evidence := os.Getenv("CHECKPOINT_M6C_EVIDENCE"); evidence != "" && r.Method == "PUT" {
			if e := os.MkdirAll(evidence, 0700); e != nil {
				t.Error(e)
			}
			f, e := os.OpenFile(filepath.Join(evidence, filepath.Base(r.URL.Path)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if e == nil {
				defer f.Close()
				r.Body = struct {
					io.Reader
					io.Closer
				}{io.TeeReader(r.Body, f), r.Body}
			}
		}
		mediaHandler.ServeHTTP(w, r)
	})}
	go workerHTTP.Serve(listener)
	defer workerHTTP.Close()
	t.Setenv("CHECKPOINT_RECORDER_STORAGE", t.TempDir())
	t.Setenv("CHECKPOINT_PROCESSOR_STORAGE", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for _, kind := range []string{"recorder", "processor"} {
		workers.Add(1)
		go func(kind string) {
			defer workers.Done()
			token := strings.Repeat("r", 64)
			if kind == "processor" {
				token = strings.Repeat("p", 64)
			}
			if e := minutes.RunWorker(ctx, kind, s.cfg.MinutesConfig, token); e != nil {
				t.Error(e)
			}
		}(kind)
	}
	go s.MaintainMedia(ctx)
	go s.Reconcile(ctx)
	token, e := s.token(host, true)
	if e != nil {
		t.Fatal(e)
	}
	publisher, e := lksdk.ConnectToRoomWithToken(s.cfg.LiveKitURL, token, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer publisher.Disconnect()
	// Poll explicit state instead of assuming a sleep means a worker is ready.
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		var n int
		_ = s.store.db.QueryRow("SELECT count(*) FROM media_workers WHERE ready=1 AND seen>unixepoch()-10").Scan(&n)
		if n == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	run := startTestRecording(t, s, host, c, true)
	wait := func(condition func(minutes.Run) bool, limit time.Duration) minutes.Run {
		t.Helper()
		end := time.Now().Add(limit)
		for time.Now().Before(end) {
			r, _, err := scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", run.ID))
			if err != nil {
				t.Fatal(err)
			}
			if condition(r) {
				return r
			}
			if r.State == "failed" || r.Processing == "failed" {
				t.Fatalf("recording failed: %+v", r)
			}
			time.Sleep(200 * time.Millisecond)
		}
		r, _, _ := scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", run.ID))
		t.Fatalf("recording timed out: %+v", r)
		return r
	}
	wait(func(r minutes.Run) bool { return r.State == "recording" }, 15*time.Second)
	audio := filepath.Join(t.TempDir(), "voice.ogg")
	wav := filepath.Join(t.TempDir(), "voice.wav")
	if output, e := exec.Command("espeak-ng", "-v", "en-us", "-s", "145", "-w", wav, "We decided to release the test on Friday. Alice will verify the microphone. The board is not required.").CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	if output, e := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", wav, "-c:a", "libopus", audio).CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	second, _, e := s.store.JoinRoomID(room.ID, host.Name)
	if e != nil {
		t.Fatal(e)
	}
	secondToken, e := s.token(second, true)
	if e != nil {
		t.Fatal(e)
	}
	other, e := lksdk.ConnectToRoomWithToken(s.cfg.LiveKitURL, secondToken, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Disconnect()
	russian := filepath.Join(t.TempDir(), "russian.ogg")
	russianWav := filepath.Join(t.TempDir(), "russian.wav")
	if output, e := exec.Command("espeak-ng", "-v", "ru", "-s", "140", "-w", russianWav, "Мы решили выпустить обновление в пятницу. Денис проверит работу микрофона. Доска пока не нужна.").CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	if output, e := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", russianWav, "-c:a", "libopus", russian).CombinedOutput(); e != nil {
		t.Fatal(e, string(output))
	}
	russianDone := make(chan struct{})
	russianTrack, e := lksdk.NewLocalFileTrack(russian, lksdk.ReaderTrackWithOnWriteComplete(func() { close(russianDone) }))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.LocalParticipant.PublishTrack(russianTrack, &lksdk.TrackPublicationOptions{Source: livekit.TrackSource_MICROPHONE, Name: "Russian voice"}); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	track, e := lksdk.NewLocalFileTrack(audio, lksdk.ReaderTrackWithOnWriteComplete(func() { close(done) }))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = publisher.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Source: livekit.TrackSource_MICROPHONE, Name: "Test voice"}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("audio sender timed out")
	}
	select {
	case <-russianDone:
	case <-time.After(30 * time.Second):
		t.Fatal("Russian audio sender timed out")
	}
	time.Sleep(time.Second)
	if e = s.store.RecordingControl(room.ID, run.ID, host.Identity, "stop", 0, false); e != nil {
		t.Fatal(e)
	}
	saved := wait(func(r minutes.Run) bool { return r.Processing == "ready" }, 10*time.Minute)
	detail, e := s.store.recordingDetail(room.ID, run.ID)
	if e != nil || len(detail.Transcript) == 0 || len(detail.Revisions) != 1 || saved.MixBytes <= 0 {
		t.Fatal("incomplete history", detail, e)
	}
	if !strings.Contains(detail.Revisions[0].Markdown, "S000001") {
		t.Fatal("minutes lack evidence")
	}
	sources := map[string]bool{}
	for _, segment := range detail.Transcript {
		sources[segment.Source] = true
	}
	if len(sources) != 2 {
		t.Fatalf("two microphones merged: %+v", detail.Transcript)
	}
	s.cleanupMedia(c)
	if _, e = os.Stat(filepath.Join(c.Storage, run.ID, "sources")); !os.IsNotExist(e) {
		t.Fatal("individual tracks not removed", e)
	}
	if e = s.store.DeleteRoom(owner.ID, room.ID, true, c.Storage); e != nil {
		t.Fatal(e)
	}
	s.cleanupMedia(c)
	if _, e = os.Stat(filepath.Join(c.Storage, run.ID)); !os.IsNotExist(e) {
		t.Fatal("room content survived delete", e)
	}
	t.Logf("real RTP -> recording %d bytes -> %d transcript segments -> minutes -> cleanup -> room deletion passed", saved.MixBytes, len(detail.Transcript))
}
