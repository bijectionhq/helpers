//go:build !unix

package process

import "os/exec"

func ownedProcess(c *exec.Cmd) {}

func cleanupProcess(c *exec.Cmd) {}
