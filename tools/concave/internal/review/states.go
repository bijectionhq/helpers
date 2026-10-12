package review

// Each state describes one independent decision. JSON retains the same vocabulary.
type SelectionState string
type SourceState string
type WitnessState string
type VerificationState string
type EvidenceMode string

const (
	SelectionAll        SelectionState    = "all"
	SelectionExplicit   SelectionState    = "explicit"
	SourcePassed        SourceState       = "passed"
	SourceFailed        SourceState       = "failed"
	SourceStale         SourceState       = "stale"
	WitnessesUnknown    WitnessState      = "unknown"
	WitnessesNone       WitnessState      = "none_declared"
	WitnessesMapped     WitnessState      = "mapped"
	WitnessesUnmapped   WitnessState      = "unmapped"
	VerificationPending VerificationState = "pending"
	VerificationPassed  VerificationState = "passed"
	VerificationFailed  VerificationState = "failed"
	VerificationStale   VerificationState = "stale"
	EvidenceExecuted    EvidenceMode      = "executed"
	EvidenceRetained    EvidenceMode      = "retained"
	EvidenceReused      EvidenceMode      = "reused"
)

// Outcome names the next meaningful step; the independent diagnostic states
// remain available for explanation and machine consumers.
type Outcome string

const (
	NoChanges         Outcome = "no_changes"
	NeedsVerification Outcome = "needs_verification"
	Blocked           Outcome = "blocked"
	Ready             Outcome = "ready"
)

func (o Outcome) Headline() string {
	switch o {
	case NoChanges:
		return "No changes"
	case NeedsVerification:
		return "Needs verification"
	case Ready:
		return "Ready to publish"
	default:
		return "Blocked"
	}
}
