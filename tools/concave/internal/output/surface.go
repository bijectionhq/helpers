package output

import (
	"concave/internal/surface"
	"fmt"
	"io"
	"sort"
)

func Surface(w io.Writer, report *surface.Report, top int) {
	fmt.Fprintf(w, "%s · %s · TypeScript %s\n", report.Kind, report.Status, report.Toolchain.TypeScript)
	entries := make([]string, 0, len(report.Entries))
	for path := range report.Entries {
		entries = append(entries, path)
	}
	sort.Strings(entries)
	for _, path := range entries {
		entry := report.Entries[path]
		fmt.Fprintf(w, "%s: %d exports, %d runtime exports\n", path, len(entry.Exports), len(entry.Runtime))
	}
	for i, compiler := range report.Compilers {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "… %d more compiler owners in --json\n", len(report.Compilers)-i)
			break
		}
		if compiler.Error != "" {
			fmt.Fprintf(w, "Compiler %s: unavailable; %d caller files\n", compiler.Owner, compiler.Files)
		} else {
			fmt.Fprintf(w, "Compiler %s (%s): TypeScript %s; %d caller files\n", compiler.Owner, compiler.Basis, compiler.TypeScript, compiler.Files)
		}
	}
	if report.Kind == "concave-callers" {
		CallerEvidence(w, report, top)
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(w, "%s:%d TS%d %s\n", diagnostic.Path, diagnostic.Line, diagnostic.Code, diagnostic.Message)
	}
	fmt.Fprintln(w, "Use --json to save the source-bound artifact, including all retained locations.")
}

func CallerEvidence(w io.Writer, report *surface.Report, top int) {
	coverage := report.Coverage
	CallerCoverage(w, report, top)
	fmt.Fprintf(w, "Caller evidence: %d edges; coverage complete within declared analysis: %t\n", len(report.Consumers), coverage.Complete)
	for i, consumer := range report.Consumers {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "… %d more caller edges in --json\n", len(report.Consumers)-i)
			break
		}
		fmt.Fprintf(w, "%s:%d:%d %s %s → %s; %d references, %d omitted; complete=%t\n", consumer.Path, consumer.Line, consumer.Column, consumer.Kind, consumer.Module, consumer.Entry, len(consumer.References), consumer.OmittedReferences, consumer.ReferencesComplete)
		for j, reference := range consumer.References {
			if top > 0 && j >= 3 {
				fmt.Fprintf(w, "  … %d more retained locations in --json\n", len(consumer.References)-j)
				break
			}
			fmt.Fprintf(w, "  use %s:%d:%d %s\n", consumer.Path, reference.Line, reference.Column, reference.Name)
		}
	}
}

func CallerCoverage(w io.Writer, report *surface.Report, top int) {
	coverage := report.Coverage
	fmt.Fprintf(w, "Scanned %d files; %d unscanned; %d nonliteral imports; %d parse errors; %d references omitted\n", coverage.ScannedFiles, coverage.UnscannedFiles, coverage.UnresolvedDynamic, coverage.ParseErrors, coverage.OmittedReferences)
	for i, issue := range coverage.Issues {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "… %d more retained coverage issues in --json\n", len(coverage.Issues)-i)
			break
		}
		fmt.Fprintf(w, "%s:%d:%d [%s] %s\n", issue.Path, issue.Line, issue.Column, issue.Kind, issue.Message)
	}
	if coverage.OmittedIssues > 0 {
		fmt.Fprintf(w, "%d additional coverage issues omitted by the capture bound\n", coverage.OmittedIssues)
	}
}

func SurfaceComparison(w io.Writer, report *surface.Comparison) {
	fmt.Fprintf(w, "%s\nCapture HEAD: %s → %s\nComparable: %t; basis changes: %v; %d changes\n", report.Kind, report.BeforeHead, report.AfterHead, report.Comparable, report.BasisChanges, len(report.Changes)+len(report.Callers))
	for _, change := range report.Changes {
		fmt.Fprintf(w, "%s %s: %s\n", change.Entry, change.Export, change.Kind)
	}
	for _, change := range report.Callers {
		fmt.Fprintf(w, "%s → %s: %s\n", change.Path, change.Entry, change.Kind)
	}
	if report.Coverage != nil {
		fmt.Fprintf(w, "Coverage changed: complete %t → %t; unresolved %d → %d; parse errors %d → %d; omitted references %d → %d\n", report.Coverage.Before.Complete, report.Coverage.After.Complete, report.Coverage.Before.UnresolvedDynamic, report.Coverage.After.UnresolvedDynamic, report.Coverage.Before.ParseErrors, report.Coverage.After.ParseErrors, report.Coverage.Before.OmittedReferences, report.Coverage.After.OmittedReferences)
	}
	if !report.Comparable {
		fmt.Fprintln(w, "Changes are descriptive; differing analysis bases or incomplete coverage prevent resolution claims.")
	}
}
