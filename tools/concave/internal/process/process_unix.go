//go:build unix

package process

import (
	"os/exec"
	"syscall"
)

func ownedProcess(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Give owners a bounded opportunity to retain receipts and remove scratch.
	// WaitDelay and cleanupProcess still kill every remaining owned child.
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
}

func cleanupProcess(c *exec.Cmd) {
	if c.Process != nil {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}
