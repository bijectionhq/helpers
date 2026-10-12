package exceptions_test

import (
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/testrepo"
)

// The crates/database/src/token.rs case: rustfmt separates a fenced item from
// its neighbors with a blank line on each side, where upstream had one blank
// line. Git aligns upstream's blank line before the fence, so the inserted hunk
// ends with the blank line after the end marker. The fence owns that blank
// line; recording, refreshing and checking agree.
func TestFenceAbsorbsFormatterBlankLinesAtItsEdges(t *testing.T) {
	upstream := "impl T {\n    pub fn a(&self) {\n        one();\n    }\n\n    pub fn next(&self) {\n        two();\n    }\n}\n"
	fenced := "impl T {\n    pub fn a(&self) {\n        one();\n    }\n\n    // concave:begin owned\n    // why: keep it\n    // witness: witness.rs\n    //\n    pub fn inserted(&self) {\n        three();\n    }\n\n    // concave:end owned\n\n    pub fn next(&self) {\n        two();\n    }\n}\n"
	for name, source := range map[string]string{
		"blank after end":    fenced,
		"blank before begin": "impl T {\n    pub fn a(&self) {\n        one();\n    }\n\n\n    // concave:begin owned\n    // why: keep it\n    // witness: witness.rs\n    //\n    pub fn inserted(&self) {\n        three();\n    }\n    // concave:end owned\n    pub fn next(&self) {\n        two();\n    }\n}\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := testrepo.New(t)
			f.Write(path, upstream)
			f.Write("witness.rs", "fn witness() {}\n")
			f.Config.Upstream = f.Commit()
			f.Save()
			f.Commit()
			f.Write(path, source)
			r, c := f.Open()
			p, err := exceptions.Prepare(r, c, exceptions.Record, []string{"owned"}, exceptions.Options{})
			if err != nil {
				t.Fatal("formatter blank line at a fence edge refused:", err)
			}
			if err := exceptions.Write(r, c, p); err != nil {
				t.Fatal(err)
			}
			r, c = f.Open()
			report, err := analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Findings) != 0 || report.Files[0].Classification != "excepted" {
				t.Fatalf("fence with absorbed blank lines not excepted: %+v %+v", report.Files[0], report.Findings)
			}
			// A code line beside the marker is never absorbed.
			f.Write(path, source+"fn extra() {}\n")
			r, c = f.Open()
			report, err = analysis.Analyze(r, c, analysis.Options{Paths: []string{path}})
			if err != nil || len(report.Findings) == 0 {
				t.Fatal("code outside the fence absorbed", err)
			}
		})
	}
}
