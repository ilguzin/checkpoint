package deploy

import (
	"os"
	"strings"
	"testing"
)

func TestStrictTargetAndSSHPolicy(t *testing.T) {
	b, e := os.ReadFile("../../deploy/targets/vps.example.yaml")
	if e != nil {
		t.Fatal(e)
	}
	target, e := Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	args := strings.Join(SSHArgs(target.Spec.Nodes[0]), " ")
	for _, required := range []string{"StrictHostKeyChecking=yes", "BatchMode=yes", "ForwardAgent=no", "ClearAllForwardings=yes"} {
		if !strings.Contains(args, required) {
			t.Fatal("unsafe SSH options")
		}
	}
	for _, change := range []func(string) string{func(s string) string { return strings.Replace(s, "provider: ufw", "provider: nftables", 1) }, func(s string) string { return strings.Replace(s, "single-node", "cluster", 1) }, func(s string) string { return s + "\nunknownField: value\n" }, func(s string) string { return strings.Replace(s, "verifySignature: true", "verifySignature: false", 1) }, func(s string) string {
		return strings.Replace(s, "passwordAuthentication: false", "passwordAuthentication: true", 1)
	}, func(s string) string { return strings.Replace(s, "automaticReboot: false", "automaticReboot: true", 1) }} {
		if _, e := Parse([]byte(change(string(b)))); e == nil {
			t.Fatal("unsupported target accepted")
		}
	}
	if Quote("a'b;$(x)") != "'a'\\''b;$(x)'" {
		t.Fatal("shell quoting broken")
	}
}

func TestCertbotContactChoice(t *testing.T) {
	body, err := os.ReadFile("../../deploy/targets/vps.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	target, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	target.Spec.TLS.Email = ""
	if target.Validate() == nil {
		t.Fatal("missing contact choice accepted")
	}
	target.Spec.TLS.NoEmail = true
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	target.Spec.TLS.Email = "operator@example.com"
	if target.Validate() == nil {
		t.Fatal("conflicting contact choices accepted")
	}
}
