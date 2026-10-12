//go:build unix

package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancellationAllowsOwnedCleanup(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, dir, []string{"sh", "-c", "trap 'echo retained > receipt; exit 0' TERM; echo ready > ready; while :; do sleep 1; done"}, nil, 1024)
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("child did not start")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "receipt")); err != nil {
		t.Fatalf("owner could not retain evidence: %v", err)
	}
}
