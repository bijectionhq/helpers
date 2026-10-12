package syntax

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"concave/internal/repository"
)

var releases = map[string]struct{ Name, SHA string }{
	"darwin/arm64": {"app-aarch64-apple-darwin.zip", "6d2279dea5bea2ad79c66ea93f5fe54ba926e398a8a26de76c56db68fe59eac6"},
	"darwin/amd64": {"app-x86_64-apple-darwin.zip", "b2ffd26f42810340326a9e8a084bdc3647a8795c1a3f21fc06bd7bef3c7c5b2c"},
	"linux/arm64":  {"app-aarch64-unknown-linux-gnu.zip", "b39cfbc58da4b869a88b8a4bc57bd5deb0d24541e704cf7c257da7b53ec81c8f"},
	"linux/amd64":  {"app-x86_64-unknown-linux-gnu.zip", "f8ac830881339d1edee6b2652f54798c0f4da5a827f2db38a08ee31117783ce8"},
}

// Install is explicitly invoked. Inspection/check never download dependencies.
func Install(r *repository.Repo) (string, error) {
	asset, ok := releases[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no pinned ast-grep distribution for this platform")
	}
	dst := filepath.Join(r.Root, "target", "concave", "engine", Version)
	binary := filepath.Join(dst, "ast-grep")
	existing := false
	if info, err := os.Lstat(dst); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("engine destination is not a directory")
		}
		existing = true
		e := &Engine{ctx: r.Context, path: binary}
		if e.Check() == nil {
			return binary, nil
		}
		if _, err = r.Git(nil, "check-ignore", "-q", "--", "target/concave/engine/"+Version+"/"); err != nil {
			return "", fmt.Errorf("engine installation must stay in ignored output")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	ctx, cancel := context.WithTimeout(r.Context, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://github.com/ast-grep/ast-grep/releases/download/"+Version+"/"+asset.Name, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ast-grep download: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, repository.MaxOutput+1))
	if err != nil {
		return "", err
	}
	if len(data) > repository.MaxOutput || repository.Digest(data) != asset.SHA {
		return "", fmt.Errorf("ast-grep archive size or SHA-256 mismatch")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var program []byte
	for _, entry := range archive.File {
		if entry.Name != "ast-grep" {
			continue
		}
		if program != nil || entry.UncompressedSize64 > repository.MaxOutput {
			return "", fmt.Errorf("invalid ast-grep archive")
		}
		f, err := entry.Open()
		if err != nil {
			return "", err
		}
		program, err = io.ReadAll(io.LimitReader(f, repository.MaxOutput+1))
		f.Close()
		if err != nil || len(program) > repository.MaxOutput {
			return "", fmt.Errorf("invalid ast-grep executable")
		}
	}
	if len(program) == 0 {
		return "", fmt.Errorf("archive has no ast-grep executable")
	}
	// Create the destination only after the complete, checked download is available.
	dir := dst
	if !existing {
		dir, err = r.OutputDirectory(dst)
		if err != nil {
			return "", err
		}
	}
	f, err := os.CreateTemp(dir, ".ast-grep-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0700); err == nil {
		_, err = f.Write(program)
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	e := &Engine{ctx: r.Context, path: f.Name()}
	if err = e.Check(); err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), binary); err != nil {
		return "", err
	}
	return binary, nil
}
