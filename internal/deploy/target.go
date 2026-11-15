package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ilguzin/checkpoint/internal/minutes"
	"github.com/ilguzin/checkpoint/internal/release"
	"gopkg.in/yaml.v3"
)

type SSH struct {
	User           string `yaml:"user"`
	Port           int    `yaml:"port"`
	Auth           string `yaml:"auth"`
	IdentityFile   string `yaml:"identityFile"`
	KnownHostsFile string `yaml:"knownHostsFile"`
}
type Node struct {
	Name     string   `yaml:"name"`
	Address  string   `yaml:"address"`
	Roles    []string `yaml:"roles"`
	Platform struct {
		OS           string `yaml:"os"`
		Version      string `yaml:"version"`
		Architecture string `yaml:"architecture"`
	} `yaml:"platform"`
	SSH SSH `yaml:"ssh"`
}
type Target struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Topology string          `yaml:"topology"`
		Minutes  *minutes.Config `yaml:"minutes,omitempty"`
		Nodes    []Node          `yaml:"nodes"`
		Release  struct {
			Repository      string `yaml:"repository"`
			Version         string `yaml:"version"`
			VerifySignature bool   `yaml:"verifySignature"`
		} `yaml:"release"`
		Domains struct {
			App       string `yaml:"app"`
			Signaling string `yaml:"signaling"`
			Turn      string `yaml:"turn"`
			Observer  string `yaml:"observer"`
		} `yaml:"domains"`
		TLS struct {
			Provider string `yaml:"provider"`
			Email    string `yaml:"email"`
			NoEmail  bool   `yaml:"noEmail"`
		} `yaml:"tls"`
		Security struct {
			SSH struct {
				PasswordAuthentication bool     `yaml:"passwordAuthentication"`
				RootLogin              string   `yaml:"rootLogin"`
				AllowedCIDRs           []string `yaml:"allowedCIDRs"`
			} `yaml:"ssh"`
			Firewall struct {
				Provider     string `yaml:"provider"`
				Enabled      bool   `yaml:"enabled"`
				ManageIPv6   bool   `yaml:"manageIPv6"`
				AllowTurnUDP bool   `yaml:"allowTurnUDP"`
			} `yaml:"firewall"`
			Services struct {
				Unprivileged bool `yaml:"unprivileged"`
			} `yaml:"services"`
			OSUpdates struct {
				SecurityUpdates bool `yaml:"securityUpdates"`
				AutomaticReboot bool `yaml:"automaticReboot"`
			} `yaml:"osUpdates"`
		} `yaml:"security"`
		Backup struct {
			Directory     string `yaml:"directory"`
			RetentionDays int    `yaml:"retentionDays"`
		} `yaml:"backup"`
	} `yaml:"spec"`
}

func Parse(body []byte) (Target, error) {
	var t Target
	if len(body) > 65536 {
		return t, errors.New("target too large")
	}
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if e := dec.Decode(&t); e != nil {
		return t, e
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return t, errors.New("one YAML document required")
	}
	return t, t.Validate()
}
func (t Target) Validate() error {
	if t.Spec.Minutes != nil {
		if err := t.Spec.Minutes.Validate(); err != nil {
			return err
		}
	}
	if t.APIVersion != "checkpoint/v1alpha1" || t.Kind != "DeploymentTarget" || !regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`).MatchString(t.Metadata.Name) || t.Spec.Topology != "single-node" || len(t.Spec.Nodes) != 1 {
		return errors.New("versioned single-node target with one named VPS required")
	}
	n := t.Spec.Nodes[0]
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`).MatchString(n.Name) {
		return errors.New("valid node name required")
	}
	ip := net.ParseIP(n.Address)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || n.Platform.OS != "ubuntu" || n.Platform.Version != "24.04" || n.Platform.Architecture != "amd64" {
		return errors.New("target requires public IPv4 and Ubuntu 24.04 amd64")
	}
	roles := map[string]bool{}
	for _, r := range n.Roles {
		if roles[r] {
			return errors.New("duplicate role")
		}
		roles[r] = true
	}
	for _, r := range []string{"core", "signal", "livekit", "observer", "edge"} {
		if !roles[r] {
			return errors.New("all five roles required on the single node")
		}
	}
	if len(roles) != 5 {
		return errors.New("unknown role")
	}
	if n.SSH.User != "root" || n.SSH.Port < 1 || n.SSH.Port > 65535 || n.SSH.KnownHostsFile == "" || (n.SSH.Auth != "agent" && n.SSH.Auth != "identity-file") || n.SSH.Auth == "identity-file" && n.SSH.IdentityFile == "" {
		return errors.New("root key authentication and trusted known_hosts required")
	}
	if t.Spec.Release.Repository != release.Repository || !release.Version.MatchString(t.Spec.Release.Version) || !t.Spec.Release.VerifySignature {
		return errors.New("pinned signed Checkpoint release required")
	}
	seen := map[string]bool{}
	for _, d := range []string{t.Spec.Domains.App, t.Spec.Domains.Signaling, t.Spec.Domains.Turn, t.Spec.Domains.Observer} {
		if len(d) > 253 || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`).MatchString(d) || seen[d] {
			return errors.New("four distinct ASCII DNS names required")
		}
		seen[d] = true
	}
	if t.Spec.TLS.Provider != "certbot" || (t.Spec.TLS.NoEmail && t.Spec.TLS.Email != "") || (!t.Spec.TLS.NoEmail && !regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`).MatchString(t.Spec.TLS.Email)) {
		return errors.New("Certbot requires either a valid email or noEmail: true")
	}
	s := t.Spec.Security
	if s.SSH.PasswordAuthentication || s.SSH.RootLogin != "prohibit-password" || len(s.SSH.AllowedCIDRs) == 0 || s.Firewall.Provider != "ufw" || !s.Firewall.Enabled || !s.Firewall.ManageIPv6 || s.Firewall.AllowTurnUDP || !s.Services.Unprivileged || !s.OSUpdates.SecurityUpdates || s.OSUpdates.AutomaticReboot {
		return errors.New("unsupported security policy; secure defaults and no automatic reboot required")
	}
	for _, cidr := range s.SSH.AllowedCIDRs {
		if ip, network, e := net.ParseCIDR(cidr); e != nil {
			return fmt.Errorf("invalid allowed SSH CIDR: %w", e)
		} else if !ip.Equal(network.IP) {
			return errors.New("SSH CIDR must use its network address")
		}
	}
	if t.Spec.Backup.Directory != "/var/backups/checkpoint" || t.Spec.Backup.RetentionDays < 1 || t.Spec.Backup.RetentionDays > 30 {
		return errors.New("managed backup directory and retention 1–30 days required")
	}
	return nil
}
func expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
func SSHArgs(n Node) []string {
	args := []string{"-p", fmt.Sprint(n.SSH.Port), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + expand(n.SSH.KnownHostsFile), "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ConnectTimeout=10"}
	if n.SSH.Auth == "identity-file" {
		args = append(args, "-o", "IdentitiesOnly=yes", "-i", expand(n.SSH.IdentityFile))
	}
	return append(args, "root@"+n.Address)
}
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (t Target) Plan() map[string]any {
	return map[string]any{"target": t.Metadata.Name, "node": t.Spec.Nodes[0].Address, "release": t.Spec.Release.Version, "changes": []string{"Verify signed release and strict SSH host key", "Native setup: users, systemd, TLS, TURN, firewall, Observer", "Restrict SSH to operator CIDRs and keys, with timed rollback and second-connection verification", "Configure security updates without reboot and daily private backups"}, "externalActions": []string{"Publish all four DNS A records; remove AAAA/proxying", "Allow provider firewall TCP SSH/80/443/7881 and UDP 7882", "Verify host key out of band and populate known_hosts", "Verify public media, ACME renewal and reboot after deployment"}}
}
