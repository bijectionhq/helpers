package output

import (
	"fmt"
	"io"

	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/process"
)

func ExceptionExplanation(writer io.Writer, explanation *exceptions.Explanation) error {
	fmt.Fprintf(writer, "%s · %s (%s)\n", explanation.ID, explanation.Status, explanation.Source)
	if explanation.Binding.File != nil {
		fmt.Fprintf(writer, "%s · file binding\n", explanation.Binding.Path)
		if err := JSON(writer, explanation.Binding.File); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(writer, "%s upstream [%d,%d)\n", explanation.Binding.Path, explanation.Binding.Start, explanation.Binding.End)
	}
	if m := explanation.Explanation; m != nil {
		fmt.Fprintf(writer, "why: %s\n", m.Why)
		for _, w := range m.Witnesses {
			witnessReference(writer, explanation.Binding.Path, w)
		}
		for _, ref := range m.Preserves {
			fmt.Fprintf(writer, "preserves: %s\n", ref)
		}
		if m.RemoveWhen != "" {
			fmt.Fprintf(writer, "remove-when: %s\n", m.RemoveWhen)
		}
	}
	fmt.Fprintf(writer, "file classification: %s\n", explanation.FileClassification)
	if explanation.PreviousSource != nil {
		if err := previousSource(writer, explanation.PreviousSource); err != nil {
			return err
		}
	}
	for _, f := range explanation.Findings {
		fmt.Fprintf(writer, "%s:%d [%s] %s\n", f.Path, f.Line, f.Rule, f.Message)
	}
	return nil
}

func witnessReference(writer io.Writer, owner, reference string) {
	_, symbol, err := policy.WitnessTarget(owner, reference)
	if err == nil && symbol == "" {
		fmt.Fprintf(writer, "source reference: %s (no executable test; use path::test_symbol to name one)\n", reference)
		return
	}
	fmt.Fprintf(writer, "witness: %s (not executed)\n", reference)
}

// The terminal view renders the same historical evidence as JSON, with a literal
// diff so that reviewers can read changed lines directly.
func previousSource(writer io.Writer, source *exceptions.PreviousSource) error {
	if _, err := fmt.Fprintf(writer, "Previously approved source: %s; examined %d historical blobs (limit %d commits)\n", source.Status, source.Examined, source.Limit); err != nil {
		return err
	}
	if source.Status != "recovered" {
		return nil
	}
	if _, err := fmt.Fprintf(writer, "Approved at %s (blob %s)\n", source.Commit, source.Blob); err != nil {
		return err
	}
	if source.Diff == "" {
		_, err := fmt.Fprintln(writer, "Approved body is unchanged; binding validity is reported separately.")
		return err
	}
	label := "Approved → current body"
	if !source.CurrentFence {
		label = "Approved → empty body (current fence absent)"
	}
	_, err := fmt.Fprintf(writer, "%s:\n%s\n", label, source.Diff)
	return err
}

func ExceptionProposal(writer io.Writer, proposal *exceptions.Proposal, writeCommand []string) error {
	if proposal.SourcePatch != "" {
		fmt.Fprintln(writer, "Source annotation patch:")
		fmt.Fprintln(writer, proposal.SourcePatch)
	}
	for _, change := range proposal.Changes {
		fmt.Fprintf(writer, "%s · %s (%s)\n", change.ID, proposal.Status, change.Action)
		for _, old := range change.Previous {
			if old.File != nil {
				fmt.Fprintf(writer, "previous %s %s\n", old.ID, old.Path)
				if err := JSON(writer, old.File); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(writer, "previous %s %s upstream [%d,%d) replacement %s\n", old.ID, old.Path, old.Start, old.End, old.ReplacementSHA256)
			}
		}
		if next := change.Next; next != nil {
			if next.File != nil {
				fmt.Fprintf(writer, "proposed %s\n", next.Path)
				if err := JSON(writer, next.File); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(writer, "proposed %s upstream [%d,%d) replacement %s\n", next.Path, next.Start, next.End, next.ReplacementSHA256)
			}
		}
		if change.Explanation != nil {
			fmt.Fprintf(writer, "why: %s\n", change.Explanation.Why)
			path := ""
			if change.Next != nil {
				path = change.Next.Path
			} else if len(change.Previous) > 0 {
				path = change.Previous[0].Path
			}
			for _, witness := range change.Explanation.Witnesses {
				witnessReference(writer, path, witness)
			}
		}
		if change.SourceDiff != "" {
			fmt.Fprintln(writer, change.SourceDiff)
		}
		if change.PreviousSource != nil {
			if err := previousSource(writer, change.PreviousSource); err != nil {
				return err
			}
		}
		if change.Action == exceptions.Remove && change.Next == nil && change.Remaining != nil {
			if len(change.Remaining) == 0 {
				fmt.Fprintln(writer, "remaining: none; the span is upstream source or approved otherwise")
			}
			for _, finding := range change.Remaining {
				fmt.Fprintf(writer, "remaining %s:%d %s: %s\n", finding.Path, finding.Line, finding.Rule, finding.Message)
			}
			if len(change.Remaining) > 0 {
				fmt.Fprintln(writer, "Publication refuses retiring this binding until its span is restored or approved otherwise.")
			}
		}

	}
	if proposal.PolicyPatch != "" {
		fmt.Fprintln(writer, proposal.PolicyPatch)
	}
	if len(writeCommand) != 0 {
		fmt.Fprintf(writer, "proposal_sha256: %s\nWrite this proposal: %s\n", proposal.ProposalSHA256, process.ShellCommand(writeCommand))
	}
	fmt.Fprintln(writer, "Witnesses were not executed; source files and the index were not edited.")
	return nil
}
