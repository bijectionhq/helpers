package repository_test

import (
	"concave/internal/repository"
	"concave/internal/testrepo"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotBindsAbsenceModeAndImmutableRevision(t *testing.T) {
	for _, change := range []string{"appearance", "mode", "content", "head"} {
		t.Run(change, func(t *testing.T) {
			fixture := testrepo.New(t)
			repo, _ := fixture.Open()
			snapshot, err := repository.OpenSnapshot(repo, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = snapshot.Read("crates/core/lib.rs"); err != nil {
				t.Fatal(err)
			}
			if _, err = snapshot.Read("missing.rs"); err == nil {
				t.Fatal("missing source was readable")
			}
			historical, err := repository.OpenSnapshot(repo, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			original, err := historical.Read("crates/core/lib.rs")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "appearance":
				fixture.Write("missing.rs", "fn new_source() {}")
			case "content":
				fixture.Write("crates/core/lib.rs", "fn changed() {}")
			case "mode":
				if err := os.Chmod(filepath.Join(fixture.Root, "crates/core/lib.rs"), 0755); err != nil {
					t.Fatal(err)
				}
			case "head":
				fixture.Write("peer.txt", "advance")
				fixture.Commit()
			}
			var stale *repository.Stale
			if _, err = snapshot.Verify(); !errors.As(err, &stale) {
				t.Fatal("working snapshot not stale", err)
			}
			if _, err = historical.Verify(); err != nil {
				t.Fatal(err)
			}
			current, err := historical.Read("crates/core/lib.rs")
			if err != nil || string(current) != string(original) {
				t.Fatal("immutable revision drifted", err)
			}
		})
	}
}

func TestCompilerInputsDistinguishAbsenceDirectoriesAndDependencies(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	manifest := repository.Inputs{"missing": nil, ".": {Kind: "directory"}}
	if err := manifest.Verify(repo, repository.CompilerInputs); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture.Root, "missing"), 0700); err != nil {
		t.Fatal(err)
	}
	var stale *repository.Stale
	if err := manifest.Verify(repo, repository.CompilerInputs); !errors.As(err, &stale) {
		t.Fatal("directory appearance not detected", err)
	}
	dependency := filepath.Join(t.TempDir(), "node_modules", "compiler.js")
	if err := os.MkdirAll(filepath.Dir(dependency), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dependency, []byte("compiler"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(dependency), filepath.Join(fixture.Root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	manifest = repository.Inputs{"node_modules/compiler.js": {Kind: "file", SHA256: repository.Digest([]byte("compiler")), Size: 8}}
	if err := manifest.Verify(repo, repository.CompilerInputs); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Verify(repo, repository.SourceInputs); !errors.As(err, &stale) {
		t.Fatal("source followed dependency symlink", err)
	}
	for _, path := range []string{"", "../../private.txt", "/tmp/node_modules/file", "../../../escape/../../file"} {
		if err := (repository.Inputs{path: nil}).Verify(repo, repository.CompilerInputs); err == nil {
			t.Fatal("unsafe compiler input", path)
		}
	}
	large := filepath.Join(fixture.Root, "large.ts")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	err = file.Truncate((16 << 20) + 1)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := (repository.Inputs{"large.ts": nil}).Verify(repo, repository.CompilerInputs); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestInputObservationAndInventory(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	inputs, err := repo.CaptureInputs([]string{"crates/core/"}, []string{"absent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, seen := inputs["absent"]; !seen {
		t.Fatal("negative required input omitted")
	}
	if err := inputs.Observe("absent", &repository.State{Kind: "file"}); err == nil {
		t.Fatal("contradictory observation accepted")
	}
	fixture.Write("crates/core/new.rs", "fn new_source() {}")
	next, err := repo.CaptureInputs([]string{"crates/core/"}, nil)
	if err != nil || next["crates/core/new.rs"] == nil {
		t.Fatal("untracked source omitted", err)
	}
	if _, err := repo.CaptureInputs([]string{"unknown/"}, nil); err == nil {
		t.Fatal("empty selector accepted")
	}
}

// Every component of a working path is classified exactly as Git would track it:
// a symlinked ancestor or leaf is never followed, and absence anywhere is absence.
func TestSnapshotClassifiesEveryPathComponent(t *testing.T) {
	fixture := testrepo.New(t)
	repo, _ := fixture.Open()
	fixture.Write("a/b/c/leaf.rs", "fn leaf() {}\n")
	fixture.Write("outside/leaf.rs", "fn outside() {}\n")
	link := func(target, name string) {
		if err := os.Symlink(target, filepath.Join(fixture.Root, name)); err != nil {
			t.Fatal(err)
		}
	}
	link("b", "a/alias")
	link("leaf.rs", "a/b/c/link.rs")
	link("../outside", "a/b/escape")
	state, data, err := repo.Snapshot("a/b/c/leaf.rs")
	if err != nil || state.Kind != "file" || string(data) != "fn leaf() {}\n" || state.OID != fixture.Git("hash-object", "a/b/c/leaf.rs") {
		t.Fatal("nested file", state, err)
	}
	for _, path := range []string{"a/alias/c/leaf.rs", "a/b/c/link.rs", "a/b/escape/leaf.rs"} {
		state, data, err := repo.Snapshot(path)
		kind := "symlink_parent"
		if path == "a/b/c/link.rs" {
			kind = "symlink"
		}
		if err != nil || state.Kind != kind || data != nil {
			t.Fatal("followed symlink", path, state, err)
		}
		if kind == "symlink" && (state.Mode != "120000" || state.OID != repo.OID([]byte("leaf.rs"))) {
			t.Fatal("link identity lost", state)
		}
	}
	if state, _, err := repo.Snapshot("a/b/c"); err != nil || state.Kind != "nonregular" {
		t.Fatal("directory", state, err)
	}
	for _, path := range []string{"a/missing/leaf.rs", "a/b/c/missing.rs", "missing/leaf.rs"} {
		if state, data, err := repo.Snapshot(path); err != nil || state != nil || data != nil {
			t.Fatal("absent path", path, state, err)
		}
	}
	if _, _, err := repo.Snapshot("a/b/c/leaf.rs/below.rs"); err == nil {
		t.Fatal("file ancestor was traversed")
	}
	// One reader reuses directories across paths yet classifies each exactly as
	// a lone snapshot does, in any order, beneath links and absences it passed
	// and after a refused path.
	fixture.Write("a/bb/leaf.rs", "fn sibling() {}\n")
	fixture.Write("a/b/top.rs", "fn top() {}\n")
	reader := repo.Reader()
	defer reader.Close()
	for _, path := range []string{"a/b/c/leaf.rs", "a/alias/c/leaf.rs", "a/alias/other.rs", "a/b/c/link.rs", "a/b/escape/leaf.rs",
		"a/missing/leaf.rs", "a/missing/deeper/leaf.rs", "a/b/c/missing.rs", "missing/leaf.rs", "a/bb/leaf.rs", "a/b/top.rs",
		"a/b/c", "a/alias/c/leaf.rs", "a/b/c/leaf.rs", "a/b/c/leaf.rs/below.rs", "a/b/c/leaf.rs"} {
		state, data, err := reader.Snapshot(path)
		want, wantData, wantErr := repo.Snapshot(path)
		if (err == nil) != (wantErr == nil) || !repository.Same(state, want) || string(data) != string(wantData) {
			t.Fatal("reader differs from a lone snapshot", path, state, want, err, wantErr)
		}
		// Identity streams the same state without retaining the bytes.
		if identity, err := reader.Identity(path); (err == nil) != (wantErr == nil) || !repository.Same(identity, want) {
			t.Fatal("identity differs from a lone snapshot", path, identity, want, err)
		}
	}
}
