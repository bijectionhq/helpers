package command_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concave/internal/analysis"
)

func TestInspectionContractLimitKeepsCompleteSavedAndJSONEvidence(t *testing.T) {
	fixture := contractFixture(t)
	index, working := fixture.Git("ls-files", "--stage"), fixture.Git("diff", "--")
	path := "crates/core/adapter.rs"
	dir := filepath.Join(fixture.Root, "target/inspection")
	code, text, stderr := run(fixture, "inspect", path, "--contracts", "--top", "1", "--output", dir)
	if code != 0 || !strings.Contains(text, "2 more contract entries (--top 0 or --json for all)") || strings.Contains(text, "[family:engine]") {
		t.Fatal(code, text, stderr)
	}
	code, full, stderr := run(fixture, "inspect", path, "--contracts", "--top", "0")
	if code != 0 || !strings.Contains(full, "[family:engine]") || strings.Contains(full, "more contract entries") {
		t.Fatal(code, full, stderr)
	}
	code, located, stderr := run(fixture, "inspect", path+":3", "--top", "1")
	if code != 0 || !strings.Contains(located, "2 more contract entries") {
		t.Fatal("implicit location context ignored the limit", code, located, stderr)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "inspection.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report analysis.Report
	if err = json.Unmarshal(saved, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Contracts.Files) != 1 || len(report.Contracts.Files[0].Obligations) != 3 {
		t.Fatal("saved truncated context", report.Contracts)
	}
	for _, top := range []string{"0", "1"} {
		code, data, stderr := run(fixture, "inspect", path, "--contracts", "--top", top, "--json")
		if code != 0 {
			t.Fatal(code, data, stderr)
		}
		var current analysis.Report
		if err = json.Unmarshal([]byte(data), &current); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(report, current) {
			t.Fatal("presentation limit changed saved or JSON evidence")
		}
	}
	if fixture.Git("ls-files", "--stage") != index || fixture.Git("diff", "--") != working {
		t.Fatal("inspection mutated source or index")
	}
}
