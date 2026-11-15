package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aead.dev/minisign"
)

const Repository = "ilguzin/checkpoint"
const MaxAsset = 300 << 20

var Version = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Schema struct {
	Min    int `json:"min"`
	Max    int `json:"max"`
	Target int `json:"target"`
}
type Asset struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind"`
}
type Manifest struct {
	Format         int     `json:"format"`
	Project        string  `json:"project"`
	Repository     string  `json:"repository"`
	Channel        string  `json:"channel"`
	Sequence       uint64  `json:"sequence"`
	Version        string  `json:"version"`
	LiveKitVersion string  `json:"livekitVersion"`
	Schema         Schema  `json:"schema"`
	Assets         []Asset `json:"assets"`
}
type State struct {
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"`
	Version  string `json:"version"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Verify(body, signature []byte, public string, current State) (Manifest, error) {
	var m Manifest
	if len(body) > 1<<20 || len(signature) > 8192 || public == "" {
		return m, errors.New("missing pinned public key or oversized signature/manifest")
	}
	var key minisign.PublicKey
	if e := key.UnmarshalText([]byte(public)); e != nil {
		return m, e
	}
	if !minisign.Verify(key, body, signature) {
		return m, errors.New("release signature rejected")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&m); e != nil {
		return m, e
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return m, errors.New("trailing manifest data")
	}
	if m.Format != 1 || m.Project != "checkpoint" || m.Repository != Repository || m.Channel != "stable" || m.Sequence == 0 || !Version.MatchString(m.Version) || !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(m.LiveKitVersion) {
		return m, errors.New("unsupported release identity or format")
	}
	if m.Sequence < current.Sequence || m.Sequence == current.Sequence && Hash(body) != current.Digest {
		return m, errors.New("release downgrade or reused sequence rejected")
	}
	if current.Version != "" && Version.MatchString(current.Version) && CompareVersion(m.Version, current.Version) < 0 {
		return m, errors.New("semantic version downgrade rejected")
	}
	if m.Schema.Min < 1 || m.Schema.Max < m.Schema.Min || m.Schema.Target < m.Schema.Min || m.Schema.Target > m.Schema.Max {
		return m, errors.New("invalid schema compatibility")
	}
	if len(m.Assets) < 1 || len(m.Assets) > 8 {
		return m, errors.New("invalid asset count")
	}
	seen := map[string]bool{}
	bundles := 0
	for _, a := range m.Assets {
		if !safeName.MatchString(a.Name) || a.Name == "." || a.Name == ".." || seen[a.Name] || a.Size <= 0 || a.Size > MaxAsset || !digest.MatchString(a.SHA256) || a.OS != "linux" || a.Arch != "amd64" || (a.Kind != "checkpoint" && a.Kind != "livekit" && a.Kind != "installer") {
			return m, errors.New("invalid release asset")
		}
		seen[a.Name] = true
		if a.Kind == "checkpoint" {
			bundles++
			if a.Name != "checkpoint-linux-amd64.tar.gz" {
				return m, errors.New("unexpected bundle name")
			}
		}
	}
	if bundles != 1 {
		return m, errors.New("one Checkpoint bundle required")
	}
	return m, nil
}
func Fetch(client *http.Client, url string, limit int64) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	res, e := client.Get(url)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("download status %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("download exceeds limit")
	}
	return b, nil
}
func FetchAsset(client *http.Client, base string, a Asset) ([]byte, error) {
	b, e := Fetch(client, strings.TrimSuffix(base, "/")+"/"+a.Name, a.Size)
	if e != nil {
		return nil, e
	}
	if int64(len(b)) != a.Size || Hash(b) != a.SHA256 {
		return nil, errors.New("asset size/hash mismatch")
	}
	return b, nil
}
func Latest(public string, state State) (Manifest, []byte, error) {
	body, e := Fetch(nil, "https://api.github.com/repos/"+Repository+"/releases/latest", 1<<20)
	if e != nil {
		return Manifest{}, nil, e
	}
	var metadata struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if e = json.Unmarshal(body, &metadata); e != nil {
		return Manifest{}, nil, e
	}
	if metadata.Draft || metadata.Prerelease || !Version.MatchString(metadata.Tag) {
		return Manifest{}, nil, errors.New("stable release not found")
	}
	base := "https://github.com/" + Repository + "/releases/download/" + metadata.Tag
	body, e = Fetch(nil, base+"/release.json", 1<<20)
	if e != nil {
		return Manifest{}, nil, e
	}
	sig, e := Fetch(nil, base+"/release.json.minisig", 8192)
	if e != nil {
		return Manifest{}, nil, e
	}
	m, e := Verify(body, sig, public, state)
	if e == nil && m.Version != metadata.Tag {
		e = errors.New("signed version does not match release tag")
	}
	return m, body, e
}

func CompareVersion(a, b string) int {
	aa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	bb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		if len(aa[i]) < len(bb[i]) {
			return -1
		}
		if len(aa[i]) > len(bb[i]) {
			return 1
		}
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}
