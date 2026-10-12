// Package verification executes only explicitly selected repository-owned checks.
// Annotation prose and witness references never become commands.
package verification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"concave/internal/environment"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type Tool struct {
	Command    []string `json:"command"`
	Version    string   `json:"version"`
	Executable string   `json:"executable"`
	SHA256     string   `json:"sha256"`
}
type Status string

const ReceiptFormat = 6

const (
	Running Status = "running"
	Passed  Status = "passed"
	Failed  Status = "failed"
	Stale   Status = "stale"
)

type Problem struct {
	Code    string `json:"code"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

type Receipt struct {
	Format           int                    `json:"format"`
	Kind             string                 `json:"kind"`
	Status           Status                 `json:"status"`
	Check            string                 `json:"check"`
	Head             string                 `json:"head"`
	SourceRoot       string                 `json:"source_root"`
	Definition       policy.CheckDefinition `json:"definition"`
	Environment      *environment.State     `json:"environment,omitempty"`
	Inputs           repository.Inputs      `json:"inputs"`
	Toolchain        []Tool                 `json:"toolchain"`
	ExitCode         int                    `json:"exit_code"`
	Results          Results                `json:"results"`
	Phase            string                 `json:"phase"`
	ExecutionStarted bool                   `json:"execution_started"`
	Problems         []Problem              `json:"problems"`
	StdoutSHA256     string                 `json:"stdout_sha256"`
	StderrSHA256     string                 `json:"stderr_sha256"`
	ElapsedMS        int64                  `json:"elapsed_ms"`
	Started          string                 `json:"started"`
	EvidenceSHA256   string                 `json:"evidence_sha256"`
	ReceiptSHA256    string                 `json:"receipt_sha256"`
	Limits           []string               `json:"limits"`
}

func Inputs(repo *repository.Repo, check policy.CheckDefinition) (repository.Inputs, error) {
	required := []string{}
	for _, tool := range check.Toolchain {
		if tool.VersionFile != "" {
			required = append(required, tool.VersionFile)
		}
	}
	return repo.CaptureInputs(check.Inputs, required)
}

// Compare captured inventories without rereading source. Diagnostics retain strict
// state and membership changes, with deterministic, bounded path output.
func inputChanges(before, after repository.Inputs) string {
	paths := []string{}
	for path, state := range before {
		current, present := after[path]
		if !present || !repository.Same(state, current) {
			paths = append(paths, path)
		}
	}
	for path := range after {
		if _, present := before[path]; !present {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	var detail strings.Builder
	fmt.Fprintf(&detail, " (%d):", len(paths))
	const limit = 20
	for _, path := range paths[:min(len(paths), limit)] {
		previous, wasPresent := before[path]
		current, isPresent := after[path]
		change := "changed"
		switch {
		case !wasPresent || previous == nil && current != nil:
			change = "added"
		case !isPresent || previous != nil && current == nil:
			change = "removed"
		}
		fmt.Fprintf(&detail, "\n  %s %q", change, path)
	}
	if len(paths) > limit {
		fmt.Fprintf(&detail, "\n  … %d more changed inputs", len(paths)-limit)
	}
	return detail.String()
}

// ExecutionContext applies declared settings to probes and the runner alike.
func ExecutionContext(ctx context.Context, check policy.CheckDefinition) context.Context {
	keys := []string{}
	for key := range check.Variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ctx = process.WithEnvironment(ctx, key+"="+check.Variables[key])
	}
	return ctx
}

func tools(repo *repository.Repo, check policy.CheckDefinition) ([]Tool, error) {
	repo = repo.WithContext(ExecutionContext(repo.Context, check))
	defer repo.Close()
	result := []Tool{}
	dir := filepath.Join(repo.Root, check.Directory)
	for _, tool := range check.Toolchain {
		want := tool.Expected
		if tool.VersionFile != "" {
			state, content, err := repo.Snapshot(tool.VersionFile)
			if err != nil {
				return nil, err
			}
			if state == nil || state.Kind != "file" || !repository.Text(content) {
				return nil, fmt.Errorf("missing tool version file")
			}
			want = strings.TrimSpace(string(content))
		}
		command := append([]string{}, tool.Command...)
		// The structural engine has one selection owner, including in a disposable
		// candidate checkout. Bind the actual selected executable to this receipt.
		if command[0] == "go" {
			ctx, cancel := context.WithTimeout(repo.Context, 10*time.Second)
			out, err := process.Run(ctx, dir, []string{"go", "env", "GOROOT"}, nil, 4096)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("select Go runtime: %w", err)
			}
			root := strings.TrimSpace(string(out.Stdout))
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("Go returned a nonabsolute runtime root")
			}
			command[0] = filepath.Join(root, "bin", "go")
		}
		if command[0] == "node" && tool.VersionFile == ".nvmrc" {
			executable, err := environment.Node(repo)
			if err != nil {
				return nil, err
			}
			command[0] = executable
		}
		if command[0] == "ast-grep" {
			executable, err := syntax.New(repo.Context, repo.Root).Executable()
			if err != nil {
				return nil, err
			}
			command[0] = executable
		}
		executable, digest, err := process.ExecutableIn(repo.Context, dir, command[0])
		if err != nil {
			return nil, fmt.Errorf("required tool %q is unavailable; make the executable reporting %q available on PATH: %w", tool.Command[0], want, err)
		}
		ctx, cancel := context.WithTimeout(repo.Context, 10*time.Second)
		out, err := process.Run(ctx, dir, command, nil, 1<<20)
		cancel()
		if err != nil {
			detail := strings.TrimSpace(string(out.Stderr))
			if len(detail) > 4096 {
				detail = detail[:4096]
			}
			return nil, fmt.Errorf("tool version probe %q failed: %w\n%s", tool.Command, err, detail)
		}
		version := strings.TrimSpace(string(out.Stdout))
		if want != "" && version != want {
			return nil, fmt.Errorf("tool %s reports %q; expected %q; select the required executable on PATH", tool.Command[0], version, want)
		}
		result = append(result, Tool{tool.Command, version, executable, digest})
	}
	return result, nil
}
func (receipt *Receipt) seal() error {
	// Timing and exact log bytes remain observations, outside evidence identity.
	var err error
	receipt.EvidenceSHA256, err = repository.JSONDigest([]any{receipt.Check, receipt.Definition, receipt.Environment, receipt.Inputs, receipt.Toolchain, receipt.ExitCode, receipt.Results.evidence(), receipt.Status, receipt.Phase, receipt.ExecutionStarted, receipt.Problems})
	if err != nil {
		return err
	}
	receipt.ReceiptSHA256, err = receipt.digest()
	return err
}
func (receipt *Receipt) digest() (string, error) {
	content := *receipt
	content.ReceiptSHA256 = ""
	return repository.JSONDigest(content)
}

// Only this named execution definition is implicit evidence. A check that reads
// policy or transformation files must list them in its source inputs.
func verifyDefinition(repo *repository.Repo, name string, expected policy.CheckDefinition) error {
	config, err := policy.Load(repo, "")
	if err != nil {
		return err
	}
	actual, ok := config.Checks[name]
	if !ok || !reflect.DeepEqual(actual, expected) {
		return &repository.Stale{Message: "check definition changed: " + name}
	}
	return nil
}

func Run(repo *repository.Repo, config *policy.Config, name, output string) (*Receipt, error) {
	return RunPrepared(repo, config, environment.New(repo, config, output), name, output)
}

func RunPrepared(repo *repository.Repo, config *policy.Config, session *environment.Session, name, output string) (receipt *Receipt, err error) {
	finish := process.Phase(repo.Context, "check", name)
	defer finish()
	check, ok := config.Checks[name]
	if !ok {
		return nil, fmt.Errorf("unknown named check %q", name)
	}
	dir, err := repo.OutputDirectory(output)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	receipt = &Receipt{Format: ReceiptFormat, Kind: "concave-receipt", Status: Running, Phase: "preparation", Check: name, Definition: check, SourceRoot: repo.Root, ExitCode: -1, Started: started.UTC().Format(time.RFC3339Nano), Problems: []Problem{}, Limits: []string{"Only the declared source selectors and version probes are bound; this is not a hermetic build or proof of every transitive runtime dependency.", "Test outcomes are the selected runner's report. Witness annotations remain references and cannot authorize execution."}}
	var stdout, stderr []byte
	if err := receipt.save(dir, stdout, stderr); err != nil {
		return nil, err
	}
	defer func() {
		receipt.ElapsedMS = time.Since(started).Milliseconds()
		if saveErr := receipt.save(dir, stdout, stderr); saveErr != nil {
			err = saveErr
		}
	}()
	fail := func(code string, cause error) (*Receipt, error) {
		if repo.Context.Err() == context.Canceled {
			code = "cancelled"
		}
		if repo.Context.Err() == context.DeadlineExceeded {
			code = "timed_out"
		}
		receipt.problem(code, cause.Error())
		if repository.IsStale(cause) {
			receipt.Status = Stale
		}
		return receipt, nil
	}
	before, err := Inputs(repo, check)
	if err != nil {
		return fail("inputs_unavailable", err)
	}
	receipt.Inputs = before
	if err := verifyDefinition(repo, name, check); err != nil {
		return fail("definition_changed", err)
	}
	receipt.Head, err = repo.Resolve("HEAD")
	if err != nil {
		return fail("source_unavailable", err)
	}
	prepared, err := session.Prepare(check.Environment)
	if prepared != nil {
		receipt.Environment = prepared.State
	}
	if err != nil {
		return fail("environment_failed", err)
	}
	repo = repo.WithContext(ExecutionContext(prepared.Context, check))
	defer repo.Close()
	versions, err := tools(repo, check)
	if err != nil {
		return fail("tool_unavailable", err)
	}
	receipt.Toolchain = versions
	// Run the attested executable. Subprocesses using Go also select this runtime.
	command := append([]string{}, check.Command...)
	for _, tool := range versions {
		if command[0] == tool.Command[0] {
			command[0] = tool.Executable
		}
		if tool.Command[0] == "go" {
			repo = repo.WithContext(process.WithPath(repo.Context, filepath.Dir(tool.Executable)))
			defer repo.Close()
		}
	}
	receipt.Phase = "execution"
	if err := receipt.save(dir, stdout, stderr); err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(repo.Context, time.Duration(check.TimeoutSeconds)*time.Second)
	result, runErr := process.Run(ctx, filepath.Join(repo.Root, check.Directory), command, nil, 16<<20)
	contextErr := ctx.Err()
	cancel()
	receipt.ExitCode = result.ExitCode
	receipt.ExecutionStarted = result.Started
	stdout, stderr = result.Stdout, result.Stderr
	if runErr != nil {
		code := "command_failed"
		if contextErr == context.DeadlineExceeded {
			code = "timed_out"
		}
		if contextErr == context.Canceled {
			code = "cancelled"
		}
		receipt.problem(code, runErr.Error())
	}
	receipt.Results, err = checkResults(check, result.Stdout, repo.Root)
	if err != nil {
		code := "invalid_results"
		if errors.Is(err, ErrTestsFailed) {
			code = "tests_failed"
		}
		receipt.problem(code, err.Error())
	}
	if contextErr != nil {
		// A cancelled execution has failed, regardless of whether source would
		// still match. Do not misclassify cancelled validation probes as drift.
		return receipt, nil
	}
	receipt.Phase = "validation"
	after, err := Inputs(repo, check)
	if err != nil {
		receipt.problem("inputs_unavailable", err.Error())
		if repository.IsStale(err) {
			receipt.Status = Stale
		}
	} else if changes := inputChanges(before, after); changes != "" {
		receipt.problem("inputs_changed", "declared source inputs changed during execution"+changes)
		receipt.Status = Stale
	}
	if err = verifyDefinition(repo, name, check); err != nil {
		receipt.problem("definition_changed", err.Error())
		receipt.Status = Stale
	}
	// HEAD is an observation, not declared source identity. A shared-checkout
	// synchronization which preserves every bound byte must not invalidate a run.
	currentTools, e := tools(repo, check)
	if e != nil || !reflect.DeepEqual(versions, currentTools) {
		receipt.problem("tools_changed", "toolchain changed during execution")
		receipt.Status = Stale
	}
	if receipt.Environment != nil {
		if err := environment.Validate(repo, config, receipt.Environment); err != nil {
			receipt.problem("environment_changed", err.Error())
			receipt.Status = Stale
		} else if err := environment.ValidateRuntime(repo, receipt.Environment); err != nil {
			receipt.problem("environment_changed", err.Error())
			receipt.Status = Stale
		}
	}
	if len(receipt.Problems) == 0 && result.ExitCode == 0 {
		receipt.Status = Passed
		receipt.Phase = "complete"
	} else if len(receipt.Problems) > 0 {
		receipt.Phase = receipt.Problems[0].Phase
	}
	return receipt, nil
}

func (receipt *Receipt) problem(code, message string) {
	receipt.Problems = append(receipt.Problems, Problem{Code: code, Phase: receipt.Phase, Message: message})
	if receipt.Status != Stale {
		receipt.Status = Failed
	}
}

func (receipt *Receipt) save(dir string, stdout, stderr []byte) error {
	receipt.StdoutSHA256 = repository.Digest(stdout)
	receipt.StderrSHA256 = repository.Digest(stderr)
	if err := receipt.seal(); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"stdout.log": stdout, "stderr.log": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	// Only a complete JSON file replaces the previous interrupted-attempt record.
	path := filepath.Join(dir, "receipt.json")
	if err = os.WriteFile(path+".tmp", append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func Validate(repo *repository.Repo, config *policy.Config, receipt *Receipt) error {
	if err := ValidateRecorded(repo, config, receipt); err != nil {
		return err
	}
	versions, err := tools(repo, receipt.Definition)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(versions, receipt.Toolchain) {
		return &repository.Stale{Message: "receipt toolchain changed"}
	}
	if receipt.Environment != nil {
		return environment.ValidateRuntime(repo, receipt.Environment)
	}
	return nil
}

// ValidateRecorded inspects source-bound evidence without provisioning or probing
// an execution environment. Reuse additionally calls Validate after preparation.
func ValidateRecorded(repo *repository.Repo, config *policy.Config, receipt *Receipt) error {
	if receipt.Format != ReceiptFormat {
		return fmt.Errorf("unsupported verification receipt format %d (expected %d); rerun verification", receipt.Format, ReceiptFormat)
	}
	digest, err := receipt.digest()
	if err != nil || receipt.Kind != "concave-receipt" || receipt.ReceiptSHA256 != digest {
		return fmt.Errorf("invalid verification receipt")
	}
	if receipt.Status == Stale {
		return &repository.Stale{Message: "verification receipt is stale"}
	}
	if receipt.Status != Passed {
		return fmt.Errorf("receipt did not pass: %s", receipt.Status)
	}
	if receipt.ExitCode != 0 || !receipt.ExecutionStarted || receipt.Phase != "complete" || len(receipt.Problems) != 0 || receipt.Results.Failed != 0 {
		return fmt.Errorf("inconsistent successful receipt")
	}
	if err := ValidateWitnesses(receipt.Definition, receipt.Results); err != nil {
		return err
	}
	check, ok := config.Checks[receipt.Check]
	if !ok || !reflect.DeepEqual(check, receipt.Definition) {
		return &repository.Stale{Message: "check definition changed: " + receipt.Check}
	}
	if err := verifyDefinition(repo, receipt.Check, receipt.Definition); err != nil {
		return err
	}
	now, err := Inputs(repo, check)
	if err != nil {
		return err
	}
	if changes := inputChanges(receipt.Inputs, now); changes != "" {
		return &repository.Stale{Message: "receipt source inputs changed" + changes}
	}
	if check.Environment != "" {
		if receipt.Environment == nil || receipt.Environment.Name != check.Environment {
			return fmt.Errorf("missing or mismatched environment evidence")
		}
		if err := environment.Validate(repo, config, receipt.Environment); err != nil {
			return err
		}
	} else if receipt.Environment != nil {
		return fmt.Errorf("unexpected environment evidence")
	}
	return nil
}
func Names(config *policy.Config) []string {
	names := []string{}
	for n := range config.Checks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func ValidateLogs(dir string, receipt *Receipt) error {
	for name, want := range map[string]string{"stdout.log": receipt.StdoutSHA256, "stderr.log": receipt.StderrSHA256} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(b) > 16<<20 || repository.Digest(b) != want {
			return &repository.Stale{Message: "verification log changed: " + name}
		}
		if name == "stdout.log" && receipt.ExecutionStarted {
			results, err := checkResults(receipt.Definition, b, receipt.SourceRoot)
			if err != nil || !reflect.DeepEqual(results, receipt.Results) {
				return fmt.Errorf("receipt outcomes differ from test log: %v", err)
			}
		}
	}
	return nil
}
