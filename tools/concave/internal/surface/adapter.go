package surface

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"concave/internal/repository"
)

// The compiler modules travel inside the Go binary. Each capture materializes a
// private ES module directory; no bundler, install step or persistent runtime.
//
//go:embed adapter/*.mjs
var adapterFiles embed.FS

var adapterSHA256 = func() string {
	entries, err := adapterFiles.ReadDir("adapter")
	if err != nil {
		panic(err)
	}
	sources := map[string]string{}
	for _, entry := range entries {
		data, err := adapterFiles.ReadFile("adapter/" + entry.Name())
		if err != nil {
			panic(err)
		}
		sources[entry.Name()] = string(data)
	}
	digest, err := repository.JSONDigest(sources)
	if err != nil {
		panic(err)
	}
	return digest
}()

func materializeAdapter() (entry string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "concave-typescript-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	entries, err := adapterFiles.ReadDir("adapter")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	for _, file := range entries {
		data, readErr := adapterFiles.ReadFile("adapter/" + file.Name())
		if readErr == nil {
			readErr = os.WriteFile(filepath.Join(dir, file.Name()), data, 0600)
		}
		if readErr != nil {
			cleanup()
			return "", nil, fmt.Errorf("materialize compiler module: %w", readErr)
		}
	}
	return filepath.Join(dir, "main.mjs"), cleanup, nil
}
