package command_test

import (
	"encoding/json"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/testrepo"
)

func TestStaleFenceDiagnosticsRetainBoundaryAndSpanCauses(t *testing.T) {
	for _, scenario := range []string{"start-crossing", "end-crossing", "moved-span"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := testrepo.New(t)
			path := "crates/core/lib.rs"
			original := "fn first() {}\nfn second() {}\n"
			body := "fn first() { changed(); }\n"
			if scenario == "moved-span" {
				body = "fn second() { changed(); }\n"
			}
			fence := "// concave:begin read\n// why: Keep read ordering.\n// witness: " + path + "\n" + body + "// concave:end read\n"
			source := fence + "fn second() {}\n"
			switch scenario {
			case "start-crossing":
				source = "fn added() {}\n" + source
			case "end-crossing":
				source = fence + "fn added() {}\nfn second() {}\n"
			case "moved-span":
				source = "fn first() {}\n" + fence
			}
			annotations, invalid := policy.Annotations(path, source)
			if len(invalid) != 0 {
				t.Fatal(invalid)
			}
			annotation := annotations["read"]
			fixture.Config.Exceptions = []policy.Exception{{ID: "read", Path: path, Fence: true, Start: 0, End: 1, UpstreamSHA256: repository.Digest([]byte(original)), ReplacementSHA256: annotation.CodeSHA256()}}
			fixture.Write(path, source)
			fixture.Save()
			revision := fixture.Commit()
			fixture.Write(path, "fn dirty() {}\n")
			before, index := fixture.Git("diff", "--"), fixture.Git("ls-files", "--stage")
			code, text, stderr := run(fixture, "inspect", path, "--rev", revision, "--json")
			var report analysis.Report
			if code != 0 || json.Unmarshal([]byte(text), &report) != nil {
				t.Fatal(code, text, stderr)
			}
			var finding policy.Finding
			for _, candidate := range report.Findings {
				if candidate.Kind == "stale_exception" {
					finding = candidate
				}
			}
			if finding.Exception != "read" || finding.Rule != "exception/valid" || finding.Line != annotation.Start+1 || finding.EndLine != annotation.End || finding.Upstream == nil || *finding.Upstream != (policy.Span{Start: 0, End: 1}) {
				t.Fatal("binding identity or location changed", finding)
			}
			if scenario == "moved-span" {
				if !strings.Contains(finding.Message, "fence maps to upstream [1,2), recorded [0,1)") {
					t.Fatal(finding)
				}
			} else {
				if len(report.Hunks) != 1 || !strings.Contains(finding.Message, "change crosses fence boundary: "+path+":") || !strings.Contains(finding.Message, "hunk "+report.Hunks[0].ID+", fence read") || !strings.Contains(finding.Message, "place the marker outside this complete hunk") {
					t.Fatal("crossing-hunk identity lost", finding, report.Hunks)
				}
			}
			for _, args := range [][]string{{"check", path, "--rev", revision}, {"check", path, "--rev", revision, "--rdjson"}} {
				code, text, stderr = run(fixture, args...)
				if code != 1 || !strings.Contains(text, finding.Message) {
					t.Fatal("check lost the cause or accepted the binding", code, text, stderr)
				}
			}
			code, text, stderr = run(fixture, "exception", "explain", "read", "--rev", revision, "--json")
			var explanations []*exceptions.Explanation
			if code != 0 || json.Unmarshal([]byte(text), &explanations) != nil || len(explanations) != 1 || explanations[0].Status != "stale" || len(explanations[0].Findings) != 1 || explanations[0].Findings[0].Message != finding.Message {
				t.Fatal(code, text, stderr)
			}
			code, text, stderr = run(fixture, "inspect", "queue", "--contracts", path, "--rev", revision, "--against", revision)
			if code != 0 || !strings.Contains(text, finding.Message) {
				t.Fatal("review lost the cause", code, text, stderr)
			}
			if fixture.Git("diff", "--") != before || fixture.Git("ls-files", "--stage") != index {
				t.Fatal("diagnosis edited source, policy or index")
			}
		})
	}
}
