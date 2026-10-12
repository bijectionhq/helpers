// Package coverage runs one fence witness with line coverage and decides
// whether the fenced code executed. It is opt-in evidence: a receipt binds the
// exact fence and witness bytes it measured, and a missing tool, an unsupported
// witness or a failing test never counts as executed.
package coverage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"concave/internal/repository"
)

// Outcomes. Only Executed establishes that the witness ran fenced code.
const (
	Executed        = "executed"         // an instrumented fenced line ran
	NotExecuted     = "not_executed"     // instrumented fenced lines, none ran
	NotInstrumented = "not_instrumented" // the fenced lines hold no instrumented code
	Failed          = "failed"           // the build or the test failed
	Unsupported     = "unsupported"      // no coverage runner for this witness
	Unavailable     = "unavailable"      // the pinned coverage tool is missing
)

// Request names one fence and one of its test witnesses, with the bytes of
// both files as inspected. CodeStart and CodeEnd are zero-based, end-exclusive.
type Request struct {
	Fence         string
	Path          string
	Source        []byte
	CodeStart     int
	CodeEnd       int
	Witness       string
	WitnessPath   string
	Symbol        string
	WitnessSource []byte
}

// Receipt is one measurement. Lines are the one-based fenced lines that ran.
type Receipt struct {
	Format        int      `json:"format"`
	Kind          string   `json:"kind"`
	Key           string   `json:"key"`
	Fence         string   `json:"fence"`
	Path          string   `json:"path"`
	SHA256        string   `json:"sha256"`
	CodeLines     [2]int   `json:"code_lines"`
	Witness       string   `json:"witness"`
	WitnessPath   string   `json:"witness_path"`
	WitnessSHA256 string   `json:"witness_sha256"`
	MutatedSHA256 string   `json:"mutated_sha256,omitempty"`
	Runner        string   `json:"runner"`
	Command       []string `json:"command,omitempty"`
	Outcome       string   `json:"outcome"`
	Instrumented  int      `json:"instrumented_lines"`
	Lines         []int    `json:"executed_lines"`
	Problem       string   `json:"problem,omitempty"`
	Log           string   `json:"log,omitempty"`
	Seconds       float64  `json:"seconds"`
}

// Key binds the fence, its file bytes, the witness and the witness file bytes.
// Dependencies of the test are not bound: a receipt is evidence about these
// sources at measurement time, not a cache that proves later runs.
func Key(r Request) string {
	b, _ := json.Marshal([]any{r.Fence, r.Path, repository.Digest(r.Source), r.CodeStart, r.CodeEnd, r.Witness, r.WitnessPath, repository.Digest(r.WitnessSource)})
	return repository.Digest(b)
}

// Directory holds receipts under the ignored target tree.
func Directory(root string) string { return filepath.Join(root, "target", "concave", "witnesses") }

// Load returns the receipt measured for exactly these bytes, if any.
func Load(root string, r Request) *Receipt {
	data, err := os.ReadFile(filepath.Join(Directory(root), Key(r)+".json"))
	if err != nil {
		return nil
	}
	var receipt Receipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Format != 2 || receipt.Kind != "concave-witness-coverage" || receipt.Key != Key(r) {
		return nil
	}
	return &receipt
}

// Options bound one invocation's runs.
type Options struct {
	Root    string
	Scratch string // private build and profile directory, removed by the caller
	Timeout time.Duration
}

// Run measures one witness and saves its receipt.
func Run(ctx context.Context, o Options, r Request) (*Receipt, error) {
	receipt := &Receipt{Format: 2, Kind: "concave-witness-coverage", Key: Key(r), Fence: r.Fence, Path: r.Path, SHA256: repository.Digest(r.Source), CodeLines: [2]int{r.CodeStart + 1, r.CodeEnd}, Witness: r.Witness, WitnessPath: r.WitnessPath, WitnessSHA256: repository.Digest(r.WitnessSource), Lines: []int{}}
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	var counts map[int]int64
	var err error
	switch {
	case r.Symbol == "":
		receipt.Outcome, receipt.Problem = Unsupported, "source reference names no executable test; use path::test_symbol to name one"
	case strings.HasSuffix(r.WitnessPath, "_test.go") && strings.HasSuffix(r.Path, ".go"):
		receipt.Runner = "go"
		counts, receipt.Command, err = runGo(ctx, o, r)
	case strings.HasSuffix(r.WitnessPath, ".rs") && strings.HasSuffix(r.Path, ".rs"):
		receipt.Runner = "rust"
		counts, receipt.Command, err = runRust(ctx, o, r)
	default:
		receipt.Outcome, receipt.Problem = Unsupported, "coverage runs support Go and Rust witnesses of Go and Rust fences"
	}
	if err != nil {
		var unavailable *unavailableError
		receipt.Outcome, receipt.Problem = Failed, err.Error()
		if ok := asUnavailable(err, &unavailable); ok {
			receipt.Outcome = Unavailable
		}
		if failed, ok := err.(*runError); ok {
			receipt.Log = failed.log
		}
	} else if receipt.Outcome == "" {
		lines := []int{}
		for line, count := range counts {
			if line >= r.CodeStart+1 && line <= r.CodeEnd {
				receipt.Instrumented++
				if count > 0 {
					lines = append(lines, line)
				}
			}
		}
		sort.Ints(lines)
		receipt.Lines = lines
		switch {
		case len(lines) > 0:
			receipt.Outcome = Executed
		case receipt.Instrumented > 0:
			receipt.Outcome = NotExecuted
		default:
			receipt.Outcome = NotInstrumented
		}
	}
	receipt.Seconds = time.Since(started).Seconds()
	if err := os.MkdirAll(Directory(o.Root), 0o755); err != nil {
		return receipt, err
	}
	data, _ := json.MarshalIndent(receipt, "", "  ")
	if err := os.WriteFile(filepath.Join(Directory(o.Root), receipt.Key+".json"), append(data, '\n'), 0o644); err != nil {
		return receipt, err
	}
	return receipt, nil
}

type unavailableError struct{ message string }

func (e *unavailableError) Error() string { return e.message }
func asUnavailable(err error, target **unavailableError) bool {
	u, ok := err.(*unavailableError)
	if ok {
		*target = u
	}
	return ok
}

// runError keeps the bounded tail of a failed command's output.
type runError struct {
	message string
	log     string
}

func (e *runError) Error() string { return e.message }

func tail(b []byte) string {
	if len(b) > 4096 {
		b = b[len(b)-4096:]
	}
	return string(b)
}

func fail(what string, out []byte, err error) error {
	return &runError{message: fmt.Sprintf("%s: %v", what, err), log: tail(out)}
}
