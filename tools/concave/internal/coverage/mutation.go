package coverage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"concave/internal/repository"
)

// Mutation outcomes. Only Witnessed establishes that the witness detects the
// fenced change: it passes on the real tree and fails on the tree where the
// fence is replaced by what it explains away.
const (
	Inconclusive         = "inconclusive"
	Witnessed            = "witnessed"
	NotWitnessing        = "not_witnessing"
	MutantDoesNotCompile = "mutant_does_not_compile"
)

// Mutation is a witness request plus the fenced file as it would read without
// the fence's divergence: the upstream span, the transformed original, or
// nothing for a pure insertion. Anchor is the binding's original anchor and
// span, which with the fenced source determine those mutated bytes.
type Mutation struct {
	Request
	Anchor  string
	Mutated []byte
}

// BindingAnchor names a binding's original: its anchor digest and span.
func BindingAnchor(upstreamSHA256 string, start, end int) string {
	return fmt.Sprintf("%s:%d:%d", upstreamSHA256, start, end)
}

func MutationKey(r Request, anchor string) string {
	return repository.Digest([]byte("mutation\x00" + Key(r) + "\x00" + anchor))
}

// LoadMutation returns the mutation receipt for exactly these inputs, if any.
func LoadMutation(root string, r Request, anchor string) *Receipt {
	key := MutationKey(r, anchor)
	data, err := os.ReadFile(filepath.Join(Directory(root), key+".json"))
	if err != nil {
		return nil
	}
	var receipt Receipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Format != 2 || receipt.Kind != "concave-witness-mutation" || receipt.Key != key {
		return nil
	}
	return &receipt
}

// Mutate runs the witness on a private copy of the working tree, then on the
// same copy with the fenced lines replaced, and saves the receipt. Each test
// run is bounded by runTimeout. A deadline is inconclusive, never a behavioral
// witness: build time, scheduling and transient resources can exhaust it.
func Mutate(ctx context.Context, o Options, m Mutation, runTimeout time.Duration) (*Receipt, error) {
	r := m.Request
	receipt := &Receipt{Format: 2, Kind: "concave-witness-mutation", Key: MutationKey(m.Request, m.Anchor), Fence: r.Fence, Path: r.Path, SHA256: repository.Digest(r.Source), CodeLines: [2]int{r.CodeStart + 1, r.CodeEnd}, Witness: r.Witness, WitnessPath: r.WitnessPath, WitnessSHA256: repository.Digest(r.WitnessSource), MutatedSHA256: repository.Digest(m.Mutated), Lines: []int{}}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	var err error
	switch {
	case r.Symbol == "":
		receipt.Outcome, receipt.Problem = Unsupported, "source reference names no executable test; use path::test_symbol to name one"
	case strings.HasSuffix(r.WitnessPath, "_test.go") && strings.HasSuffix(r.Path, ".go"):
		receipt.Runner = "go"
		err = mutateGo(ctx, o, m, runTimeout, receipt)
	case strings.HasSuffix(r.WitnessPath, ".rs") && strings.HasSuffix(r.Path, ".rs"):
		receipt.Runner = "rust"
		err = mutateRust(ctx, o, m, runTimeout, receipt)
	default:
		receipt.Outcome, receipt.Problem = Unsupported, "mutation runs support Go and Rust witnesses of Go and Rust fences"
	}
	if err != nil {
		receipt.Outcome, receipt.Problem = Failed, err.Error()
		var failed *runError
		if errors.As(err, &failed) {
			receipt.Log = failed.log
		}
	}
	receipt.Seconds = time.Since(started).Seconds()
	if err := os.MkdirAll(Directory(o.Root), 0o755); err != nil {
		return receipt, err
	}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	return receipt, os.WriteFile(filepath.Join(Directory(o.Root), receipt.Key+".json"), append(data, '\n'), 0o644)
}

// copyTree copies the tracked and untracked, unignored working files under
// prefixes into dst once per invocation.
func copyTree(ctx context.Context, root, dst string, prefixes []string) error {
	if _, err := os.Stat(filepath.Join(dst, ".copied")); err == nil {
		return nil
	}
	list, err := command(ctx, root, nil, append([]string{"git", "ls-files", "-co", "--exclude-standard", "-z", "--"}, prefixes...)...)
	if err != nil {
		return fail("git ls-files", list, err)
	}
	for _, name := range strings.Split(string(list), "\x00") {
		if name == "" {
			continue
		}
		from, to := filepath.Join(root, filepath.FromSlash(name)), filepath.Join(dst, filepath.FromSlash(name))
		info, err := os.Lstat(from)
		if os.IsNotExist(err) {
			continue // a tracked file deleted in the working tree
		}
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(from)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, to); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		in, err := os.Open(from)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err == nil {
			_, err = io.Copy(out, in)
			err = errors.Join(err, out.Close())
		}
		in.Close()
		if err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dst, ".copied"), nil, 0o644)
}

// withMutant writes the mutated bytes over the copy's fenced file for run and
// always restores the copied bytes, which must equal the inspected source.
func withMutant(tree string, m Mutation, run func() error) error {
	file := filepath.Join(tree, filepath.FromSlash(m.Path))
	original, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if !bytes.Equal(original, m.Source) {
		return &repository.Stale{Message: "fenced source changed since inspection: " + m.Path}
	}
	if err := os.WriteFile(file, m.Mutated, 0o644); err != nil {
		return err
	}
	return errors.Join(run(), os.WriteFile(file, original, 0o644))
}

func mutateRust(ctx context.Context, o Options, m Mutation, runTimeout time.Duration, receipt *Receipt) error {
	tree := filepath.Join(o.Scratch, "tree")
	if err := copyTree(ctx, o.Root, tree, []string{"Cargo.toml", "Cargo.lock", "rust-toolchain", "rust-toolchain.toml", ".cargo", "crates", "vendor"}); err != nil {
		return err
	}
	t, err := rustTarget(tree, m.Request)
	if err != nil {
		return err
	}
	flags, err := rustFlags(ctx, o.Root)
	if err != nil {
		return err
	}
	runOnce := func() (bool, []string, []byte, error) {
		executable, argv, err := buildRust(ctx, o, tree, t, flags)
		if err != nil {
			return false, argv, nil, err
		}
		runCtx, cancel := context.WithTimeout(ctx, runTimeout)
		defer cancel()
		return runRustTest(runCtx, o, tree, t, executable, nil)
	}
	return decide(ctx, tree, m, runOnce, receipt)
}

// decide runs the real copy, then the mutant, and records the outcome.
func decide(ctx context.Context, tree string, m Mutation, runOnce func() (bool, []string, []byte, error), receipt *Receipt) error {
	passed, run, out, err := runOnce()
	receipt.Command = run
	if err != nil {
		return fmt.Errorf("witness on the real tree: %w", err)
	}
	if !passed {
		return &runError{message: "the witness fails on the real tree", log: tail(out)}
	}
	return withMutant(tree, m, func() error {
		passed, _, out, err := runOnce()
		var build *runError
		switch {
		case err != nil && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			receipt.Outcome, receipt.Problem = Inconclusive, "the mutant exceeded its run deadline; no behavioral conclusion"
		case err != nil && errors.As(err, &build) && strings.HasPrefix(build.message, "build"):
			receipt.Outcome, receipt.Problem, receipt.Log = MutantDoesNotCompile, "the mutant does not compile; the witness never ran against it", build.log
		case err != nil:
			return fmt.Errorf("witness on the mutant: %w", err)
		case passed:
			receipt.Outcome, receipt.Problem = NotWitnessing, "the witness still passes with the fenced code replaced by what the fence explains away"
		default:
			receipt.Outcome, receipt.Log = Witnessed, tail(out)
		}
		return nil
	})
}

func mutateGo(ctx context.Context, o Options, m Mutation, runTimeout time.Duration, receipt *Receipt) error {
	module, err := owner(o.Root, m.WitnessPath, "go.mod")
	if err != nil {
		return err
	}
	if fence, err := owner(o.Root, m.Path, "go.mod"); err != nil || fence != module {
		return fmt.Errorf("fence %s is outside the witness module %s", m.Path, module)
	}
	tree := filepath.Join(o.Scratch, "go-"+repository.Digest([]byte(module))[:16])
	if err := copyTree(ctx, o.Root, tree, []string{module}); err != nil {
		return err
	}
	moduleDir := filepath.Join(tree, filepath.FromSlash(module))
	dir, _ := filepath.Rel(moduleDir, filepath.Join(tree, filepath.FromSlash(path.Dir(m.WitnessPath))))
	parts := strings.Split(m.Symbol, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	argv := []string{"go", "test", "-count=1", "-run", strings.Join(parts, "/"), "./" + filepath.ToSlash(dir)}
	runOnce := func() (bool, []string, []byte, error) {
		runCtx, cancel := context.WithTimeout(ctx, runTimeout)
		defer cancel()
		out, err := command(runCtx, moduleDir, nil, argv...)
		switch {
		case runCtx.Err() != nil:
			return false, argv, out, runCtx.Err()
		case bytes.Contains(out, []byte("[build failed]")) || bytes.Contains(out, []byte("[setup failed]")):
			return false, argv, out, &runError{message: "build failed", log: tail(out)}
		case bytes.Contains(out, []byte("no tests to run")):
			return false, argv, out, &runError{message: "the witness test did not run", log: tail(out)}
		case err == nil:
			return true, argv, out, nil
		case bytes.Contains(out, []byte("--- FAIL")):
			return false, argv, out, nil
		}
		return false, argv, out, fail("go test", out, err)
	}
	return decide(ctx, tree, m, runOnce, receipt)
}
