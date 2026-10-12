package repository

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Stale means an observed input changed. Commands use exit 3 consistently.
type Stale struct{ Message string }

func (err *Stale) Error() string { return err.Message }

func IsStale(err error) bool {
	var stale *Stale
	return errors.As(err, &stale)
}

// InputAccess selects the read contract. Source inputs bind Git mode and object
// identity and never follow symlinks. Compiler lookups bind content or absence,
// distinguish directories, and allow installed dependency symlinks.
type InputAccess uint8

const (
	SourceInputs InputAccess = iota
	CompilerInputs
)

type Inputs map[string]*State

func (inputs Inputs) Observe(path string, state *State) error {
	if previous, observed := inputs[path]; observed && !Same(previous, state) {
		return &Stale{Message: "input changed: " + path}
	}
	inputs[path] = state
	return nil
}
func (inputs Inputs) Verify(repo *Repo, access InputAccess) error {
	if len(inputs) > 40000 {
		return fmt.Errorf("input count exceeds bound")
	}
	paths := make([]string, 0, len(inputs))
	for path := range inputs {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var total int64
	limit := int64(MaxOutput)
	if access == CompilerInputs {
		// Compiler catalogs bind multiple installed engines in addition to source.
		limit = 256 << 20
	}
	reader := repo.Reader()
	defer reader.Close()
	for _, path := range paths {
		var state *State
		var err error
		switch access {
		case SourceInputs:
			state, err = reader.Identity(path)
		case CompilerInputs:
			state, err = repo.compilerInput(path)
		default:
			return fmt.Errorf("unknown input access")
		}
		if err != nil {
			return err
		}
		if state != nil {
			total += state.Size
		}
		if total > limit {
			return fmt.Errorf("input bytes exceed bound")
		}
		if !Same(inputs[path], state) {
			return &Stale{Message: "input changed: " + path}
		}
	}
	return nil
}

// Inventory is sorted and unique, including tracked deletions and ordinary
// untracked files. Callers retain their own selection policy.
func (repo *Repo) Inventory() ([]string, error) {
	if repo.Revision != "" {
		paths := make([]string, 0, len(repo.sourceTree))
		for path := range repo.sourceTree {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		return paths, nil
	}
	data, err := repo.Git(nil, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return []string{}, nil
	}
	slices.Sort(paths)
	return slices.Compact(paths), nil
}
func (repo *Repo) CaptureInputs(selectors, required []string) (Inputs, error) {
	inventory, err := repo.Inventory()
	if err != nil {
		return nil, err
	}
	return repo.CaptureInventory(inventory, selectors, required)
}

// CaptureInventory binds a caller-owned membership snapshot without a second
// Git inventory traversal. Admission owners still recapture before acceptance.
func (repo *Repo) CaptureInventory(inventory, selectors, required []string) (Inputs, error) {
	paths := map[string]bool{}
	for _, path := range required {
		paths[path] = true
	}
	matched := make([]bool, len(selectors))
	for _, path := range inventory {
		for index, selector := range selectors {
			if Matches(path, []string{selector}) {
				paths[path] = true
				matched[index] = true
			}
		}
	}
	for index, selector := range selectors {
		if !matched[index] {
			return nil, fmt.Errorf("input selector matches no source: %s", selector)
		}
	}
	if len(paths) > 20000 {
		return nil, fmt.Errorf("input count exceeds bound")
	}
	inputs := Inputs{}
	var total int64
	for path := range paths {
		state, _, err := repo.Snapshot(path)
		if err != nil {
			return nil, err
		}
		if state != nil {
			if state.Kind != "file" && !(state.Kind == "oversize" && state.OID != "" && state.Size <= MaxOutput) {
				return nil, fmt.Errorf("input needs bounded regular source: %s", path)
			}
			total += state.Size
		}
		if total > MaxOutput {
			return nil, fmt.Errorf("input bytes exceed bound")
		}
		inputs[path] = state
	}
	return inputs, nil
}

func (repo *Repo) compilerInput(path string) (*State, error) {
	repo.Reads++
	ancestor := path == "."
	if path != "" && path != "." {
		ancestor = true
		for _, part := range strings.Split(path, "/") {
			if part != ".." {
				ancestor = false
				break
			}
		}
	}
	dependency := strings.Contains("/"+path+"/", "/node_modules/") && filepath.ToSlash(filepath.Clean(path)) == path
	if filepath.IsAbs(path) || path == "" || strings.ContainsAny(path, "\\\x00") || !ValidPath(path) && !ancestor && !dependency {
		return nil, fmt.Errorf("compiler input outside repository or installed dependencies: %s", path)
	}
	filename := filepath.Join(repo.Root, filepath.FromSlash(path))
	before, err := os.Stat(filename)
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if before.IsDir() {
		return &State{Kind: "directory"}, nil
	}
	if ancestor || !before.Mode().IsRegular() || before.Size() > 16<<20 {
		return nil, fmt.Errorf("unsupported compiler input: %s", path)
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	after, err := os.Stat(filename)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || len(data) > 16<<20 {
		return nil, &Stale{Message: "input changed while reading: " + path}
	}
	return &State{Kind: "file", SHA256: Digest(data), Size: int64(len(data))}, nil
}
