package syntax

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeFailuresRemainOperational(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 'ast-grep "+Version+"'; else echo malformed-output; fi\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &Engine{ctx: ctx, path: path}
	if err := e.Check(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Scan(nil, nil); !IsUnavailable(err) {
		t.Fatal("invalid engine transport treated as source finding", err)
	}
	cancel()
	if _, err := e.Scan(nil, nil); !IsUnavailable(err) {
		t.Fatal("cancellation treated as source finding", err)
	}
}
