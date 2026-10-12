package coverage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationDeadlineIsInconclusive(t *testing.T) {
	root := t.TempDir()
	source := []byte("original")
	if err := os.WriteFile(filepath.Join(root, "f.go"), source, 0600); err != nil {
		t.Fatal(err)
	}
	m := Mutation{Request: Request{Path: "f.go", Source: source}, Mutated: []byte("mutant")}
	r := &Receipt{}
	calls := 0
	err := decide(context.Background(), root, m, func() (bool, []string, []byte, error) {
		calls++
		if calls == 1 {
			return true, nil, nil, nil
		}
		return false, nil, nil, context.DeadlineExceeded
	}, r)
	if err != nil || r.Outcome != Inconclusive || calls != 2 {
		t.Fatal(r, err, calls)
	}
	restored, err := os.ReadFile(filepath.Join(root, "f.go"))
	if err != nil || string(restored) != string(source) {
		t.Fatal(string(restored), err)
	}
}
