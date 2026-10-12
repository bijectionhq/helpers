package output

import (
	"fmt"
	"io"
	"strings"

	"concave/internal/analysis"
	"concave/internal/contracts"
	"concave/internal/policy"
)

// Render the already captured old/new meaning, including removed obligations.
// Source identities and policy bindings retain their complete detail in JSON.
func contractChangeDetails(w io.Writer, change analysis.ContractChange) {
	for _, side := range []struct {
		label string
		value any
	}{{"Before", change.Before}, {"After", change.After}} {
		switch value := side.value.(type) {
		case contracts.Invariant:
			fmt.Fprintf(w, "  %s %s:%d → %s\n", side.label, value.Path, value.Line, value.Declaration.Identity)
			contractProse(w, value.Prose)
		case analysis.Obligation:
			fmt.Fprintf(w, "  %s %s:%d [%s]\n", side.label, value.Path, value.Line, value.Scope)
			contractProse(w, value.Text)
		case analysis.ExceptionContext:
			fmt.Fprintf(w, "  %s exception %s at %s\n", side.label, value.ID, value.Path)
			if value.Explanation != nil {
				contractExplanation(w, *value.Explanation)
			} else {
				contractProse(w, "rationale unavailable: "+value.ExplanationUnavailable)
			}
		case policy.Explanation:
			fmt.Fprintln(w, "  "+side.label+":")
			contractExplanation(w, value)
		case []string:
			fmt.Fprintf(w, "  %s: %q\n", side.label, value)
		}
	}
}

func contractProse(w io.Writer, text string) {
	fmt.Fprintln(w, "    "+strings.ReplaceAll(text, "\n", "\n    "))
}

func contractExplanation(w io.Writer, explanation policy.Explanation) {
	contractProse(w, "why: "+explanation.Why)
	for _, reference := range explanation.Preserves {
		contractProse(w, "preserves: "+reference)
	}
	for _, reference := range explanation.Witnesses {
		contractProse(w, "witness: "+reference+" (not executed)")
	}
	if explanation.RemoveWhen != "" {
		contractProse(w, "remove-when: "+explanation.RemoveWhen)
	}
}

// A rationale explains intent, never binding validity. Keep removed baseline
// rationales visible and deduplicate overlapping selected/caller context.
func contractRationales(w io.Writer, packet *analysis.ContractReview, top int) {
	var baseline *analysis.ContractContext
	if packet.Baseline != nil {
		baseline = packet.Baseline.Contracts
	}
	contexts := []struct {
		label   string
		context *analysis.ContractContext
	}{{"Candidate", packet.Candidate.Contracts}, {"Caller", packet.CallerContext}, {"Baseline-only", baseline}}
	seen := map[string]bool{}
	count := 0
	for _, side := range contexts {
		if side.context == nil {
			continue
		}
		for _, file := range side.context.Files {
			for _, exception := range file.Exceptions {
				key := file.Path + "#" + exception.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				count++
				if top > 0 && count > top {
					continue
				}
				if count == 1 {
					fmt.Fprintln(w, "Divergence rationales (explanations, not approvals):")
				}
				fmt.Fprintf(w, "%s rationale %s", side.label, key)
				if exception.Start > 0 {
					fmt.Fprintf(w, " at %s:%d-%d", file.Path, exception.Start, exception.End)
				}
				fmt.Fprintln(w)
				contractExplanation(w, exception.Explanation)
			}
		}
	}
	if top > 0 && count > top {
		fmt.Fprintf(w, "… %d more divergence rationales in --json (or use --top 0)\n", count-top)
	}
}
