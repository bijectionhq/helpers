package exceptions

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
)

// ProposalFormat versions concave-exception-proposal.
const ProposalFormat = 4

// MaxChanges bounds an explicit proposal; there is deliberately no --all.
const MaxChanges = 128

type Action string

const (
	Record  Action = "record"
	Refresh Action = "refresh"
	Remove  Action = "remove"
)

type Status string

const (
	Preview   Status = "preview"
	Unchanged Status = "unchanged"
	Recorded  Status = "recorded"
)

// A proposal binds exactly what it reads and writes: its source inputs, and
// the policy identity scoped to the binding files of BindingPaths. Approvals
// of other paths, and HEAD, do not change it.
type Proposal struct {
	Format         int               `json:"format"`
	Kind           string            `json:"kind"`
	Status         Status            `json:"status"`
	Action         Action            `json:"action"`
	BindingPaths   []string          `json:"binding_paths"`
	PolicySHA256   string            `json:"policy_sha256"`
	Changes        []*Change         `json:"changes"`
	Observed       repository.Inputs `json:"observed"`
	PolicyPatch    string            `json:"policy_patch"`
	SourcePatch    string            `json:"source_patch,omitempty"`
	ProposalSHA256 string            `json:"proposal_sha256"`
	created        []string
	deleted        []string
}

// Prepare analyzes once, reviews every selected binding, and validates the
// resulting policy once. Single and multiple selections have the same schema.
func Prepare(repo *repository.Repo, config *policy.Config, action Action, ids []string, options Options) (*Proposal, error) {
	if options.Annotate {
		return prepareAnnotated(repo, config, action, ids, options)
	}
	if action == Remove {
		if len(ids) == 0 || len(ids) > MaxChanges {
			return nil, fmt.Errorf("select 1..%d exception IDs", MaxChanges)
		}
		return prepareRemove(repo, config, ids, options)
	}
	if action != Record && action != Refresh {
		return nil, fmt.Errorf("unknown exception action %q", action)
	}
	if len(ids) == 0 || len(ids) > MaxChanges {
		return nil, fmt.Errorf("select 1..%d exception IDs", MaxChanges)
	}
	if len(ids) > 1 && (options.File || len(options.Hunks) != 0 || len(options.Replace) != 0 || options.Metadata.Why != "" || len(options.Metadata.Witnesses) != 0 || options.Metadata.RemoveWhen != "" || len(options.Metadata.Preserves) != 0) {
		return nil, fmt.Errorf("multiple selections accept named source fences and optional --path; explicit file, hunk, replacement or metadata options require one ID")
	}
	if action == Refresh && len(options.Replace) != 0 {
		return nil, fmt.Errorf("refresh cannot replace other exceptions")
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	var paths []string
	if options.Path != "" {
		paths = append(paths, options.Path)
	}
	for index, id := range ids {
		if index > 0 && ids[index-1] == id {
			return nil, fmt.Errorf("duplicate exception ID %q", id)
		}
		old := lookup(config, id)
		if action == Record && old != nil {
			return nil, fmt.Errorf("exception %q exists; use refresh", id)
		}
		if action == Refresh {
			if old == nil {
				return nil, fmt.Errorf("unknown exception %q", id)
			}
			if options.Path != "" && options.Path != old.Path {
				return nil, fmt.Errorf("refresh cannot move exception %s to another file", id)
			}
			paths = append(paths, old.Path)
		}
	}
	if options.File && action == Record && options.Path == "" {
		return nil, fmt.Errorf("file binding requires --path")
	}
	slices.Sort(paths)
	report, err := analysis.Analyze(repo, config, analysis.Options{Paths: slices.Compact(paths)})
	if err != nil {
		return nil, err
	}
	proposal := &Proposal{Format: ProposalFormat, Kind: "concave-exception-proposal", Status: Preview, Action: action, Changes: []*Change{}, Observed: repository.Inputs{}}
	var selectedBindings []policy.Exception
	for _, id := range ids {
		if old := lookup(config, id); old != nil {
			selectedBindings = append(selectedBindings, *old)
		}
	}
	history := newSourceHistory(repo, report.Head, selectedBindings)
	total := 0
	for _, id := range ids {
		selected := options
		old := lookup(config, id)
		if old != nil {
			selected.Path = old.Path
		}
		change, err := prepareFromReport(repo, config, action, id, selected, old, report, history)
		if err != nil {
			return nil, fmt.Errorf("exception %s: %w", id, err)
		}
		total += len(change.SourceDiff)
		if change.PreviousSource != nil {
			total += len(change.PreviousSource.Diff)
		}
		if total > repository.MaxOutput {
			return nil, fmt.Errorf("source review exceeds size bound")
		}
		for path, state := range change.observed {
			if err := proposal.Observed.Observe(path, state); err != nil {
				return nil, err
			}
		}
		change.observed = nil
		proposal.Changes = append(proposal.Changes, change)
	}
	if err := cleanupPatch(repo, proposal); err != nil {
		return nil, err
	}
	return finish(config, proposal)
}

// Cleanup is another guarded source proposal, never an eager rewrite. Combine
// selected spans per file so adjacent fences cannot overwrite each other's edits.
func cleanupPatch(repo *repository.Repo, proposal *Proposal) error {
	files := map[string][]policy.Annotation{}
	for _, change := range proposal.Changes {
		if change.cleanup != nil {
			path := change.Previous[0].Path
			files[path] = append(files[path], *change.cleanup)
			change.cleanup = nil
		}
	}
	paths := []string{}
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		state, source, err := repo.Snapshot(path)
		if err != nil {
			return err
		}
		if !repository.Same(state, proposal.Observed[path]) {
			return &repository.Stale{Message: "cleanup source changed: " + path}
		}
		annotations := files[path]
		slices.SortFunc(annotations, func(a, b policy.Annotation) int { return a.Start - b.Start })
		lines := repository.Lines(string(source))
		var next strings.Builder
		position := 0
		for _, annotation := range annotations {
			if annotation.Start < position || annotation.End > len(lines) || strings.Join(lines[annotation.Start:annotation.End], "") != annotation.Whole {
				return fmt.Errorf("cleanup span changed or overlaps another fence: %s", annotation.ID)
			}
			next.WriteString(strings.Join(lines[position:annotation.Start], ""))
			next.WriteString(annotation.Code)
			position = annotation.End
		}
		next.WriteString(strings.Join(lines[position:], ""))
		proposal.SourcePatch += string(recovery.SinglePatch(path, source, []byte(next.String())))
	}
	return nil
}

func finish(config *policy.Config, proposal *Proposal) (*Proposal, error) {
	replacements := map[string]*Change{}
	for _, change := range proposal.Changes {
		for _, previous := range change.Previous {
			replacements[previous.ID] = change
		}
	}
	next := *config
	next.Exceptions = []policy.Exception{}
	inserted := map[*Change]bool{}
	for _, previous := range config.Exceptions {
		change, replaced := replacements[previous.ID]
		if !replaced {
			next.Exceptions = append(next.Exceptions, previous)
			continue
		}
		if !inserted[change] && change.Next != nil {
			next.Exceptions = append(next.Exceptions, *change.Next)
			inserted[change] = true
		}
	}
	for _, change := range proposal.Changes {
		if !inserted[change] && change.Next != nil {
			next.Exceptions = append(next.Exceptions, *change.Next)
		}
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	touched := map[string]bool{}
	for _, change := range proposal.Changes {
		for _, previous := range change.Previous {
			touched[previous.Path] = true
		}
		if change.Next != nil {
			touched[change.Next.Path] = true
		}
	}
	proposal.BindingPaths = []string{}
	for path := range touched {
		proposal.BindingPaths = append(proposal.BindingPaths, path)
	}
	slices.Sort(proposal.BindingPaths)
	proposal.PolicySHA256 = config.ScopedSHA256(proposal.BindingPaths)
	for _, path := range proposal.BindingPaths {
		var bindings []policy.Exception
		for _, binding := range next.Exceptions {
			if binding.Path == path {
				bindings = append(bindings, binding)
			}
		}
		updated, err := policy.EncodeBindings(path, bindings)
		if err != nil {
			return nil, err
		}
		previous, existed := config.Bindings[path]
		if bytes.Equal(previous, updated) && existed == (updated != nil) {
			continue
		}
		file := policy.BindingPath(path)
		switch {
		case !existed:
			proposal.created = append(proposal.created, file)
		case updated == nil:
			proposal.deleted = append(proposal.deleted, file)
		}
		proposal.PolicyPatch += string(recovery.FilePatch(file, previous, existed, updated, updated != nil))
	}
	if proposal.PolicyPatch == "" {
		proposal.Status = Unchanged
	}
	var err error
	proposal.ProposalSHA256, err = proposal.proposalDigest()
	return proposal, err
}

func (proposal *Proposal) proposalDigest() (string, error) {
	content := *proposal
	content.ProposalSHA256 = ""
	content.Status = "" // Preview/recorded presentation does not change the approved content.
	return repository.JSONDigest(content)
}

func Write(repo *repository.Repo, config *policy.Config, proposal *Proposal) error {
	digest, err := proposal.proposalDigest()
	if err != nil {
		return err
	}
	if digest != proposal.ProposalSHA256 {
		return &repository.Stale{Message: "exception proposal changed"}
	}
	return writeReviewed(repo, proposal, "explicit exception "+string(proposal.Action))
}
