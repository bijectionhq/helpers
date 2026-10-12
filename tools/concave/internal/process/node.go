package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Node resolves the repository-pinned runtime without changing global tools:
// CONCAVE_NODE, else the installation of the repository's declared tool
// manager (mise.toml reads .nvmrc), else the exact-version installations this
// repository's other tooling already reads (the official distribution unpacked
// under target/core-tools, then nvm's versions directory), else PATH. Each
// must report the version, so an extra location can never select another one.
func Node(ctx context.Context, root, version string) (string, error) {
	candidates := []string{os.Getenv("CONCAVE_NODE")}
	if mise, _, err := ExecutableIn(ctx, root, "mise"); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := Run(ctx, root, []string{mise, "where", "node@" + version}, nil, 4096)
		cancel()
		if where := strings.TrimSpace(string(out.Stdout)); err == nil && filepath.IsAbs(where) {
			candidates = append(candidates, filepath.Join(where, "bin", "node"))
		}
	}
	if arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]; arch != "" {
		candidates = append(candidates, filepath.Join(root, "target", "core-tools", "node-v"+version+"-"+runtime.GOOS+"-"+arch, "bin", "node"))
	}
	nvm := os.Getenv("NVM_DIR")
	if home, err := os.UserHomeDir(); nvm == "" && err == nil {
		nvm = filepath.Join(home, ".nvm")
	}
	if filepath.IsAbs(nvm) {
		candidates = append(candidates, filepath.Join(nvm, "versions", "node", "v"+version, "bin", "node"))
	}
	candidates = append(candidates, "node")
	for _, path := range candidates {
		if path == "" {
			continue
		}
		executable, _, err := ExecutableIn(ctx, root, path)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		out, err := Run(ctx, root, []string{executable, "-p", "process.versions.node"}, nil, 4096)
		cancel()
		if err == nil && strings.TrimSpace(string(out.Stdout)) == version {
			return executable, nil
		}
	}
	return "", fmt.Errorf("Node %s from .nvmrc is required; install it with mise install or nvm install, unpack its official distribution under target/core-tools, set CONCAVE_NODE to its executable or select that runtime on PATH", version)
}
