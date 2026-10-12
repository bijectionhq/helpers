// Package exceptions owns exact, previewed source fences and approval bindings.
// It never runs witnesses, stages files, publishes or absorbs unselected changes.
package exceptions

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"concave/internal/analysis"
	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type Options struct {
	Annotate bool
	Path     string
	File     bool
	Hunks    []string
	Replace  []string
	Metadata policy.Explanation
}

// Change is the source review for one binding. Proposal owns the combined
// policy patch, freshness boundary and proposal fingerprint.
type Change struct {
	Action         Action              `json:"action"`
	ID             string              `json:"id"`
	Previous       []policy.Exception  `json:"previous"`
	Next           *policy.Exception   `json:"next"`
	Explanation    *policy.Explanation `json:"explanation"`
	Hunks          []string            `json:"hunks"`
	SourceDiff     string              `json:"source_diff"`
	PreviousSource *PreviousSource     `json:"previous_source,omitempty"`
	// Remaining are the findings a removal leaves on the binding's span.
	Remaining []policy.Finding `json:"remaining,omitempty"`
	observed  repository.Inputs
	cleanup   *policy.Annotation
}
type Explanation struct {
	Format             int                 `json:"format"`
	ID                 string              `json:"id"`
	Status             string              `json:"status"`
	Source             string              `json:"source"`
	Binding            policy.Exception    `json:"binding"`
	Explanation        *policy.Explanation `json:"explanation"`
	FileClassification string              `json:"file_classification"`
	Findings           []policy.Finding    `json:"findings"`
	BehavioralParity   string              `json:"behavioral_parity"`
	PreviousSource     *PreviousSource     `json:"previous_source,omitempty"`
}

func lookup(config *policy.Config, id string) *policy.Exception {
	for _, binding := range config.Exceptions {
		if binding.ID == id {
			copy := binding
			return &copy
		}
	}
	return nil
}

// A batch shares the same immutable analysis, but every source and reference is
// rechecked against it before it can enter the combined proposal.
func prepareFromReport(repo *repository.Repo, config *policy.Config, action Action, id string, options Options, old *policy.Exception, report *analysis.Report, history *sourceHistory) (*Change, error) {
	if options.Path == "" {
		for _, f := range report.Findings {
			if f.Exception == id && f.Kind == "unknown_fence" {
				if options.Path != "" && options.Path != f.Path {
					return nil, fmt.Errorf("duplicate annotation ID across files")
				}
				options.Path = f.Path
			}
		}
		if options.Path == "" {
			return nil, fmt.Errorf("annotation %q not found; use --path for an uncommentable file", id)
		}
	}
	var row *analysis.Row
	for i := range report.Files {
		if report.Files[i].Path == options.Path {
			row = &report.Files[i]
			break
		}
	}
	if options.File || old != nil && old.File != nil {
		return prepareFile(repo, config, action, id, options, old, report, row)
	}
	if row == nil || row.Upstream == nil || row.Selected == nil || row.Selected.Kind != "file" || (row.Upstream.Mode != "100644" && row.Upstream.Mode != "100755") || row.Upstream.Mode != row.Selected.Mode {
		return nil, fmt.Errorf("exception requires an existing inherited regular text file with unchanged mode")
	}
	if config.Exact(options.Path) {
		return nil, fmt.Errorf("exact-preservation files cannot have exceptions")
	}
	state, after, err := repo.Snapshot(options.Path)
	if err != nil {
		return nil, err
	}
	if !repository.Same(state, row.Selected) {
		return nil, &repository.Stale{Message: "source changed; inspect again"}
	}
	before, err := repo.Blob(row.Upstream.OID)
	if err != nil {
		return nil, err
	}
	if !repository.Text(before) || !repository.Text(after) {
		return nil, fmt.Errorf("exception requires bounded UTF-8 source")
	}
	hunks := []repository.Hunk{}
	for _, h := range report.Hunks {
		if h.Path == options.Path {
			hunks = append(hunks, h)
		}
	}
	annotations, bad := history.session.Annotations(options.Path, string(after))
	if len(bad) > 0 {
		return nil, fmt.Errorf("invalid annotation at %s:%d: %s", options.Path, bad[0].Line, bad[0].Message)
	}
	policy.AbsorbBlankEdges(annotations, string(after), hunks)
	annotation, fenced := annotations[id]
	change := &Change{Action: action, ID: id, Previous: []policy.Exception{}, Hunks: []string{}, observed: repository.Inputs{options.Path: state}}
	if old != nil {
		change.Previous = append(change.Previous, *old)
		change.PreviousSource, err = history.previous(*old, after)
		if err != nil {
			return nil, err
		}
	}
	if old != nil && !fenced && (bytes.Equal(before, after) || old.UpstreamSHA256 == repository.Digest(before) && policy.SpanRestored(*old, hunks)) {
		if len(options.Hunks) > 0 {
			return nil, fmt.Errorf("restored exception removal takes no hunks")
		}
		change.Action = Remove
	} else {
		next := policy.Exception{ID: id, Path: options.Path, UpstreamSHA256: repository.Digest(before), Fence: fenced}
		var metadata policy.Explanation
		chosen := []repository.Hunk{}
		if fenced {
			if options.Metadata.Why != "" || len(options.Metadata.Witnesses) > 0 || options.Metadata.RemoveWhen != "" || len(options.Metadata.Preserves) > 0 {
				return nil, fmt.Errorf("fenced explanations are authored only in source")
			}
			span, err := policy.AnnotationSpan(annotation, hunks)
			if err != nil {
				return nil, err
			}
			next.Start, next.End = span.Start, span.End
			lines := repository.Lines(string(before))
			if next.Start < 0 || next.End < next.Start || next.End > len(lines) {
				return nil, fmt.Errorf("annotation maps outside upstream source")
			}
			composition, err := composeBinding(repo, config, before, &next)
			if err != nil {
				return nil, err
			}
			if annotation.Code == strings.Join(lines[next.Start:next.End], "") || annotation.Code == composition.Original {
				if action != Refresh || old == nil || old.UpstreamSHA256 != next.UpstreamSHA256 || old.Start != next.Start || old.End != next.End || len(options.Hunks) != 0 {
					return nil, fmt.Errorf("obsolete annotation cleanup requires refresh of the unchanged upstream binding, without --hunk")
				}
				change.Action = Remove
				change.Explanation = &annotation.Explanation
				change.cleanup = &annotation
				change.SourceDiff = string(repository.Patch(options.Path, []repository.Hunk{{Start: annotation.Start, End: annotation.End, CurrentStart: annotation.Start, CurrentEnd: annotation.Start + len(repository.Lines(annotation.Code)), Before: annotation.Whole, After: annotation.Code}}))
				return change, nil
			}
			for _, h := range hunks {
				if h.CurrentStart >= annotation.Start && h.CurrentEnd <= annotation.End && h.Start >= next.Start && h.End <= next.End {
					chosen = append(chosen, h)
				}
			}
			if len(chosen) == 0 {
				return nil, fmt.Errorf("annotation covers no changed hunks")
			}
			next.ReplacementSHA256 = annotation.CodeSHA256()
			metadata = annotation.Explanation
			// The preview shows what the explanation covers: the difference
			// that remains after the enclosed transformations.
			change.SourceDiff = string(repository.Patch(options.Path, []repository.Hunk{{Start: next.Start, End: next.End, CurrentStart: annotation.Start, CurrentEnd: annotation.End, Before: composition.Original, After: annotation.Whole}}))
		} else {
			if policy.SupportsAnnotations(options.Path) {
				return nil, fmt.Errorf("add a source annotation for %s before recording this exception", id)
			}
			if len(options.Hunks) != 1 {
				return nil, fmt.Errorf("unfenced exceptions require exactly one explicit hunk")
			}
			for _, h := range hunks {
				if h.ID == options.Hunks[0] {
					chosen = append(chosen, h)
				}
			}
			if len(chosen) != 1 {
				return nil, &repository.Stale{Message: "hunk ID does not match current source; inspect again"}
			}
			h := chosen[0]
			next.Start, next.End = h.Start, h.End
			if _, err := composeBinding(repo, config, before, &next); err != nil {
				return nil, err
			}
			next.ReplacementSHA256 = repository.Digest([]byte(h.After))
			metadata = options.Metadata
			if old != nil && metadata.Why == "" && len(metadata.Witnesses) == 0 && metadata.RemoveWhen == "" && len(metadata.Preserves) == 0 {
				metadata = old.Explanation()
			}
			next.Reason, next.Witnesses, next.RemoveWhen = metadata.Why, metadata.Witnesses, metadata.RemoveWhen
			next.Preserves = metadata.Preserves
			change.SourceDiff = string(repository.Patch(options.Path, chosen))
		}
		for _, h := range chosen {
			change.Hunks = append(change.Hunks, h.ID)
		}
		sort.Strings(change.Hunks)
		// A new source fence names the complete preview scope. CLI writes
		// require that preview's fingerprint. Existing bindings still need
		// explicit hunk selection when their original span or anchor changes.
		explicit := len(options.Hunks) > 0 || old != nil && (old.Start != next.Start || old.End != next.End || old.UpstreamSHA256 != next.UpstreamSHA256)
		if explicit {
			ids := append([]string{}, options.Hunks...)
			sort.Strings(ids)
			if !equalStrings(ids, change.Hunks) {
				return nil, &repository.Stale{Message: "select exactly every hunk inside this annotation; stale, duplicate, outside or omitted hunk IDs refuse"}
			}
		}
		if err := observeReferences(repo, change, options.Path, metadata); err != nil {
			return nil, err
		}
		change.Next = &next
		change.Explanation = &metadata
	}
	remove := map[string]bool{}
	if old != nil {
		remove[id] = true
	}
	for _, other := range options.Replace {
		if remove[other] {
			return nil, fmt.Errorf("duplicate replaced exception")
		}
		binding := lookup(config, other)
		if binding == nil || binding.File != nil || change.Next == nil || binding.Path != change.Next.Path || binding.Start < change.Next.Start || binding.End > change.Next.End {
			return nil, fmt.Errorf("replacement %s must be an existing binding contained in the new original span", other)
		}
		// Both spans use pinned-upstream coordinates, but an enclosing fence
		// may include transformations that the smaller fence does not. Validate
		// each old anchor against its own original instead of requiring equal
		// hashes for these different, independently valid compositions.
		if _, err := Original(repo, config, before, *binding); err != nil {
			return nil, fmt.Errorf("replacement %s: %w", other, err)
		}
		remove[other] = true
		change.Previous = append(change.Previous, *binding)
	}
	return change, nil
}

// Original is what a recorded binding explains away: its original span, after
// the transformations it encloses when it anchors the transformed original.
// A binding still anchored to the pinned original explains the whole span.
func Original(repo *repository.Repo, config *policy.Config, before []byte, binding policy.Exception) (string, error) {
	lines := repository.Lines(string(before))
	if binding.Start < 0 || binding.End < binding.Start || binding.End > len(lines) {
		return "", fmt.Errorf("binding %s maps outside upstream source", binding.ID)
	}
	if binding.UpstreamSHA256 == repository.Digest(before) {
		return strings.Join(lines[binding.Start:binding.End], ""), nil
	}
	anchor := binding.UpstreamSHA256
	composition, err := composeBinding(repo, config, before, &binding)
	if err != nil {
		return "", err
	}
	if composition.Anchor != anchor {
		return "", fmt.Errorf("binding %s is stale; refresh it before mutating", binding.ID)
	}
	return composition.Original, nil
}

// Authoring uses the same upstream recipe and hunk owner as inspection. A
// binding enclosing transformation edits anchors the transformed original, so a
// changed recipe makes its explanation stale; one crossing an edit refuses.
func composeBinding(repo *repository.Repo, config *policy.Config, before []byte, binding *policy.Exception) (policy.Composition, error) {
	plans, err := policy.NewSession(repo, config).TransformationPlans(repo, binding.Path, before)
	if err != nil {
		return policy.Composition{}, err
	}
	composition, err := policy.Compose(before, plans, policy.Span{Start: binding.Start, End: binding.End})
	if err != nil {
		return composition, fmt.Errorf("exception %s: %w", binding.ID, err)
	}
	binding.UpstreamSHA256 = composition.Anchor
	return composition, nil
}

func observeReferences(repo *repository.Repo, change *Change, path string, metadata policy.Explanation) error {
	return policy.CheckReferences(path, metadata, contracts.New(func(path string) ([]byte, error) {
		state, content, e := repo.Snapshot(path)
		if e != nil {
			return nil, e
		}
		if state == nil || state.Kind != "file" || !repository.Text(content) {
			return nil, fmt.Errorf("missing regular UTF-8 reference %s", path)
		}
		if prior, ok := change.observed[path]; ok && !repository.Same(prior, state) {
			return nil, &repository.Stale{Message: "source/reference changed"}
		}
		change.observed[path] = state
		return content, nil
	}, syntax.New(repo.Context, repo.Root)))
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeReviewed applies the source and binding patches as one exact-preimage
// patch. Its guard is the proposal's own scope: the policy file, manifest and
// the bound paths' binding files, plus every observed source input. Approvals
// recorded meanwhile for other paths, or a moved HEAD, cannot refuse it.
func writeReviewed(repo *repository.Repo, proposal *Proposal, reason string) error {
	now, err := policy.Load(repo, "")
	if err != nil {
		return err
	}
	if now.ScopedSHA256(proposal.BindingPaths) != proposal.PolicySHA256 {
		return &repository.Stale{Message: "policy or bindings of " + strings.Join(proposal.BindingPaths, ", ") + " changed; preview again"}
	}
	if err := proposal.Observed.Verify(repo, repository.SourceInputs); err != nil {
		return err
	}
	patch := proposal.SourcePatch + proposal.PolicyPatch
	if patch == "" {
		return nil
	}
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return err
	}
	effects, err := recovery.CheckedPatch(repo, []byte(patch), proposal.created, proposal.deleted)
	if err != nil {
		return err
	}
	plan := &recovery.Plan{Format: 1, Kind: "concave-recovery-plan", Head: head, PolicySHA256: now.SHA256, Reason: reason, CreatedPaths: proposal.created, DeletedPaths: proposal.deleted, Files: effects, Patch: patch, PatchSHA256: repository.Digest([]byte(patch))}
	return recovery.Apply(repo, now, plan)
}
