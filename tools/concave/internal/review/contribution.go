package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"concave/internal/analysis"
	"concave/internal/continuation"
	"concave/internal/exceptions"
	"concave/internal/policy"
	"concave/internal/recovery"
	"concave/internal/repository"
	"concave/internal/verification"
)

const ContributionFormat = 6

// VerdictFormat versions the concave-verdict projection.
const VerdictFormat = 3

// revisionKinds names each immutable revision of a contribution by its ID prefix.
var revisionKinds = map[string]string{"r-": "concave-capture", "c-": "concave-candidate", "v-": "concave-verification"}

type CheckEvidence struct {
	Mode      EvidenceMode          `json:"mode"`
	Reason    string                `json:"reason,omitempty"`
	Directory string                `json:"directory"`
	Receipt   *verification.Receipt `json:"receipt"`
}

// Candidate binds selected content to the exact tree prepared by land.py.
type Candidate struct {
	Commit  string           `json:"commit"`
	Tree    string           `json:"tree"`
	Parent  string           `json:"parent"`
	Content *SelectedContent `json:"content"`
}

// Verification is a separate immutable child of a candidate, with per-check provenance.
type Verification struct {
	Candidate string          `json:"candidate"`
	Evidence  []CheckEvidence `json:"evidence"`
}

// Contribution is one immutable revision of explicitly selected source changes.
// Its context packet owns source context; receipts remain owned by verification.
type Contribution struct {
	Format            int                      `json:"format"`
	Kind              string                   `json:"kind"`
	ID                string                   `json:"id"`
	Parent            string                   `json:"parent,omitempty"`
	Origin            string                   `json:"origin,omitempty"`
	Context           *analysis.ContractReview `json:"context"`
	Inputs            repository.Inputs        `json:"inputs"`
	Inventory         []string                 `json:"inventory"`
	Hunks             []repository.Hunk        `json:"hunks"`
	Selected          []string                 `json:"selected"`
	RequiredChecks    []string                 `json:"required_checks"`
	UnmappedWitnesses []string                 `json:"unmapped_witnesses"`
	Candidate         *Candidate               `json:"candidate,omitempty"`
	Verification      *Verification            `json:"verification,omitempty"`
	Recoveries        []recovery.Plan          `json:"recoveries,omitempty"`
	Approvals         []exceptions.Proposal    `json:"approvals,omitempty"`
}

func Compose(repo *repository.Repo, config *policy.Config, packet *analysis.ContractReview, selected []string) (*Contribution, error) {
	if packet.Candidate.Revision != "" && repo.Revision == "" {
		return nil, fmt.Errorf("contribution capture requires a matching source view")
	}
	r := &Contribution{Format: ContributionFormat, Kind: revisionKinds["r-"], Context: packet, Inputs: repository.Inputs{}, Selected: selected}
	var inventoryErr error
	r.Inventory, inventoryErr = r.inventory(repo)
	if inventoryErr != nil {
		return nil, inventoryErr
	}
	pairs := map[string]repository.SourcePair{}
	before := map[string][]byte{}
	baselineTree, err := repo.Tree(packet.Against)
	if err != nil {
		return nil, err
	}
	for _, s := range packet.Sources {
		if s.Version == "baseline" {
			if s.Unavailable == "" {
				before[s.Path] = []byte(packet.Blobs[s.SHA256])
			} else if s.OID == "" {
				before[s.Path] = []byte{} // Explicit absence, not unavailable bytes.
			}
		}
	}
	for _, s := range packet.Sources {
		if s.Version != "candidate" && s.Version != "consumer" {
			continue
		}
		state, data, err := repo.Snapshot(s.Path)
		if err != nil {
			return nil, err
		}
		if s.Unavailable == "" && (state == nil || state.SHA256 != s.SHA256) {
			return nil, &repository.Stale{Message: "review source changed: " + s.Path}
		}
		if s.Unavailable != "" && (state == nil && s.OID != "" || state != nil && state.OID != s.OID) {
			return nil, &repository.Stale{Message: "unavailable review source changed: " + s.Path}
		}
		if err := r.Inputs.Observe(s.Path, state); err != nil {
			return nil, err
		}
		if state == nil {
			data = []byte{}
		}
		// Dependencies remain bound inputs, but only explicitly scoped source is
		// offered as a contribution. Peer policy edits are not selected implicitly.
		if s.Version == "candidate" && repository.Matches(s.Path, packet.Candidate.Scope.Paths) {
			if !repository.Text(data) || !repository.Text(before[s.Path]) {
				baseline := baselineTree[s.Path]
				if state == nil || state.OID != baseline.OID || state.Mode != baseline.Mode {
					return nil, fmt.Errorf("contribution requires bounded regular UTF-8 source: %s; inspect the file binding and use the repository landing owner for this file operation", s.Path)
				}
				continue
			}
			pairs[s.Path] = repository.SourcePair{Before: before[s.Path], After: data}
		}
	}
	hs, err := repo.DiffSources(pairs)
	if err != nil {
		return nil, err
	}
	for path, pair := range pairs {
		hunks := hs[path]
		beforeMode := baselineTree[path].Mode
		afterMode := ""
		if state := r.Inputs[path]; state != nil {
			afterMode = state.Mode
		}
		if beforeMode != afterMode {
			if beforeMode == "" {
				beforeMode = "absent"
			}
			if afterMode == "" {
				afterMode = "absent"
			}
			hunks = []repository.Hunk{{BeforeMode: beforeMode, AfterMode: afterMode, End: len(repository.Lines(string(pair.Before))), CurrentEnd: len(repository.Lines(string(pair.After))), Before: string(pair.Before), After: string(pair.After)}}
		}
		r.Hunks = append(r.Hunks, repository.Identify(path, pairs[path].Before, pairs[path].After, hunks)...)
	}
	sort.Slice(r.Hunks, func(i, j int) bool {
		if r.Hunks[i].Path != r.Hunks[j].Path {
			return r.Hunks[i].Path < r.Hunks[j].Path
		}
		return r.Hunks[i].Start < r.Hunks[j].Start
	})
	if err := r.validateSelection(); err != nil {
		return nil, err
	}
	r.RequiredChecks, r.UnmappedWitnesses = requiredChecks(config, packet, r.selectedHunks())
	// A policy edit cannot erase its own verification duty. Removed mappings and
	// case requirements remain actionable even if no source annotation names them.
	if repository.Matches(policy.File, packet.Candidate.Scope.Paths) {
		previous, err := policy.Load(repo, packet.Against)
		if err != nil {
			return nil, err
		}
		for name, old := range previous.Checks {
			for _, path := range old.Paths {
				if !repository.Matches(path, config.Checks[name].Paths) {
					packet.Candidate.Findings = append(packet.Candidate.Findings, policy.Finding{Kind: "policy_witness_weakened", Path: policy.File, Message: "required check source scope narrowed: " + name + " / " + path})
				}
			}
			for ref, cases := range old.Witnesses {
				current := config.Checks[name].Witnesses[ref]
				for _, id := range cases {
					if !slices.Contains(current, id) {
						packet.Candidate.Findings = append(packet.Candidate.Findings, policy.Finding{Kind: "policy_witness_weakened", Path: policy.File, Message: "required witness case removed: " + name + " / " + ref + " / " + id})
					}
				}
			}
		}
		policy.NormalizeFindings(packet.Candidate.Findings)
	}
	if err := r.Inputs.Verify(repo, repository.SourceInputs); err != nil {
		return nil, err
	}
	return r, r.Seal()
}

func requiredChecks(config *policy.Config, packet *analysis.ContractReview, selected []repository.Hunk) ([]string, []string) {
	witnesses := map[string]bool{}
	// Both sides matter: removing a witness does not remove its verification duty.
	for side, report := range []*analysis.Report{packet.Baseline, packet.Candidate} {
		if report.Contracts == nil {
			continue
		}
		for _, w := range report.Contracts.Witnesses {
			ref := w.Path
			if w.Symbol != "" {
				ref += "::" + w.Symbol
			}
			witnesses[ref] = true
		}
		for _, f := range report.Contracts.Files {
			for _, ex := range f.Exceptions {
				inside := len(selected) == 0
				for _, h := range selected {
					start, end := h.CurrentStart, h.CurrentEnd
					if side == 0 {
						start, end = h.Start, h.End
					}
					if h.Path == f.Path && (ex.Start == 0 || overlap(start, end, ex.Start-1, ex.End)) {
						inside = true
					}
				}
				if !inside {
					continue
				}
				for _, w := range ex.Explanation.Witnesses {
					path, symbol, err := policy.WitnessTarget(f.Path, w)
					if err != nil {
						continue
					}
					ref := path
					if symbol != "" {
						ref += "::" + symbol
					}
					witnesses[ref] = true
				}
			}
		}
	}
	paths := []string{}
	if len(selected) == 0 {
		for _, report := range []*analysis.Report{packet.Baseline, packet.Candidate} {
			for _, file := range report.Files {
				paths = append(paths, file.Path)
			}
		}
	}
	for _, hunk := range selected {
		paths = append(paths, hunk.Path)
	}
	references := []string{}
	for witness := range witnesses {
		references = append(references, witness)
	}
	requirements := config.RequiredChecks(paths, references)
	names := []string{}
	for _, check := range requirements.Checks {
		names = append(names, check.Name)
	}
	return names, requirements.Unmapped
}

func (r *Contribution) selectedHunks() []repository.Hunk {
	if len(r.Selected) == 0 {
		return nil
	}
	ids := map[string]bool{}
	for _, id := range r.Selected {
		ids[id] = true
	}
	out := []repository.Hunk{}
	for _, h := range r.Hunks {
		if ids[h.ID] {
			out = append(out, h)
		}
	}
	return out
}

func (r *Contribution) digest() (string, error) {
	copy := *r
	copy.ID = ""
	// Contract changes contain typed values before serialization and maps after
	// reading. Canonicalize both, preserving exact JSON numbers.
	raw, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	return repository.JSONDigest(value)
}
func (r *Contribution) prefix() string {
	if r.Verification != nil {
		return "v-"
	}
	if r.Candidate != nil {
		return "c-"
	}
	return "r-"
}
func (r *Contribution) Seal() error {
	r.Kind = revisionKinds[r.prefix()]
	digest, err := r.digest()
	r.ID = r.prefix() + digest
	return err
}
func (r *Contribution) Evidence() []CheckEvidence {
	if r.Verification == nil {
		return nil
	}
	return r.Verification.Evidence
}
func (r *Contribution) validateSelection() error {
	available := map[string]bool{}
	for _, h := range r.Hunks {
		available[h.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range r.Selected {
		if !available[id] || seen[id] {
			return fmt.Errorf("absent or duplicate review hunk %s", id)
		}
		seen[id] = true
	}
	return nil
}

func (r *Contribution) Validate(repo *repository.Repo, config *policy.Config) error {
	digest, err := r.digest()
	if err != nil || r.Format != ContributionFormat || r.Kind != revisionKinds[r.prefix()] || r.ID != r.prefix()+digest || r.Context == nil || r.Context.Candidate == nil || r.Context.Baseline == nil {
		return fmt.Errorf("invalid contribution review; capture again")
	}
	if err := r.validateSelection(); err != nil {
		return err
	}
	inventory, err := r.inventory(repo)
	if err != nil {
		return err
	}
	if !slices.Equal(inventory, r.Inventory) {
		return &repository.Stale{Message: "review scope membership changed; capture again"}
	}
	if r.Candidate == nil && config.SHA256 != r.Context.Candidate.PolicySHA256 {
		return &repository.Stale{Message: "review policy changed; capture again"}
	}
	if r.Candidate != nil {
		rows := []string{}
		for _, row := range r.Context.Candidate.Files {
			rows = append(rows, row.Path)
		}
		for _, path := range config.Inputs(rows) {
			if _, ok := r.Inputs[path]; ok {
				continue
			}
			if _, binding := policy.BindingSource(path); binding {
				return &repository.Stale{Message: "bindings appeared in review scope: " + path + "; capture again"}
			}
			return fmt.Errorf("publication review omits policy input %s", path)
		}
	}
	if r.Candidate == nil {
		for _, p := range r.Recoveries {
			if err := recovery.ValidateApplied(repo, config, &p); err != nil {
				return err
			}
		}
		for _, p := range r.Approvals {
			if err := exceptions.ValidateRecorded(repo, config, &p); err != nil {
				return err
			}
		}
	}
	return r.Inputs.Verify(repo, repository.SourceInputs)
}

func (r *Contribution) inventory(repo *repository.Repo) ([]string, error) {
	paths, err := repo.Inventory()
	if err != nil {
		return nil, err
	}
	selected := []string{}
	for _, path := range paths {
		if repository.Matches(path, r.Context.Candidate.Scope.Paths) {
			selected = append(selected, path)
		}
	}
	if len(selected) > 20000 {
		return nil, fmt.Errorf("review inventory exceeds 20000 paths; narrow scope")
	}
	return selected, nil
}

type LocatedFinding struct {
	Finding policy.Finding `json:"finding"`
	Scope   string         `json:"scope"`
}
type Verdict struct {
	Outcome           Outcome             `json:"outcome"`
	Contribution      string              `json:"contribution"`
	Format            int                 `json:"format"`
	Kind              string              `json:"kind"`
	Selection         SelectionState      `json:"selection"`
	Source            SourceState         `json:"source"`
	Witnesses         WitnessState        `json:"witnesses"`
	Verification      VerificationState   `json:"verification"`
	Blockers          []string            `json:"blockers"`
	Next              continuation.Action `json:"next"`
	Checks            []CheckOutcome      `json:"checks"`
	Locations         []LocatedFinding    `json:"locations"`
	Revision          string              `json:"revision"`
	Artifact          string              `json:"artifact,omitempty"`
	Findings          []policy.Finding    `json:"findings"`
	Outside           []policy.Finding    `json:"outside_selected_changes"`
	MissingChecks     []string            `json:"missing_checks"`
	UnmappedWitnesses []string            `json:"unmapped_witnesses"`
	Uninspected       int                 `json:"uninspected_inherited_files"`
	BehavioralParity  string              `json:"behavioral_parity"`
}

type CheckOutcome struct {
	Name             string                 `json:"name"`
	Status           verification.Status    `json:"status"`
	Phase            string                 `json:"phase"`
	Mode             EvidenceMode           `json:"mode"`
	Reason           string                 `json:"reason,omitempty"`
	Directory        string                 `json:"directory"`
	ExecutionStarted bool                   `json:"execution_started"`
	Results          verification.Results   `json:"results"`
	Problems         []verification.Problem `json:"problems"`
}

func (r *Contribution) Check(repo *repository.Repo, config *policy.Config) (*Verdict, error) {
	if err := r.Validate(repo, config); err != nil {
		if repository.IsStale(err) {
			v := &Verdict{Format: VerdictFormat, Kind: "concave-verdict", Contribution: r.Reference(), Revision: r.ID, Source: SourceStale, Verification: VerificationStale, Witnesses: WitnessesUnknown, Selection: SelectionAll, BehavioralParity: "not_established", Blockers: []string{err.Error()}}
			r.completeVerdict(v)
			return v, err
		}
		return nil, err
	}
	v := &Verdict{Format: VerdictFormat, Kind: "concave-verdict", Contribution: r.Reference(), Revision: r.ID, BehavioralParity: "not_established", UnmappedWitnesses: r.UnmappedWitnesses, Uninspected: r.Context.Candidate.Coverage.Uninspected}
	selected := map[string]bool{}
	for _, id := range r.Selected {
		selected[id] = true
	}
	findings := r.Context.Candidate.Findings
	contracts := r.Context.Candidate.Contracts
	required := r.RequiredChecks

	for _, f := range findings {
		scope := "outside_selection"
		inside := len(r.Selected) == 0
		if inside {
			scope = "captured_scope"
		}
		for _, h := range r.Hunks {
			if selected[h.ID] && h.Path == f.Path && (f.Line == 0 || overlap(h.CurrentStart, h.CurrentEnd, f.Line-1, max(f.Line, f.EndLine))) {
				inside = true
				scope = "selected_edit"
				if f.Line-1 < h.CurrentStart || f.EndLine > h.CurrentEnd {
					scope = "enclosing_diff"
				}
			}
			if !selected[h.ID] || h.Path != f.Path || contracts == nil {
				continue
			}
			for _, file := range contracts.Files {
				if file.Path != h.Path {
					continue
				}
				for _, inv := range file.Invariants {
					start, end := inv.Line-1, inv.Declaration.Range.End.Line+1
					if overlap(h.CurrentStart, h.CurrentEnd, start, end) && overlap(f.Line-1, max(f.Line, f.EndLine), start, end) {
						inside = true
						scope = "enclosing_declaration"
					}
				}
				for _, ex := range file.Exceptions {
					if ex.Start != 0 && overlap(h.CurrentStart, h.CurrentEnd, ex.Start-1, ex.End) && overlap(f.Line-1, max(f.Line, f.EndLine), ex.Start-1, ex.End) {
						inside = true
						scope = "enclosing_declaration"
					}
				}
			}
		}
		if f.Path == policy.File || strings.HasPrefix(f.Kind, "policy_") {
			inside = true
		}
		if inside {
			v.Locations = append(v.Locations, LocatedFinding{f, scope})
			v.Findings = append(v.Findings, f)
		} else {
			v.Outside = append(v.Outside, f)
		}
	}
	v.Selection = SelectionAll
	if len(r.Selected) > 0 {
		v.Selection = SelectionExplicit
	}
	v.Source = SourcePassed
	if len(v.Findings) > 0 {
		v.Source = SourceFailed
		v.Blockers = append(v.Blockers, "resolve selected source findings")
	}
	v.Witnesses = WitnessesNone
	if len(required) > 0 {
		v.Witnesses = WitnessesMapped
	}
	if len(v.UnmappedWitnesses) > 0 {
		v.Witnesses = WitnessesUnmapped
		v.Blockers = append(v.Blockers, "map required witness cases")
	}
	v.Verification = VerificationPending
	if r.Verification != nil {
		v.Verification = VerificationPassed
	}
	valid := map[string]bool{}
	for _, e := range r.Evidence() {
		if e.Receipt == nil {
			return nil, fmt.Errorf("missing evidence receipt")
		}
		v.Checks = append(v.Checks, CheckOutcome{Name: e.Receipt.Check, Status: e.Receipt.Status, Phase: e.Receipt.Phase, Mode: e.Mode, Reason: e.Reason, Directory: e.Directory, ExecutionStarted: e.Receipt.ExecutionStarted, Results: e.Receipt.Results, Problems: append([]verification.Problem{}, e.Receipt.Problems...)})
		outcome := &v.Checks[len(v.Checks)-1]
		if e.Receipt.Status != verification.Passed {
			v.Verification = VerificationFailed
			continue
		}
		if err := verification.ValidateRecorded(repo, config, e.Receipt); err != nil {
			v.Verification = VerificationStale
			outcome.Status = "stale"
			outcome.Problems = append(outcome.Problems, verification.Problem{Code: "stale_evidence", Phase: "validation", Message: err.Error()})
			continue
		}
		if err := verification.ValidateLogs(e.Directory, e.Receipt); err != nil {
			v.Verification = VerificationStale
			outcome.Status = "stale"
			outcome.Problems = append(outcome.Problems, verification.Problem{Code: "invalid_logs", Phase: "validation", Message: err.Error()})
			continue
		}
		valid[e.Receipt.Check] = true
	}

	for _, name := range required {
		if !valid[name] {
			v.MissingChecks = append(v.MissingChecks, name)
		}
	}
	if len(v.MissingChecks) > 0 {
		v.Blockers = append(v.Blockers, "verify required checks")
	}
	if r.Candidate != nil && r.Verification == nil {
		if len(v.MissingChecks) == 0 {
			v.Blockers = append(v.Blockers, "verify the selected candidate")
		}
	}
	r.completeVerdict(v)
	return v, nil
}

func overlap(a, b, c, d int) bool { return a <= d && c <= b }
