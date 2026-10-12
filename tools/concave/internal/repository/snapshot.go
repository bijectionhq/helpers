package repository

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

type Snapshot struct {
	Repo      *Repo
	Revision  string
	Head      string
	Tree      map[string]Entry
	Inputs    Inputs
	reads     map[string]snapshotRead
	readBytes int
}

type snapshotRead struct {
	data []byte
	err  error
}

type Unavailable struct{ Path string }

func (e *Unavailable) Error() string { return e.Path + ": missing bounded regular UTF-8 source" }

func OpenSnapshot(repo *Repo, revision string) (*Snapshot, error) {
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return nil, err
	}
	if revision != "" {
		revision, err = repo.Resolve(revision)
		if err != nil {
			return nil, err
		}
	}
	target := revision
	if target == "" {
		target = head
	}
	tree, err := repo.Tree(target)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Repo: repo, Revision: revision, Head: head, Tree: tree, Inputs: Inputs{}}, nil
}

// Read never follows symlinks and records even a missing path, so appearance or
// disappearance during a working inspection invalidates that inspection.
// Captured bytes are reused within a 32 MiB invocation-local cache. Verify always
// rereads live inputs; callers must verify before accepting the snapshot.
func (snapshot *Snapshot) Read(path string) ([]byte, error) {
	if !ValidPath(path) || strings.HasSuffix(path, "/") {
		return nil, fmt.Errorf("unsafe source path %q", path)
	}
	if cached, ok := snapshot.reads[path]; ok {
		return bytes.Clone(cached.data), cached.err
	}
	var state *State
	var data []byte
	var err error
	if snapshot.Revision == "" {
		state, data, err = snapshot.Repo.Snapshot(path)
	} else {
		entry, ok := snapshot.Tree[path]
		if ok {
			if entry.Mode == "100644" || entry.Mode == "100755" {
				data, err = snapshot.Repo.Blob(entry.OID)
			}
			if err == nil {
				state, err = snapshot.Repo.BlobState(entry, data)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err := snapshot.Inputs.Observe(path, state); err != nil {
		return nil, err
	}
	if state == nil || state.Kind != "file" || !Text(data) {
		data, err = nil, &Unavailable{Path: path}
	}
	if len(snapshot.reads) < 40000 && len(data) <= (32<<20)-snapshot.readBytes {
		if snapshot.reads == nil {
			snapshot.reads = map[string]snapshotRead{}
		}
		snapshot.reads[path] = snapshotRead{bytes.Clone(data), err}
		snapshot.readBytes += len(data)
	}
	return data, err
}
func (snapshot *Snapshot) Verify() (string, error) {
	if snapshot.Revision == "" {
		head, err := snapshot.Repo.Resolve("HEAD")
		if err != nil {
			return "", err
		}
		if head != snapshot.Head {
			return "", &Stale{Message: "snapshot changed: HEAD advanced"}
		}
		if err := snapshot.Inputs.Verify(snapshot.Repo, SourceInputs); err != nil {
			return "", err
		}
	}
	return JSONDigest(struct {
		Revision string
		Inputs   Inputs
	}{snapshot.Revision, snapshot.Inputs})
}
func (snapshot *Snapshot) Paths() []string {
	paths := make([]string, 0, len(snapshot.Tree))
	for path := range snapshot.Tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
