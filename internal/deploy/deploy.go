package deploy

import (
	"bytes"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ilguzin/checkpoint/internal/core"
	"github.com/ilguzin/checkpoint/internal/release"
)

//go:embed harden.py
var harden []byte

func remote(n Node, command string, input io.Reader, output io.Writer) error {
	args := append(SSHArgs(n), command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = input
	cmd.Stdout = output
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func checkLocal(n Node) error {
	for _, path := range []string{n.SSH.KnownHostsFile} {
		info, e := os.Stat(expand(path))
		if e != nil || !info.Mode().IsRegular() {
			return errors.New("trusted known_hosts file is missing")
		}
	}
	if n.SSH.Auth == "identity-file" {
		info, e := os.Stat(expand(n.SSH.IdentityFile))
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("SSH private key must be a private regular file")
		}
	}
	return nil
}
func Recover(t Target) error {
	n := t.Spec.Nodes[0]
	if e := checkLocal(n); e != nil {
		return e
	}
	return remote(n, "python3 /var/lib/checkpoint-deploy/harden.py rollback", nil, os.Stdout)
}
func Apply(t Target, public string) error {
	n := t.Spec.Nodes[0]
	if e := checkLocal(n); e != nil {
		return e
	}
	if public == "" {
		return errors.New("deploy requires a release build with a pinned public key")
	}
	base := "https://github.com/" + release.Repository + "/releases/download/" + t.Spec.Release.Version
	raw, e := release.Fetch(nil, base+"/release.json", 1<<20)
	if e != nil {
		return e
	}
	signature, e := release.Fetch(nil, base+"/release.json.minisig", 8192)
	if e != nil {
		return e
	}
	manifest, e := release.Verify(raw, signature, public, release.State{})
	if e != nil {
		return e
	}
	if manifest.LiveKitVersion != "1.13.11" || manifest.Schema.Target != core.SchemaVersion {
		return errors.New("unsupported media/schema version for this deployment tool")
	}
	if manifest.Version != t.Spec.Release.Version {
		return errors.New("target release/version mismatch")
	}
	var asset release.Asset
	for _, a := range manifest.Assets {
		if a.Kind == "checkpoint" {
			asset = a
		}
	}
	bundle, e := release.FetchAsset(nil, base, asset)
	if e != nil {
		return e
	}
	_, files, e := release.Unpack(bundle, manifest.Version)
	if e != nil {
		return e
	}
	setup, ok := files["setup.sh"]
	if !ok {
		return errors.New("signed setup script missing")
	}
	// Verify the existing target before copying or running any payload.
	var preflight bytes.Buffer
	if e = remote(n, "test \"$(id -u)\" = 0 && test \"$(uname -m)\" = x86_64 && command -v python3 && command -v systemctl", nil, &preflight); e != nil {
		return errors.New("target root/platform/systemd preflight failed")
	}
	nonce := make([]byte, 16)
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	directory := "/tmp/checkpoint-deploy-" + hex.EncodeToString(nonce)
	if e = remote(n, "umask 077; mkdir "+Quote(directory), nil, os.Stdout); e != nil {
		return e
	}
	defer remote(n, "rm -rf -- "+Quote(directory), nil, io.Discard)
	for name, data := range map[string][]byte{"bundle.tar.gz": bundle, "setup.sh": setup, "harden.py": harden} {
		if e = remote(n, "umask 077; cat > "+Quote(directory+"/"+name), bytes.NewReader(data), os.Stdout); e != nil {
			return e
		}
	}
	args := []string{"bash", directory + "/setup.sh", "--bundle", directory + "/bundle.tar.gz", "--app-domain", t.Spec.Domains.App, "--rtc-domain", t.Spec.Domains.Signaling, "--turn-domain", t.Spec.Domains.Turn, "--observer-domain", t.Spec.Domains.Observer, "--public-ip", n.Address, "--ssh-port", fmt.Sprint(n.SSH.Port)}
	if t.Spec.TLS.NoEmail {
		args = append(args, "--no-email")
	} else {
		args = append(args, "--email", t.Spec.TLS.Email)
	}
	quoted := []string{}
	for _, arg := range args {
		quoted = append(quoted, Quote(arg))
	}
	command := strings.Join(quoted, " ")
	if e = remote(n, command, nil, os.Stdout); e != nil {
		return fmt.Errorf("read-only setup preflight failed: %w", e)
	}
	if e = remote(n, command+" --apply", nil, os.Stdout); e != nil {
		return fmt.Errorf("native setup incomplete; reapply the same target after resolving the reported issue: %w", e)
	}
	var embedded bytes.Buffer
	if e = remote(n, "/usr/local/bin/checkpoint trust-key", nil, &embedded); e != nil {
		return e
	}
	if strings.TrimSpace(embedded.String()) != strings.TrimSpace(public) {
		return errors.New("installed binary trust key differs from deploy key")
	}
	state, _ := json.Marshal(release.State{Sequence: manifest.Sequence, Digest: release.Hash(raw), Version: manifest.Version})
	if e = remote(n, "umask 077; cat > /etc/checkpoint/update-state.json", bytes.NewReader(state), os.Stdout); e != nil {
		return e
	}
	policy, _ := json.Marshal(map[string]any{"allowedCIDRs": t.Spec.Security.SSH.AllowedCIDRs, "sshPort": n.SSH.Port, "retentionDays": t.Spec.Backup.RetentionDays})
	if e = remote(n, "python3 "+Quote(directory+"/harden.py")+" stage", bytes.NewReader(policy), os.Stdout); e != nil {
		return fmt.Errorf("security configuration failed; timed rollback/recover protects SSH: %w", e)
	}
	// A distinct SSH process, still with BatchMode and strict trusted host key.
	if e = remote(n, "true", nil, io.Discard); e != nil {
		return errors.New("second SSH connection failed; automatic security rollback is scheduled")
	}
	if e = remote(n, "python3 "+Quote(directory+"/harden.py")+" commit", nil, os.Stdout); e != nil {
		return e
	}
	if e = remote(n, "CHECKPOINT_DATABASE=/var/lib/checkpoint/checkpoint.db checkpoint doctor", nil, os.Stdout); e != nil {
		return e
	}
	if t.Spec.Minutes != nil {
		config, e := json.Marshal(t.Spec.Minutes)
		if e != nil {
			return e
		}
		path := directory + "/minutes.yaml"
		if e = remote(n, "umask 077; cat > "+Quote(path), bytes.NewReader(config), os.Stdout); e != nil {
			return e
		}
		if t.Spec.Minutes.Enabled {
			e = remote(n, "python3 /opt/checkpoint/current/scripts/minutes-setup.py --config "+Quote(path), nil, os.Stdout)
		} else {
			e = remote(n, "checkpoint minutes config apply --file "+Quote(path), nil, os.Stdout)
		}
		if e != nil {
			return fmt.Errorf("optional minutes setup incomplete: %w", e)
		}
	}
	fmt.Println("Native deployment and key-only SSH verified. Provider firewall, public media, renewal and reboot still require external acceptance.")
	return nil
}
