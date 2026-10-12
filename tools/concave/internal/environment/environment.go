// Package environment prepares candidate-owned dependencies through pinned
// repository tooling. Package downloads may be shared; workspace links may not.
package environment

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

type State struct {
	Recipe         int                `json:"recipe"`
	Name           string             `json:"name"`
	Definition     policy.Environment `json:"definition"`
	Inputs         repository.Inputs  `json:"inputs"`
	Platform       string             `json:"platform"`
	NodeVersion    string             `json:"node_version"`
	NodeSHA256     string             `json:"node_sha256"`
	PackageManager string             `json:"package_manager"`
	// Includes binds the prepared state of each included environment.
	Includes []*State `json:"includes,omitempty"`
	SHA256   string   `json:"sha256"`
}

// Session shares preparation only within one candidate invocation. No persistent
// ready marker can substitute for frozen installation and source validation.
type Session struct {
	repo     *repository.Repo
	config   *policy.Config
	output   string
	prepared map[string]preparation
}

// Prepared carries runtime settings separately from persisted evidence.
type Prepared struct {
	State   *State
	Context context.Context
}
type preparation struct {
	prepared *Prepared
	err      error
}

func New(repo *repository.Repo, config *policy.Config, output string) *Session {
	return &Session{repo: repo, config: config, output: output, prepared: map[string]preparation{}}
}

// Inputs returns the source boundary used by both materialization and evidence.
func Inputs(repo *repository.Repo, definition policy.Environment) (repository.Inputs, error) {
	paths := []string{".nvmrc", ".npmrc", "scripts/package.json", "scripts/package-lock.json", "scripts/npm_toolchain.json", "scripts/qualification_toolchain.py"}
	for _, file := range []string{"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc", ".pnpmfile.cjs"} {
		paths = append(paths, definition.Directory+"/"+file)
	}
	inventory, err := repo.Inventory()
	if err != nil {
		return nil, err
	}
	for _, path := range inventory {
		if strings.HasPrefix(path, definition.Directory+"/") && (filepath.Base(path) == "package.json" || filepath.Base(path) == ".npmrc" || strings.HasPrefix(path, definition.Directory+"/patches/")) {
			paths = append(paths, path)
		}
	}
	inputs, err := repo.CaptureInventory(inventory, nil, paths)
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func Capture(repo *repository.Repo, name string, definition policy.Environment) (*State, error) {
	inputs, err := Inputs(repo, definition)
	if err != nil {
		return nil, err
	}
	state := &State{Recipe: 1, Name: name, Definition: definition, Inputs: inputs, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	_, node, err := repo.Snapshot(".nvmrc")
	if err != nil || len(node) == 0 {
		return nil, fmt.Errorf("environment requires .nvmrc: %v", err)
	}
	state.NodeVersion = strings.TrimPrefix(strings.TrimSpace(string(node)), "v")
	_, manifest, err := repo.Snapshot("scripts/package.json")
	if err != nil {
		return nil, err
	}
	var tools struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(manifest, &tools); err != nil {
		return nil, err
	}
	state.PackageManager = tools.DevDependencies["pnpm"]
	if state.PackageManager == "" {
		return nil, fmt.Errorf("scripts/package.json must pin pnpm")
	}
	return state, nil
}

// Node searches only the selected executable and the repository's existing tool
// installation. It never changes the machine's global runtime or shell settings.
func Node(repo *repository.Repo) (string, error) {
	_, data, err := repo.Snapshot(".nvmrc")
	if err != nil {
		return "", err
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	return process.Node(repo.Context, repo.Root, version)
}

func (s *Session) Prepare(name string) (*Prepared, error) {
	if name == "" {
		return &Prepared{Context: s.repo.Context}, nil
	}
	if prepared, ok := s.prepared[name]; ok {
		if prepared.err == nil {
			prepared.err = Validate(s.repo, s.config, prepared.prepared.State)
		}
		if prepared.err == nil {
			prepared.err = ValidateRuntime(s.repo, prepared.prepared.State)
		}
		return prepared.prepared, prepared.err
	}
	prepared := &Prepared{Context: s.repo.Context}
	err := s.prepare(name, prepared)
	s.prepared[name] = preparation{prepared, err}
	return prepared, err
}

func (s *Session) prepare(name string, prepared *Prepared) (err error) {
	finish := process.Phase(s.repo.Context, "environment", "prepare "+name+" from pinned candidate inputs")
	defer finish()
	definition, ok := s.config.Environments[name]
	if !ok {
		return fmt.Errorf("unknown environment %s", name)
	}
	state, err := Capture(s.repo, name, definition)
	prepared.State = state
	if err != nil {
		return err
	}
	node, err := Node(s.repo)
	if err != nil {
		return err
	}
	_, state.NodeSHA256, err = process.Executable(s.repo.Root, node)
	if err != nil {
		return err
	}
	prepared.Context = process.WithEnvironment(s.repo.Context, "PATH="+filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(prepared.Context, time.Duration(definition.TimeoutSeconds)*time.Second)
	defer cancel()
	dir, err := s.repo.OutputDirectory(filepath.Join(s.output, "environments", name))
	if err != nil {
		return err
	}
	var logs []byte
	defer func() {
		if err != nil {
			logs = append(logs, []byte("\n"+err.Error()+"\n")...)
		}
		if e := os.WriteFile(filepath.Join(dir, "preparation.log"), logs, 0600); e != nil {
			err = e
		}
	}()
	run := func(directory string, argv []string) (process.Result, error) {
		out, e := process.Run(ctx, directory, argv, nil, 4<<20)
		if len(logs)+len(out.Stdout)+len(out.Stderr) > 16<<20 {
			return out, fmt.Errorf("environment logs exceed bound")
		}
		logs = append(logs, out.Stdout...)
		logs = append(logs, out.Stderr...)
		if e != nil {
			return out, fmt.Errorf("environment %s command %q: %w; log: %s", name, argv, e, filepath.Join(dir, "preparation.log"))
		}
		return out, nil
	}
	if os.Getenv("LAND_CANDIDATE_CHECK") == "1" {
		args := []string{"python3", "scripts/qualification_toolchain.py"}
		if cache := os.Getenv("CONCAVE_CACHE_ROOT"); cache != "" {
			args = append(args, "--cache-root", cache)
		}
		out, e := run(s.repo.Root, args)
		if e != nil {
			return e
		}
		var prepared struct {
			Executables struct {
				NPM struct {
					Path string `json:"path"`
				} `json:"npm"`
			} `json:"executables"`
		}
		if e = json.Unmarshal(out.Stdout, &prepared); e != nil || prepared.Executables.NPM.Path == "" {
			return fmt.Errorf("invalid pinned npm preparation: %v", e)
		}
		if _, err = run(s.repo.Root, []string{node, prepared.Executables.NPM.Path, "ci", "--prefix", "scripts", "--ignore-scripts", "--no-audit", "--no-fund", "--userconfig=" + os.DevNull}); err != nil {
			return err
		}
		pnpm := filepath.Join(s.repo.Root, "scripts/node_modules/pnpm/bin/pnpm.mjs")
		args = []string{node, pnpm}
		for _, path := range definition.Packages {
			args = append(args, "--filter", "./"+path+"...")
		}
		args = append(args, "install", "--fail-if-no-match", "--frozen-lockfile", "--ignore-scripts", "--ignore-pnpmfile", "--prefer-offline", "--reporter=append-only", "--config.manage-package-manager-versions=false", "--config.enable-global-virtual-store=false", "--config.userconfig="+os.DevNull)
		if _, err = run(filepath.Join(s.repo.Root, definition.Directory), args); err != nil {
			return err
		}
		if err = workspaceLinks(s.repo.Root, definition); err != nil {
			return err
		}
	}
	out, err := run(s.repo.Root, []string{node, filepath.Join(s.repo.Root, "scripts/node_modules/pnpm/bin/pnpm.mjs"), "--version"})
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out.Stdout)) != state.PackageManager {
		return fmt.Errorf("pnpm version differs from scripts/package.json")
	}
	current, err := Capture(s.repo, name, definition)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Inputs, state.Inputs) {
		return &repository.Stale{Message: "environment inputs changed during preparation"}
	}
	for _, include := range definition.Includes {
		included, err := s.Prepare(include)
		if err != nil {
			return err
		}
		state.Includes = append(state.Includes, included.State)
	}
	state.SHA256, err = state.digest()
	return err
}

func (state *State) digest() (string, error) {
	copy := *state
	copy.SHA256 = ""
	return repository.JSONDigest(copy)
}

// Validate checks captured preparation inputs without installing or executing
// anything. Execution separately admits the current pinned tools.
func Validate(repo *repository.Repo, config *policy.Config, state *State) error {
	if state == nil {
		return fmt.Errorf("missing environment evidence")
	}
	digest, err := state.digest()
	if err != nil || digest != state.SHA256 || state.Recipe != 1 {
		return fmt.Errorf("invalid environment identity")
	}
	definition, ok := config.Environments[state.Name]
	if !ok || !reflect.DeepEqual(definition, state.Definition) {
		return &repository.Stale{Message: "environment definition changed: " + state.Name}
	}
	currentPolicy, err := policy.Load(repo, "")
	if err != nil {
		return err
	}
	if actual, ok := currentPolicy.Environments[state.Name]; !ok || !reflect.DeepEqual(actual, state.Definition) {
		return &repository.Stale{Message: "environment definition changed: " + state.Name}
	}
	current, err := Capture(repo, state.Name, definition)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.Inputs, state.Inputs) {
		return &repository.Stale{Message: "environment inputs or workspace membership changed: " + state.Name}
	}
	if len(state.Includes) != len(definition.Includes) {
		return fmt.Errorf("missing or unexpected included environment evidence: %s", state.Name)
	}
	for i, included := range state.Includes {
		if included == nil || included.Name != definition.Includes[i] {
			return fmt.Errorf("mismatched included environment evidence: %s", state.Name)
		}
		if err := Validate(repo, config, included); err != nil {
			return err
		}
	}
	return nil
}

// Runtime admission is separate from read-only receipt inspection. A check's
// tool list cannot accidentally omit the executable that prepared its workspace.
func ValidateRuntime(repo *repository.Repo, state *State) error {
	if state.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		return &repository.Stale{Message: "environment platform changed"}
	}
	node, err := Node(repo)
	if err != nil {
		return err
	}
	_, digest, err := process.Executable(repo.Root, node)
	if err != nil {
		return err
	}
	if digest != state.NodeSHA256 {
		return &repository.Stale{Message: "environment Node executable changed"}
	}
	for _, included := range state.Includes {
		if err := ValidateRuntime(repo, included); err != nil {
			return err
		}
	}
	return nil
}

// pnpm imports package bytes from its shared store and owns all workspace links
// in this candidate. No symlink can borrow source from another checkout.
func workspaceLinks(root string, definition policy.Environment) error {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	root = canonical
	paths := []string{filepath.Join(definition.Directory, "node_modules")}
	for _, path := range definition.Packages {
		paths = append(paths, filepath.Join(definition.Directory, path, "node_modules"))
	}
	count := 0
	for _, base := range paths {
		err := filepath.WalkDir(filepath.Join(root, base), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			count++
			if count > 200000 {
				return fmt.Errorf("dependency inventory exceeds 200000 entries")
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return nil
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("unresolved dependency link %s: %w", path, err)
			}
			rel, err := filepath.Rel(root, target)
			if err != nil {
				return err
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("workspace link escapes candidate: %s", path)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
