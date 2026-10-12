package analysis

import (
	"fmt"

	"concave/internal/continuation"
	"concave/internal/policy"
)

// PolicyPaths are the source paths whose bindings this report read: its rows.
// A full audit's rows include every bound path, since bindings name only
// inherited paths.
func (report *Report) PolicyPaths() []string {
	paths := make([]string, 0, len(report.Files))
	for _, row := range report.Files {
		paths = append(paths, row.Path)
	}
	return paths
}

// PolicyCurrent reports whether every policy input this report read is
// unchanged in now. Bindings of paths outside the report never stale it.
func (report *Report) PolicyCurrent(config, now *policy.Config) bool {
	paths := report.PolicyPaths()
	return config.ScopedSHA256(paths) == now.ScopedSHA256(paths)
}

// CheckAction derives the same continuation for terminal and JSON consumers.
// Explicit revisions remain pinned in the next inspection or rule explanation.
// In working files, a binding that no longer matches its source leads to its
// own refresh preview, since it decides every finding inside its span; an
// unfenced span binding needs a hunk ID, so inspection shows one first.
func (report *Report) CheckAction(root string, config *policy.Config) continuation.Action {
	if len(report.Findings) == 0 {
		return continuation.Action{Kind: continuation.None, Message: "Source passed: every difference in this scope is explained. Behavioral checks not run."}
	}
	for _, finding := range report.Findings {
		if report.Revision != "" || config == nil {
			break
		}
		if finding.Kind != "stale_exception" && finding.Kind != "obsolete_exception" {
			continue
		}
		for _, binding := range config.Exceptions {
			if binding.ID != finding.Exception || binding.Path != finding.Path || !binding.Fence && binding.File == nil {
				continue
			}
			next := continuation.Action{Kind: continuation.Inspect, Argv: []string{"concave", "exception", "refresh", binding.ID},
				Message: fmt.Sprintf("Exception %s no longer matches its source: restore the approved source, or review this refresh preview.", binding.ID)}
			if finding.Kind == "obsolete_exception" {
				next.Message = fmt.Sprintf("Exception %s is no longer needed: review this refresh preview, which retires it.", binding.ID)
			}
			if binding.File != nil {
				next.Argv = append(next.Argv, "--file")
			}
			return next.InRepository(root)
		}
	}
	finding := report.Findings[0]
	next := continuation.Action{Kind: continuation.Inspect, Message: "Inspect this difference and its applicable contracts."}
	_, binding := policy.BindingSource(finding.Path)
	if finding.Path == "" || finding.Path == policy.File || binding {
		next.Kind, next.Message = continuation.ExplainRule, "Inspect the policy rule and its remediation."
		next.Argv = []string{"concave", "rule", "explain", finding.Rule}
	} else {
		location := finding.Path
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		next.Argv = []string{"concave", "inspect", location, "--contracts"}
	}
	if report.Revision != "" {
		next.Argv = append(next.Argv, "--rev", report.Revision)
	}
	return next.InRepository(root)
}
