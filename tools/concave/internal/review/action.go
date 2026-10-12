package review

import "concave/internal/continuation"

// Passed reports whether the inspected scope satisfies its current obligations.
// A working review can pass inspection before its changes are selected.
func (v *Verdict) Passed() bool {
	return v.Source == SourcePassed && len(v.Blockers) == 0 &&
		v.Verification != VerificationFailed && v.Verification != VerificationStale
}

func (v *Verdict) Ready() bool { return v.Outcome == Ready }

func (r *Contribution) NextAction(verdict *Verdict) continuation.Action {
	recapture := append([]string{"concave", "review"}, r.Context.Candidate.Scope.Paths...)
	switch {
	case verdict.Source == SourceStale:
		return continuation.Action{Kind: continuation.Recapture, Message: "Source changed; capture the scope again and reselect your hunks.", Argv: recapture}
	case verdict.Source == SourceFailed:
		return continuation.Action{Kind: continuation.RepairSource, Message: "Resolve the selected source findings, then capture again.", Argv: recapture}
	case r.Candidate == nil && len(r.Hunks) == 0:
		return continuation.Action{Kind: continuation.None, Message: "No source changes in this scope; nothing to prepare."}
	case verdict.Witnesses == WitnessesUnmapped:
		return continuation.Action{Kind: continuation.MapWitnesses, Message: "Map the listed witnesses to executable cases in concave.json, then capture again.", Argv: recapture}
	case r.Candidate == nil:
		return continuation.Action{Kind: continuation.Verify, Message: "Verify every change shown; capture again with --hunk or --select to narrow it first.", Argv: []string{"concave", "verify", r.Reference()}}
	}
	if !verdict.Passed() || verdict.Verification != VerificationPassed {
		interrupted := false
		for _, check := range verdict.Checks {
			if len(check.Problems) > 0 && (check.Problems[0].Code == "cancelled" || check.Problems[0].Code == "timed_out") {
				interrupted = true
				continue
			}
			for _, problem := range check.Problems {
				if problem.Code == "tool_unavailable" || problem.Code == "environment_failed" {
					return continuation.Action{Kind: continuation.RepairEnvironment, Message: problem.Message + "\nAfter resolving this setup failure, retry this candidate.", Argv: []string{"concave", "verify", r.Reference()}}
				}
				if problem.Code == "command_failed" || problem.Code == "invalid_results" || problem.Code == "tests_failed" {
					return continuation.Action{Kind: continuation.RepairCheck, Message: "Inspect the retained check logs, fix the failure and capture the changed source again.", Argv: recapture}
				}
			}
		}
		if interrupted {
			return continuation.Action{Kind: continuation.Verify, Message: "Verification was interrupted; logs and completed evidence were retained. Retry this candidate.", Argv: []string{"concave", "verify", r.Reference()}}
		}
		return continuation.Action{Kind: continuation.Verify, Message: "Verify the required checks on this candidate.", Argv: []string{"concave", "verify", r.Reference()}}
	}
	return continuation.Action{Kind: continuation.Publish, Message: "Publish the verified review through land.py.", Argv: []string{"concave", "review", "publish", r.Reference()}, Requires: []string{"--message <what changed and why>"}}
}

func (r *Contribution) completeVerdict(v *Verdict) {
	v.Next = r.NextAction(v)
	switch v.Next.Kind {
	case continuation.None:
		v.Outcome = NoChanges
	case continuation.Verify:
		v.Outcome = NeedsVerification
	case continuation.Publish:
		v.Outcome = Ready
	default:
		v.Outcome = Blocked
	}
}
