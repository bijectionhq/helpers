package policy

// Span is an original-source range, zero-based and end-exclusive. Finding line
// and end_line are one-based inclusive locations in the selected source.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type Rule struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Help        string `json:"help"`
}

var Rules = []Rule{
	{"document/valid", "Published document syntax and local imports must resolve against the inspected source.", "Inspect the actual publishing source and its explicit upstream correspondence. Fix invalid MDX or missing imports; correspondence and rendered similarity never approve source differences."},
	{"source/exact", "Protected inherited files must equal upstream.", "Restore the inherited source. Put product naming at its facade or packaging owner; protected files cannot receive scoped exceptions."},
	{"source/covered", "Every inherited change must be explained independently.", "Use inspect PATH to see exact hunks, then prepare a recovery plan for reviewed hunk IDs. A required divergence needs a bounded exception with its reason and source witnesses; restoring a different file does not compensate."},
	{"transform/scoped", "Source transformations must match their declared literals, file scopes and exact edits.", "Use rule inspect ID to see actual upstream matches and rule test ID to check positive and negative fixtures. Narrow the recipe to its intended syntax and paths; preserve unrelated wire values and exception-owned spans."},
	{"derived/declared", "Generated inherited files must be declared outputs of a named generator over enforced sources.", "Use rule explain ID for the entry's generator, sources and freshness check. Regenerate with the generator instead of editing the output; explain divergence in the sources. Run the named check (verify run CHECK) to validate freshness; inspection never runs it, and a derived file is not behavioral proof."},
	{"history/restored", "Historical restorations must match exact deleted bytes, file and unchanged insertion context.", "Use inspect PATH --history to compare a retained test with the pinned deletion and its insertion context. Restore exact bytes or review the adaptation separately; a historical match is not an executed test."},
	{"exception/valid", "Exceptions must bind a valid explanation and exact original/replacement source.", "Use exception explain ID for its source binding. exception refresh ID previews current bytes without approving them. Review why the change is required and its witnesses before recording a replacement. Native source uses invariants instead of upstream exception fences."},
	{"contract/valid", "Behavioral obligations must have unique identities, valid scopes and resolved references; prose is not proof.", "Use inspect PATH:LINE --contracts for the applicable obligations. Attach each concave:invariant ID comment and its prose to an immediate declaration; resolve preserves and witness references. Structural validity does not prove the prose."},
	{"policy/changed", "Changes to policy and execution boundaries require review.", "Review the candidate policy and execution boundary changes against the selected ancestor. Adopt a new reviewed reference only after that review; changing policy cannot explain unrelated source differences."},
	{"source/readable", "Source inputs must be present and inspectable.", "Check the selector and selected revision. Source must be bounded regular UTF-8 text for text recovery; binary changes need explicit file bindings. Missing or unsupported input is an incomplete check."},
}

func NormalizeFindings(fs []Finding) {
	for i := range fs {
		f := &fs[i]
		if f.Rule != "" {
			continue
		}
		switch f.Kind {
		case "invalid_document", "missing_document_import", "missing_document_correspondence":
			f.Rule = "document/valid"
		case "exact_file_changed":
			f.Rule = "source/exact"
		case "unexplained_change", "missing_file", "mode_or_type_changed":
			f.Rule = "source/covered"
		case "stale_transform":
			f.Rule = "transform/scoped"
		case "invalid_fence", "unknown_fence", "stale_exception", "obsolete_exception", "invalid_reference":
			f.Rule = "exception/valid"
		case "policy_changed", "boundary_changed", "policy_witness_weakened":
			f.Rule = "policy/changed"
		default:
			f.Rule = "source/readable"
		}
	}
}
func (x Exception) Explanation() Explanation {
	return Explanation{Why: x.Reason, Witnesses: x.Witnesses, RemoveWhen: x.RemoveWhen, Preserves: x.Preserves}
}

// ConflictingSpans rejects shared ownership, including insertions at either
// edge of another span. Such insertions have no unambiguous ordering or owner.
func ConflictingSpans(a, b Span) bool {
	if a.Start == a.End {
		return a.Start >= b.Start && a.Start <= b.End
	}
	if b.Start == b.End {
		return b.Start >= a.Start && b.Start <= a.End
	}
	return a.Start < b.End && b.Start < a.End
}
