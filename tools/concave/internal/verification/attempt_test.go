package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelledAttemptRetainsReceipt(t *testing.T) {
	for _, phase := range []string{"preparation", "execution"} {
		t.Run(phase, func(t *testing.T) { testCancelledAttempt(t, phase) })
	}
}

func testCancelledAttempt(t *testing.T, phase string) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	if phase == "preparation" {
		check.Toolchain[0].Command = []string{"sh", "-c", "touch target/probe-started; sleep 10"}
	} else {
		check.Command = []string{"sh", "-c", "touch target/probe-started; sleep 10"}
	}
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	ctx, cancel := context.WithCancel(repo.Context)
	defer cancel()
	repo.Context = ctx
	dir := filepath.Join(f.Root, "target/cancelled")
	probe := filepath.Join(f.Root, "target/probe-started")
	done := make(chan bool, 1)
	go func() {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-deadline.C:
				cancel()
				done <- false
				return
			case <-tick.C:
				if _, err := os.Stat(probe); err == nil {
					cancel()
					done <- true
					return
				}
			}
		}
	}()
	receipt, err := Run(repo, config, "local", dir)
	if !<-done || err != nil || receipt.ExecutionStarted != (phase == "execution") || receipt.Status != Failed || receipt.Phase != phase || receipt.Problems[0].Code != "cancelled" {
		t.Fatal(receipt, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved Receipt
	if err := json.Unmarshal(data, &saved); err != nil || saved.ReceiptSHA256 != receipt.ReceiptSHA256 {
		t.Fatal(saved, err)
	}
}

func TestMissingExecutableIsPreparationFailureWithIntactLogs(t *testing.T) {
	f := verificationFixture(t)
	check := f.Config.Checks["local"]
	check.Toolchain[0].Command = []string{"concave-missing-tool-fixture"}
	f.Config.Checks["local"] = check
	f.Save()
	repo, config := f.Open()
	dir := filepath.Join(f.Root, "target/missing")
	receipt, err := Run(repo, config, "local", dir)
	if err != nil || receipt.Status != Failed || receipt.ExecutionStarted || receipt.Results.Executed != 0 || receipt.Problems[0].Code != "tool_unavailable" {
		t.Fatal(receipt, err)
	}
	if err := ValidateLogs(dir, receipt); err != nil {
		t.Fatal(err)
	}
	if err := Validate(repo, config, receipt); err == nil {
		t.Fatal("unexecuted failure accepted")
	}
}
