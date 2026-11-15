package minutes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRejectsUnsafeProfiles(t *testing.T) {
	for _, body := range []string{"transcription:\n  language: fr\n", "generator:\n  adapter: local\n  endpoint: https://external.example/v1/chat/completions\n", "confirmationSeconds: 10\n", "unknownField: true\n", "enabled: true\n---\n[invalid"} {
		if _, e := ParseConfig([]byte(body)); e == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	if _, e := ParseConfig([]byte("enabled: false\n")); e != nil {
		t.Fatal(e)
	}
}
func TestGeneratedMinutesRequireExistingEvidence(t *testing.T) {
	segments := []Segment{{ID: "S000001", Source: "mic1", Speaker: "Alice", Text: "Do not ship on Friday", StartMS: 1234, EndMS: 2000, Language: "en"}}
	bad := Draft{Decisions: []Evidence{{Text: "Ship", Sources: []string{"S999999"}}}}
	if validateDraft(bad, segments) == nil {
		t.Fatal("invented source accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 || !strings.Contains(request.Messages[0].Content, "UNTRUSTED") {
			t.Error("missing isolation prompt")
		}
		draft := Draft{Summary: []Evidence{{Text: "Do not ship on Friday", Sources: []string{"S000001"}}}}
		body, _ := json.Marshal(draft)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(body)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	c := DefaultConfig()
	c.Generator.Endpoint = server.URL
	c.Language = "en"
	text, e := (ChatGenerator{}).Generate(context.Background(), Task{Config: c, Title: "<script>alert(1)</script>", Run: Run{Started: 1, ID: "test"}}, segments)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(text, "S000001 @ 00:01") || !strings.Contains(text, "AI-generated · Not reviewed") || strings.Contains(text, "<script>") {
		t.Fatal(text)
	}
}
func TestTranscriptValidation(t *testing.T) {
	valid := []Segment{{ID: "S1", Source: "mic", Speaker: "one", Text: "Hello", StartMS: 0, EndMS: 100, Language: "en"}, {ID: "S2", Source: "mic2", Speaker: "two", Text: "Привет", StartMS: 50, EndMS: 150, Language: "ru"}}
	if e := ValidateTranscript(valid, 200); e != nil {
		t.Fatal("overlap rejected", e)
	}
	valid[1].ID = "S1"
	if ValidateTranscript(valid, 200) == nil {
		t.Fatal("duplicate segment accepted")
	}
	valid[1].ID = "S2"
	valid[1].EndMS = 10000
	if ValidateTranscript(valid, 200) == nil {
		t.Fatal("out of range timestamp accepted")
	}
}
func TestMixPreservesLateStartAndSilence(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("FFmpeg integration environment required")
	}
	dir := t.TempDir()
	if e = os.Mkdir(filepath.Join(dir, "sources"), 0700); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(dir, "sources/source-0000.webm")
	if e = runCommand(context.Background(), ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "libopus", file); e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.FFmpeg = ffmpeg
	m := Manifest{Started: 1, Ended: 5, Sources: []Source{{File: "sources/source-0000.webm", Packets: 50, StartMS: 2000}}}
	if e = Mix(context.Background(), c, dir, &m); e != nil {
		t.Fatal(e)
	}
	if m.MixBytes < 1000 || len(m.MixHash) != 64 {
		t.Fatal(m)
	}
	out, e := exec.Command(ffmpeg, "-hide_banner", "-i", filepath.Join(dir, "mix.m4a"), "-af", "silencedetect=noise=-40dB:d=1", "-f", "null", "-").CombinedOutput()
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(out), "silence_start: 0") || !strings.Contains(string(out), "silence_end: 2") {
		t.Fatal("late source timing collapsed", string(out))
	}
}
