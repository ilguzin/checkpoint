// Package minutes contains the recording/processing wire contracts. It has no
// access to the account database and is shared by core and isolated workers.
package minutes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Enabled                 bool                `json:"enabled" yaml:"enabled"`
	Storage                 string              `json:"storage" yaml:"storage"`
	Socket                  string              `json:"socket" yaml:"socket"`
	FFmpeg                  string              `json:"ffmpeg" yaml:"ffmpeg"`
	ConfirmationSeconds     int                 `json:"confirmationSeconds" yaml:"confirmationSeconds"`
	WarningSeconds          int                 `json:"warningSeconds" yaml:"warningSeconds"`
	EmptySeconds            int                 `json:"emptySeconds" yaml:"emptySeconds"`
	MaxRunSeconds           int                 `json:"maxRunSeconds" yaml:"maxRunSeconds"`
	MinFreeBytes            uint64              `json:"minFreeBytes" yaml:"minFreeBytes"`
	MaxStorageBytes         int64               `json:"maxStorageBytes" yaml:"maxStorageBytes"`
	MaxSources              int                 `json:"maxSources" yaml:"maxSources"`
	MaxConcurrentRecordings int                 `json:"maxConcurrentRecordings" yaml:"maxConcurrentRecordings"`
	FailedAudioTTLSeconds   int                 `json:"failedAudioTTLSeconds" yaml:"failedAudioTTLSeconds"`
	JobTimeoutSeconds       int                 `json:"jobTimeoutSeconds" yaml:"jobTimeoutSeconds"`
	MaxAttempts             int                 `json:"maxAttempts" yaml:"maxAttempts"`
	Transcription           TranscriptionConfig `json:"transcription" yaml:"transcription"`
	Generator               GeneratorConfig     `json:"generator" yaml:"generator"`
	Language                string              `json:"language" yaml:"language"`
}
type TranscriptionConfig struct {
	Adapter        string `json:"adapter" yaml:"adapter"`
	Python         string `json:"python" yaml:"python"`
	Script         string `json:"script" yaml:"script"`
	Model          string `json:"model" yaml:"model"`
	Revision       string `json:"revision" yaml:"revision"`
	Language       string `json:"language" yaml:"language"`
	Threads        int    `json:"threads" yaml:"threads"`
	Endpoint       string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	CredentialFile string `json:"credentialFile,omitempty" yaml:"credentialFile,omitempty"`
}
type GeneratorConfig struct {
	Adapter        string `json:"adapter" yaml:"adapter"`
	Endpoint       string `json:"endpoint" yaml:"endpoint"`
	Model          string `json:"model" yaml:"model"`
	Revision       string `json:"revision" yaml:"revision"`
	CredentialFile string `json:"credentialFile,omitempty" yaml:"credentialFile,omitempty"`
}

func DefaultConfig() Config {
	return Config{Storage: "/var/lib/checkpoint/recordings", Socket: "/run/checkpoint-media/core.sock", FFmpeg: "/usr/bin/ffmpeg", ConfirmationSeconds: 7200, WarningSeconds: 300, EmptySeconds: 60, MinFreeBytes: 2 << 30, MaxStorageBytes: 20 << 30, MaxSources: 32, MaxConcurrentRecordings: 2, FailedAudioTTLSeconds: 7 * 86400, JobTimeoutSeconds: 4 * 3600, MaxAttempts: 3, Language: "ru", Transcription: TranscriptionConfig{Adapter: "faster-whisper", Python: "/opt/checkpoint-minutes/venv/bin/python", Script: "/opt/checkpoint/current/scripts/transcribe.py", Model: "/opt/checkpoint-minutes/models/whisper", Revision: "08e178d48790749d25932bbc082711ddcfdfbc4f", Language: "auto", Threads: 2}, Generator: GeneratorConfig{Adapter: "local", Endpoint: "http://127.0.0.1:18089/v1/chat/completions", Model: "Qwen3-4B-Q4_K_M", Revision: "bc640142c66e1fdd12af0bd68f40445458f3869b"}}
}
func ParseConfig(body []byte) (Config, error) {
	c := DefaultConfig()
	d := yaml.NewDecoder(bytes.NewReader(body))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("one valid configuration document required")
	}
	return c, c.Validate()
}
func LoadConfig(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return DefaultConfig(), nil
	}
	if e != nil {
		return Config{}, e
	}
	return ParseConfig(b)
}
func (c Config) Validate() error {
	for _, p := range []string{c.Storage, c.Socket, c.FFmpeg, c.Transcription.Python, c.Transcription.Script} {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return errors.New("absolute dedicated paths required")
		}
	}
	if c.ConfirmationSeconds < 60 || c.ConfirmationSeconds > 86400 || c.WarningSeconds < 1 || c.WarningSeconds >= c.ConfirmationSeconds || c.EmptySeconds < 5 || c.EmptySeconds > 3600 || c.MaxRunSeconds < 0 || c.MaxSources < 1 || c.MaxSources > 100 || c.MaxConcurrentRecordings < 1 || c.MaxConcurrentRecordings > 8 || c.MinFreeBytes < 64<<20 || c.MaxStorageBytes < 128<<20 || c.FailedAudioTTLSeconds < 60 || c.JobTimeoutSeconds < 30 || c.JobTimeoutSeconds > 86400 || c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("invalid recording limits")
	}
	if c.Language != "ru" && c.Language != "en" {
		return errors.New("minutes language must be ru or en")
	}
	if c.Transcription.Language != "auto" && c.Transcription.Language != "ru" && c.Transcription.Language != "en" {
		return errors.New("transcription language must be auto, ru or en")
	}
	if c.Transcription.Threads < 1 || c.Transcription.Threads > 8 {
		return errors.New("ASR threads must be 1–8")
	}
	if c.Transcription.Adapter != "faster-whisper" && c.Transcription.Adapter != "api" {
		return errors.New("unsupported transcriber")
	}
	if c.Transcription.Adapter == "api" {
		if e := validateEndpoint(c.Transcription.Endpoint, false); e != nil {
			return e
		}
	}
	if c.Generator.Adapter != "local" && c.Generator.Adapter != "api" {
		return errors.New("unsupported generator")
	}
	if e := validateEndpoint(c.Generator.Endpoint, c.Generator.Adapter == "local"); e != nil {
		return e
	}
	for _, p := range []string{c.Transcription.CredentialFile, c.Generator.CredentialFile} {
		if p != "" && !filepath.IsAbs(p) {
			return errors.New("credential file must be absolute")
		}
	}
	return nil
}
func validateEndpoint(raw string, local bool) error {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid provider endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	loop := ip != nil && ip.IsLoopback()
	if local && !loop {
		return errors.New("local provider requires a loopback IP")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return errors.New("provider requires HTTPS (loopback HTTP allowed)")
	}
	return nil
}
func AtomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return AtomicWrite(path, b, 0600)
}
func AtomicWrite(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if os.Geteuid() == 0 {
		if info, err := os.Stat(path); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				if err = f.Chown(int(st.Uid), int(st.Gid)); err != nil {
					f.Close()
					return err
				}
			}
		}
	}
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func Credential(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	st, e := os.Lstat(path)
	if e != nil {
		return "", e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0007 != 0 || st.Size() > 16384 {
		return "", errors.New("credential must be private regular file")
	}
	b, e := os.ReadFile(path)
	return string(bytes.TrimSpace(b)), e
}
func (c Config) CheckModels() error {
	if c.Transcription.Adapter == "faster-whisper" {
		for _, p := range []string{c.Transcription.Python, c.Transcription.Script, c.Transcription.Model} {
			if _, e := os.Stat(p); e != nil {
				return fmt.Errorf("ASR not installed: %s", p)
			}
		}
	}
	if c.Transcription.Revision == "" || c.Generator.Revision == "" {
		return errors.New("model revisions must be configured before requesting minutes")
	}
	return nil
}

type Segment struct {
	ID                  string  `json:"id"`
	Source              string  `json:"source"`
	Speaker             string  `json:"speaker"`
	StartMS             int64   `json:"startMs"`
	EndMS               int64   `json:"endMs"`
	Text                string  `json:"text"`
	Language            string  `json:"language"`
	LanguageProbability float64 `json:"languageProbability,omitempty"`
}
type Source struct {
	SHA256   string `json:"sha256,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	ID       string `json:"id"`
	Identity string `json:"identity"`
	UserID   string `json:"userId,omitempty"`
	Name     string `json:"name"`
	Track    string `json:"track"`
	File     string `json:"file"`
	Packets  int64  `json:"packets"`
	Dropped  int64  `json:"dropped"`
	StartMS  int64  `json:"startMs"`
	EndMS    int64  `json:"endMs"`
}
type Manifest struct {
	Run      string   `json:"run"`
	Started  int64    `json:"started"`
	Ended    int64    `json:"ended"`
	Sources  []Source `json:"sources"`
	MixHash  string   `json:"mixHash"`
	MixBytes int64    `json:"mixBytes"`
	Partial  bool     `json:"partial"`
	Reason   string   `json:"reason"`
}
type Run struct {
	ID               string     `json:"id"`
	Room             string     `json:"room"`
	Occurrence       string     `json:"occurrence"`
	State            string     `json:"state"`
	Processing       string     `json:"processing"`
	MinutesRequested bool       `json:"minutesRequested"`
	Started          int64      `json:"started"`
	Ended            int64      `json:"ended"`
	Deadline         int64      `json:"deadline"`
	Window           int        `json:"window"`
	PendingExtension bool       `json:"pendingExtension"`
	WarningSeconds   int        `json:"warningSeconds"`
	StopReason       string     `json:"stopReason"`
	MixBytes         int64      `json:"mixBytes"`
	Partial          bool       `json:"partial"`
	Transcript       []Segment  `json:"transcript,omitempty"`
	Revisions        []Revision `json:"revisions,omitempty"`
}
type Revision struct {
	ID       string `json:"id"`
	Markdown string `json:"markdown"`
	Reviewed bool   `json:"reviewed"`
	Created  int64  `json:"created"`
	Author   string `json:"author"`
}
type Task struct {
	Kind       string    `json:"kind"`
	Run        Run       `json:"run"`
	Lease      string    `json:"lease"`
	Config     Config    `json:"config"`
	MediaRoom  string    `json:"mediaRoom"`
	URL        string    `json:"url"`
	Token      string    `json:"token"`
	Transcript []Segment `json:"transcript,omitempty"`
	Manifest   Manifest  `json:"manifest"`
	Title      string    `json:"title"`
}
type Heartbeat struct {
	Run           string `json:"run"`
	Lease         string `json:"lease"`
	Phase         string `json:"phase"`
	AppliedWindow int    `json:"appliedWindow"`
	Started       int64  `json:"started,omitempty"`
	Reason        string `json:"reason,omitempty"`
}
type Directive struct {
	Stop     bool              `json:"stop"`
	Cancel   bool              `json:"cancel"`
	Deadline int64             `json:"deadline"`
	Window   int               `json:"window"`
	Sources  map[string]Source `json:"sources,omitempty"`
}
type Result struct {
	Run        string    `json:"run"`
	Lease      string    `json:"lease"`
	Phase      string    `json:"phase"`
	Manifest   *Manifest `json:"manifest,omitempty"`
	Transcript []Segment `json:"transcript,omitempty"`
	Markdown   string    `json:"markdown,omitempty"`
	Error      string    `json:"error,omitempty"`
}

const LeaseDuration = 30 * time.Second

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type plain Config
	b, e := yaml.Marshal(node)
	if e != nil {
		return e
	}
	value := plain(DefaultConfig())
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if e = d.Decode(&value); e != nil {
		return e
	}
	*c = Config(value)
	return c.Validate()
}
