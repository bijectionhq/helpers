// Package process runs bounded local tool invocations without a shell.
package process

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type environmentKey struct{}

// WithEnvironment scopes child settings to an invocation, never the host process.
func WithEnvironment(ctx context.Context, values ...string) context.Context {
	return context.WithValue(ctx, environmentKey{}, append(environment(ctx), values...))
}

// WithPath prepends a selected runtime while preserving earlier scoped runtimes.
func WithPath(ctx context.Context, directory string) context.Context {
	path := ""
	for _, value := range environment(ctx) {
		if strings.HasPrefix(value, "PATH=") {
			path = strings.TrimPrefix(value, "PATH=")
		}
	}
	return WithEnvironment(ctx, "PATH="+directory+string(os.PathListSeparator)+path)
}

func environment(ctx context.Context) []string {
	if values, ok := ctx.Value(environmentKey{}).([]string); ok {
		return append([]string{}, values...)
	}
	return os.Environ()
}

func executable(ctx context.Context, dir, name string) (string, error) {
	if strings.ContainsAny(name, "/\\") {
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		return name, nil
	}
	path := os.Getenv("PATH")
	for _, value := range environment(ctx) {
		if strings.HasPrefix(value, "PATH=") {
			path = strings.TrimPrefix(value, "PATH=")
		}
	}
	for _, base := range filepath.SplitList(path) {
		if !filepath.IsAbs(base) {
			continue
		}
		candidate := filepath.Join(base, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %s is unavailable on the invocation PATH", name)
}

func ExecutableIn(ctx context.Context, dir, name string) (string, string, error) {
	path, err := executable(ctx, dir, name)
	if err != nil {
		return "", "", err
	}
	return Executable(dir, path)
}

type buffer struct {
	sync.Mutex
	bytes.Buffer
	max      int
	overflow bool
}

func (b *buffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	if b.Len()+len(p) > b.max {
		b.overflow = true
		return 0, fmt.Errorf("tool output exceeds %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}

type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
	Elapsed        time.Duration
	Started        bool
}

func Run(ctx context.Context, dir string, argv []string, input []byte, limit int) (Result, error) {
	return RunOutput(ctx, dir, argv, input, limit, nil, nil)
}

// RunStreaming retains bounded output while exposing a child's progress on stderr.
func RunStreaming(ctx context.Context, dir string, argv []string, input []byte, limit int, stderr io.Writer) (Result, error) {
	return RunOutput(ctx, dir, argv, input, limit, nil, stderr)
}

// RunOutput streams output through the same bounded process and cancellation owner.
func RunOutput(ctx context.Context, dir string, argv []string, input []byte, limit int, stdout, stderr io.Writer) (Result, error) {
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("empty command")
	}
	path, err := executable(ctx, dir, argv[0])
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	c := exec.CommandContext(ctx, path, argv[1:]...)
	c.Env = environment(ctx)
	c.Dir = dir
	c.Stdin = bytes.NewReader(input)
	c.WaitDelay = 10 * time.Second
	ownedProcess(c)
	out, errout := &buffer{max: limit}, &buffer{max: limit}
	c.Stdout = out
	if stdout != nil {
		c.Stdout = io.MultiWriter(out, stdout)
	}
	c.Stderr = errout
	if stderr != nil {
		c.Stderr = io.MultiWriter(errout, stderr)
	}
	start := time.Now()
	err = c.Run()
	cleanupProcess(c)
	code := -1
	if c.ProcessState != nil {
		code = c.ProcessState.ExitCode()
	}
	r := Result{append([]byte(nil), out.Bytes()...), append([]byte(nil), errout.Bytes()...), code, time.Since(start), c.ProcessState != nil}
	if out.overflow || errout.overflow {
		return r, fmt.Errorf("tool output exceeds bound")
	}
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	return r, err
}
