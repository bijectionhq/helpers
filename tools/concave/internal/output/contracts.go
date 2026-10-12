package output

import (
	"concave/internal/analysis"
	"concave/internal/policy"
	"fmt"
	"io"
	"strings"
)

func Contracts(w io.Writer, view *analysis.ContractContext, top int) {
	if view == nil {
		return
	}
	fmt.Fprintln(w, "\nBehavioral obligations (discovered; compliance not established):")
	// Bound presentation, not discovery. Keep each file's applicability while
	// showing identical witness evidence only once within that file.
	total, shown, previousPath := 0, 0, ""
	show := func(path string) bool {
		total++
		if top > 0 && shown >= top {
			return false
		}
		if path != "" && path != previousPath {
			fmt.Fprintln(w, path)
		}
		previousPath = path
		shown++
		return true
	}
	witnesses := func(path, prefix string, values []analysis.Witness) {
		seen := map[analysis.Witness]bool{}
		for _, witness := range values {
			if seen[witness] {
				continue
			}
			seen[witness] = true
			if show(path) {
				label := prefix
				if witness.Symbol == "" {
					label = strings.ReplaceAll(prefix, "witness", "source reference")
				}
				fmt.Fprintf(w, "%s %s: %s; %s\n", label, witness.Reference, witness.Resolution, executedText(witness))
			}
		}
	}
	witnesses("", "Case witness", view.Witnesses)
	for _, file := range view.Files {
		for _, obligation := range file.Obligations {
			if show(file.Path) {
				fmt.Fprintf(w, "  %s [%s] %s:%d\n    %s\n", obligation.Reference, obligation.Scope, obligation.Path, obligation.Line, view.Definitions[obligation.Reference].Text)
			}
		}
		for _, exception := range file.Exceptions {
			if !show(file.Path) {
				continue
			}
			if exception.Start > 0 {
				fmt.Fprintf(w, "  exception %s:%d-%d: %s\n", exception.ID, exception.Start, exception.End, exception.Explanation.Why)
			} else {
				fmt.Fprintf(w, "  rule %s (file context): %s\n", exception.ID, exception.Explanation.Why)
			}
			for _, reference := range exception.Explanation.Preserves {
				fmt.Fprintf(w, "    preserves: %s\n", reference)
			}
			if exception.Explanation.RemoveWhen != "" {
				fmt.Fprintf(w, "    remove when: %s\n", exception.Explanation.RemoveWhen)
			}
		}
		witnesses(file.Path, "  witness", file.Witnesses)
	}
	if view.Verification != nil {
		for _, check := range view.Verification.Checks {
			for _, id := range check.Cases {
				if show("") {
					fmt.Fprintf(w, "Required check %s · %s (not run)\n", check.Name, id)
				}
			}
		}
		for _, reference := range view.Verification.Unmapped {
			if show("") {
				if !strings.Contains(reference, "::") {
					fmt.Fprintf(w, "Source reference %s: name an executable test as path::test_symbol and map its check in concave.json.\n", reference)
				} else {
					fmt.Fprintf(w, "Unmapped witness %s: add an executable-case mapping in concave.json.\n", reference)
				}
			}
		}
	}
	if total == 0 {
		fmt.Fprintln(w, "No discovered obligations, divergence explanations or witnesses in this scope.")
	}
	if shown < total {
		fmt.Fprintf(w, "… %d more contract entries (--top 0 or --json for all)\n", total-shown)
	}
	// The whole scope's count, whatever --top displayed above.
	if s := view.WitnessSummary; s != nil && s.Total > 0 {
		fmt.Fprintf(w, "Witnesses: %d cited · %d run by a named check · %d name a symbol or test no check runs · %d source references\n", s.Total, s.Mapped, s.UnmappedWitnesses, s.SourceReferences)
	}
	for _, limit := range view.Limits {
		fmt.Fprintln(w, "  Limit:", limit)
	}
}

func ContractReview(w io.Writer, p *analysis.ContractReview, top int) {
	fmt.Fprintf(w, "Concave contract review · baseline %s\ncandidate %s\n%d changes against baseline; %d source inputs; %d reference candidates\n", p.Against, candidate(p.Candidate), len(p.Changes), len(p.Sources), len(p.Consumers))
	fmt.Fprintln(w, "Semantic review and witness execution have not run.")
	findings := append([]policy.Finding{}, p.Candidate.Findings...)
	findings = append(findings, p.CallerFindings...)
	policy.NormalizeFindings(findings)
	for i, finding := range findings {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "… %d more findings in --json\n", len(findings)-i)
			break
		}
		fmt.Fprintf(w, "%s:%d [%s] %s\n", finding.Path, finding.Line, finding.Kind, finding.Message)
	}
	for i, c := range p.Changes {
		if top > 0 && i >= top {
			fmt.Fprintf(w, "… %d more changes in --json\n", len(p.Changes)-i)
			break
		}
		fmt.Fprintf(w, "%s %s %s\n", c.Kind, c.Path, c.Reference)
		contractChangeDetails(w, c)
	}
	if p.ExcludedExceptionChanges > 0 {
		fmt.Fprintf(w, "Exception changes outside review context: %d; full policy retained.\n", p.ExcludedExceptionChanges)
	}
	contractRationales(w, p, top)
	shownObligations := 0
	definitions := map[string]bool{}
	if p.Candidate.Contracts != nil {
		for _, file := range p.Candidate.Contracts.Files {
			for _, obligation := range file.Obligations {
				if definitions[obligation.Reference] {
					continue
				}
				definitions[obligation.Reference] = true
				if top == 0 || shownObligations < top {
					fmt.Fprintf(w, "Obligation %s [%s]: %s\n", obligation.Reference, obligation.Scope, p.Candidate.Contracts.Definitions[obligation.Reference].Text)
				}
				shownObligations++
			}
		}
	}
	if top > 0 && shownObligations > top {
		fmt.Fprintf(w, "… %d more obligations in --json\n", shownObligations-top)
	}
	if p.ImportConsumers != nil {
		shown := 0
		definitions := map[string]bool{}
		for _, location := range p.CallerLocations {
			if len(location.Obligations) == 0 {
				continue
			}
			if top > 0 && shown >= top {
				fmt.Fprintln(w, "… more caller obligations in --json")
				break
			}
			shown++
			fmt.Fprintf(w, "Caller obligations at %s:%d:%d (%d source relationships):\n", location.Path, location.Line, location.Column, len(location.Origins))
			for _, reference := range location.Obligations {
				fmt.Fprintln(w, "  "+reference)
				if !definitions[reference] {
					fmt.Fprintln(w, "    "+p.CallerContext.Definitions[reference].Text)
					definitions[reference] = true
				}
			}
		}

	}
	shownWitnesses := 0
	witnesses := map[string]bool{}
	for _, side := range []struct {
		label   string
		context *analysis.ContractContext
	}{{"Review", p.Candidate.Contracts}, {"Caller", p.CallerContext}} {
		if side.context == nil {
			continue
		}
		all := append([]analysis.Witness{}, side.context.Witnesses...)
		for _, file := range side.context.Files {
			all = append(all, file.Witnesses...)
		}
		for _, witness := range all {
			key := witness.Path + "::" + witness.Symbol
			if witnesses[key] {
				continue
			}
			witnesses[key] = true
			if top == 0 || shownWitnesses < top {
				fmt.Fprintf(w, "%s witness %s: %s; %s\n", side.label, witness.Reference, witness.Resolution, executedText(witness))
			}
			shownWitnesses++
		}
	}
	if top > 0 && shownWitnesses > top {
		fmt.Fprintf(w, "… %d more witnesses in --json\n", shownWitnesses-top)
	}
	fmt.Fprintf(w, "Consumer search: %d files and %d bytes inspected; %d files and %d references omitted.\n", p.Search.ScannedFiles, p.Search.ScannedBytes, p.Search.UnscannedFiles, p.Search.OmittedReferences)
	if p.CallerCoverage != nil {
		coverage := p.CallerCoverage
		fmt.Fprintf(w, "Caller context: %d files, %d bytes and %d locations resolved; %d files and %d locations omitted; %d files and %d locations outside selected scope\n", coverage.ResolvedFiles, coverage.ResolvedBytes, coverage.ResolvedLocations, coverage.UnresolvedFiles, coverage.OmittedLocations, coverage.ExcludedFiles, coverage.ExcludedLocations)
		for i, omission := range coverage.Omissions {
			if top > 0 && i >= top {
				fmt.Fprintf(w, "… %d more omitted caller files in --json\n", len(coverage.Omissions)-i)
				break
			}
			fmt.Fprintf(w, "%s [%s]: %d locations unresolved\n", omission.Path, omission.Reason, omission.Locations)
			if omission.Detail != "" {
				fmt.Fprintln(w, "  "+omission.Detail)
			}
		}
		if top == 0 {
			CallerEvidence(w, p.ImportConsumers, 0)
		} else {
			CallerCoverage(w, p.ImportConsumers, top)
		}
	}
	for _, limit := range p.Search.Limits {
		fmt.Fprintln(w, "Limit:", limit)
	}
}

// executedText states execution only from a coverage receipt.
// The strongest evidence leads: a mutation outcome, then coverage.
func executedText(witness analysis.Witness) string {
	text := fmt.Sprintf("executed=%t", witness.Executed)
	if witness.Evidence == "historical" {
		text = fmt.Sprintf("historical observation: executed=%t; dependencies and toolchain not revalidated", witness.ObservedExecution)
	}
	if witness.Mutation != "" && witness.Mutation != "not_run" {
		text += " (mutation " + witness.Mutation + ")"
	}
	if witness.Coverage != "" && witness.Coverage != "not_run" {
		text += " (coverage " + witness.Coverage + ")"
	}
	return text
}
