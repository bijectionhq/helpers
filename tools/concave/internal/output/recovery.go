package output

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/recovery"
)

// RecoveryPreview presents the actual patch and declared obligations. Evidence
// comes from the subsequent candidate verification, never from this summary.
func RecoveryPreview(w io.Writer, plan *recovery.Plan, report *analysis.Report, config *policy.Config, root, directory string) {
	fmt.Fprintf(w, "Recovery preview · %s\n%s\n", Count(len(plan.Files), "file"), plan.Reason)
	paths := make([]string, 0, len(plan.Files))
	for path := range plan.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		effect := plan.Files[path]
		operation := "modify"
		if effect.Before == nil {
			operation = "create"
		} else if effect.After == nil {
			operation = "delete"
		}
		fmt.Fprintf(w, "  %s %s\n", operation, path)
	}
	for _, example := range plan.Examples {
		fmt.Fprintf(w, "  Example line %d: %s — %s\n", example.Line, example.Status, example.Reason)
	}
	witnesses := map[string]bool{}
	for _, file := range report.Files {
		if _, ok := plan.Files[file.Path]; !ok {
			continue
		}
		ids := make([]string, 0, len(file.Explanations))
		for id := range file.Explanations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			explanation := file.Explanations[id]
			fmt.Fprintf(w, "Declared adaptation %s: %s\n", id, explanation.Why)
			for _, ref := range explanation.Preserves {
				fmt.Fprintf(w, "  Preserve: %s\n", ref)
			}
			for _, ref := range explanation.Witnesses {
				path, symbol, err := policy.WitnessTarget(file.Path, ref)
				if err != nil {
					continue
				}
				if symbol != "" {
					path += "::" + symbol
				}
				witnesses[path] = true
			}
		}
	}
	references := []string{}
	for reference := range witnesses {
		references = append(references, reference)
	}
	requirements := config.RequiredChecks(paths, references)
	if len(requirements.Checks) > 0 {
		fmt.Fprintln(w, "Required checks for affected source and declared adaptations (not run):")
		for _, check := range requirements.Checks {
			fmt.Fprintln(w, " ", check.Name)
			for _, id := range check.Cases {
				fmt.Fprintln(w, "    ", id)
			}
		}
	}
	for _, reference := range requirements.Unmapped {
		if !strings.Contains(reference, "::") {
			fmt.Fprintln(w, "Source reference:", reference, "· name an executable test as path::test_symbol and map its check in concave.json.")
		} else {
			fmt.Fprintln(w, "Unmapped witness:", reference, "· add an executable-case mapping in concave.json.")
		}
	}
	fmt.Fprintf(w, "Source preview: %d finding(s) remain in the proposed files.\n", len(plan.Findings))
	for _, finding := range plan.Findings {
		fmt.Fprintf(w, "  %s:%d [%s] %s\n", finding.Path, finding.Line, finding.Rule, finding.Message)
	}
	fmt.Fprintln(w, "Candidate review determines all required checks. Behavioral checks not run.")
	fmt.Fprintln(w, plan.Patch)
	fmt.Fprintln(w, "Saved:", filepath.Join(directory, "plan.json"))
	fmt.Fprintln(w, "Next:", process.ShellCommand([]string{"concave", "--repo", root, "recover", "apply", filepath.Join(directory, "plan.json")}))
}
