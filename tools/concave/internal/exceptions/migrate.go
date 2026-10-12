package exceptions

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"concave/internal/artifact"
	"concave/internal/policy"
	"concave/internal/repository"
)

// RetiredBindings is the single approval file that preceded per-path binding
// files. Only Migrate reads it; policy loading never does.
const RetiredBindings = "concave.exceptions.json"

// Migration converts the retired file's bindings into binding files.
type Migration struct {
	Format    int      `json:"format"`
	Kind      string   `json:"kind"`
	Status    Status   `json:"status"`
	Source    string   `json:"source"`
	Base      string   `json:"base,omitempty"`
	Bindings  int      `json:"bindings"`
	Written   []string `json:"written"`
	Deleted   []string `json:"deleted"`
	Unchanged int      `json:"unchanged"`
	files     map[string][]byte
	observed  repository.Inputs
}

// PrepareMigration converts the working retired file. On a checkout with no
// binding files it converts every path. Where binding files already exist, it
// converts only the paths whose bindings differ from the retired file in the
// newest HEAD ancestor carrying it, so local edits land on top of the
// published conversion instead of reverting it.
func PrepareMigration(repo *repository.Repo, config *policy.Config) (*Migration, error) {
	state, data, err := repo.Snapshot(RetiredBindings)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("%s is absent; nothing to migrate", RetiredBindings)
	}
	local, err := decodeRetired(data)
	if err != nil {
		return nil, err
	}
	m := &Migration{Format: 1, Kind: "concave-binding-migration", Status: Preview, Source: RetiredBindings, Bindings: len(local), Written: []string{}, Deleted: []string{}, files: map[string][]byte{}, observed: repository.Inputs{RetiredBindings: state}}
	selected := map[string]bool{}
	for _, binding := range local {
		selected[binding.Path] = true
	}
	if len(config.Bindings) > 0 {
		base, err := retiredBase(repo)
		if err != nil {
			return nil, err
		}
		m.Base = base.commit
		selected = changedPaths(base.bindings, local)
	}
	byPath := map[string][]policy.Exception{}
	for _, binding := range local {
		byPath[binding.Path] = append(byPath[binding.Path], binding)
	}
	next := *config
	next.Exceptions = []policy.Exception{}
	for _, binding := range config.Exceptions {
		if !selected[binding.Path] {
			next.Exceptions = append(next.Exceptions, binding)
		}
	}
	paths := make([]string, 0, len(selected))
	for source := range selected {
		paths = append(paths, source)
		next.Exceptions = append(next.Exceptions, byPath[source]...)
	}
	slices.Sort(paths)
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("migrated bindings: %w", err)
	}
	for _, source := range paths {
		encoded, err := policy.EncodeBindings(source, byPath[source])
		if err != nil {
			return nil, err
		}
		file := policy.BindingPath(source)
		fileState, current, err := repo.Snapshot(file)
		if err != nil {
			return nil, err
		}
		m.observed[file] = fileState
		switch {
		case encoded == nil && fileState == nil, encoded != nil && fileState != nil && bytes.Equal(current, encoded):
			m.Unchanged++
		case encoded == nil:
			m.Deleted = append(m.Deleted, file)
			m.files[file] = nil
		default:
			m.Written = append(m.Written, file)
			m.files[file] = encoded
		}
	}
	return m, nil
}

// Write applies the migration exactly as previewed and deletes the retired
// file, after verifying every observed input.
func (m *Migration) Write(repo *repository.Repo) error {
	if err := m.observed.Verify(repo, repository.SourceInputs); err != nil {
		return err
	}
	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range slices.Sorted(maps.Keys(m.files)) {
		data := m.files[file]
		if data == nil {
			if err := root.Remove(file); err != nil {
				return err
			}
			removeEmptyParents(root, path.Dir(file))
			continue
		}
		if err := root.MkdirAll(path.Dir(file), 0o755); err != nil {
			return err
		}
		if err := root.WriteFile(file, data, 0o644); err != nil {
			return err
		}
	}
	if err := root.Remove(RetiredBindings); err != nil {
		return err
	}
	m.Status = Recorded
	return nil
}

func removeEmptyParents(root *os.Root, dir string) {
	for dir != policy.BindingsDirectory && strings.HasPrefix(dir, policy.BindingsDirectory+"/") {
		if root.Remove(dir) != nil {
			return
		}
		dir = path.Dir(dir)
	}
}

func decodeRetired(data []byte) ([]policy.Exception, error) {
	var retired struct {
		Format     int                `json:"format"`
		Exceptions []policy.Exception `json:"exceptions"`
	}
	if err := artifact.Decode(data, &retired); err != nil {
		return nil, fmt.Errorf("%s: %w", RetiredBindings, err)
	}
	if retired.Format != 1 {
		return nil, fmt.Errorf("%s: unsupported format", RetiredBindings)
	}
	return retired.Exceptions, nil
}

type retired struct {
	commit   string
	bindings []policy.Exception
}

// retiredBase is the retired file at the newest HEAD ancestor carrying it.
func retiredBase(repo *repository.Repo) (*retired, error) {
	out, err := repo.Git(nil, "rev-list", "-1", "HEAD", "--", RetiredBindings)
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(out))
	if commit == "" {
		return nil, fmt.Errorf("no HEAD ancestor carries %s; binding files already exist", RetiredBindings)
	}
	tree, err := repo.Tree(commit)
	if err != nil {
		return nil, err
	}
	if tree[RetiredBindings].OID == "" {
		// The newest commit touching it deleted it; its parent carries it.
		if commit, err = repo.Resolve(commit + "^"); err != nil {
			return nil, err
		}
		if tree, err = repo.Tree(commit); err != nil {
			return nil, err
		}
	}
	data, err := repo.Blob(tree[RetiredBindings].OID)
	if err != nil {
		return nil, err
	}
	bindings, err := decodeRetired(data)
	if err != nil {
		return nil, err
	}
	return &retired{commit: commit, bindings: bindings}, nil
}

// changedPaths names the source paths whose bindings differ between two sets.
func changedPaths(base, local []policy.Exception) map[string]bool {
	group := func(bindings []policy.Exception) map[string][]byte {
		byPath := map[string][]policy.Exception{}
		for _, binding := range bindings {
			byPath[binding.Path] = append(byPath[binding.Path], binding)
		}
		encoded := map[string][]byte{}
		for source, xs := range byPath {
			encoded[source], _ = policy.EncodeBindings(source, xs)
		}
		return encoded
	}
	before, after := group(base), group(local)
	changed := map[string]bool{}
	for source, data := range after {
		if !bytes.Equal(before[source], data) {
			changed[source] = true
		}
	}
	for source := range before {
		if _, ok := after[source]; !ok {
			changed[source] = true
		}
	}
	return changed
}
