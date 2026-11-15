package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// systemd Restart=on-failure must see a failed bind as a nonzero exit.
func TestListenerFailure(t *testing.T) {
	if os.Getenv("CHECKPOINT_TEST_CHILD") == "1" {
		os.Args = []string{"checkpoint", "observe"}
		main()
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestListenerFailure$")
	cmd.Env = append(os.Environ(), "CHECKPOINT_TEST_CHILD=1", "LIVEKIT_API_KEY=test-key",
		"LIVEKIT_API_SECRET="+strings.Repeat("a", 32), "CHECKPOINT_OBSERVER_LISTEN="+listener.Addr().String())
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil || err == nil || cmd.ProcessState.ExitCode() != 1 || !strings.Contains(string(output), "address already in use") {
		t.Fatalf("expected failed bind and exit 1, got %v: %s", err, output)
	}
}
