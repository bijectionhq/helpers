package command_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/surface"
)

// A small fixed workflow records work, not a wall-clock acceptance threshold.
// A scoped review still verifies the complete caller capture and its inventory.
func TestNarrowCallerReviewWork(t *testing.T) {
	fixture := contractFixture(t)
	modules, err := filepath.Abs("../../testdata/typescript-legacy/node_modules")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(modules, filepath.Join(fixture.Root, "node_modules")); err != nil {
		t.Fatal(err)
	}
	fixture.Write(".gitignore", "target/\nnode_modules\n")
	entry := "npm-packages/convex/index.ts"
	fixture.Write(entry, "// concave:invariant request-identity\n// Preserve request identity.\nexport function request() { return 1; }\n")
	for index := 0; index < 16; index++ {
		fixture.Write(fmt.Sprintf("npm-packages/callers/use%02d.ts", index), "import { request } from 'sdk';\nexport function use() { return request(); }\n")
	}
	fixture.Commit()
	code, capture, stderr := run(fixture, "surface", "callers", entry, "--module", "sdk="+entry, "--json", "--timings")
	if code != 0 {
		t.Fatal(code, capture, stderr)
	}
	var captured surface.Report
	if err := json.Unmarshal([]byte(capture), &captured); err != nil {
		t.Fatal(err)
	}
	t.Log("capture:", strings.TrimSpace(stderr))
	fixture.Write("target/callers.json", capture)
	artifact := filepath.Join(fixture.Root, "target/callers.json")
	caller := "npm-packages/callers/use00.ts"
	args := []string{"inspect", "queue", "--contracts", entry, "--callers", artifact, "--caller-path", caller, "--json", "--timings"}
	var first string
	for attempt := 0; attempt < 2; attempt++ {
		code, output, stderr := run(fixture, args...)
		if code != 0 {
			t.Fatal(code, output, stderr)
		}
		var packet analysis.ContractReview
		if err := json.Unmarshal([]byte(output), &packet); err != nil {
			t.Fatal(err)
		}
		coverage := packet.CallerCoverage
		if coverage == nil || coverage.TotalFiles != 16 || coverage.ResolvedFiles != 1 || coverage.ExcludedFiles != 15 || coverage.OmittedLocations != 0 || coverage.ResolvedLocations != 2 {
			t.Fatal("incomplete scoped review", coverage)
		}
		if packet.Search.ScannedFiles == 0 || len(packet.Sources) == 0 {
			t.Fatal("missing lexical or source evidence")
		}
		if attempt == 0 {
			first = output
		} else if output != first {
			t.Fatal("unchanged workflow produced different evidence")
		}
		var elapsed string
		var processes, reads, blobs int
		_, counters, found := strings.Cut(stderr, "elapsed=")
		if !found {
			t.Fatal("missing aggregate timings", stderr)
		}
		if count, err := fmt.Sscanf("elapsed="+counters, "elapsed=%s git_processes=%d source_reads=%d blob_reads=%d", &elapsed, &processes, &reads, &blobs); err != nil || count != 4 {
			t.Fatal("missing work counters", stderr, err)
		}
		// Tool installation changes the required manifest size. Bound only the
		// review's overhead above one complete caller-input sweep, never time.
		overhead := reads - len(captured.Inputs)
		if processes > 32 || overhead > 64 {
			t.Fatal("repeated review work", processes, overhead)
		}
		t.Logf("review %d: %s; manifest_inputs=%d review_overhead_reads=%d", attempt+1, strings.TrimSpace(stderr), len(captured.Inputs), overhead)
	}
	fixture.Write("npm-packages/callers/use15.ts", "export const changed = 1;\n")
	if code, _, stderr := run(fixture, args...); code != 3 || !strings.Contains(stderr, "npm-packages/callers/use15.ts") {
		t.Fatal("excluded caller escaped full capture validation", code, stderr)
	}
}
