package analysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"concave/internal/continuation"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

// A scoped comparison names its paths; a larger change is a repository audit.
const parityPathLimit = 1000

// Parity is the publication verdict for a candidate against the baseline it
// builds on. One rule decides it, whatever changed:
//
//   - The baseline source measured by the candidate's policy and Concave, against
//     the baseline's upstream pin, carries its findings. A candidate finding whose
//     identity it does not carry is a regression. Identities name content, so a
//     divergence an upstream merge left untouched stays carried.
//   - A location the baseline's own policy and Concave detected on that source,
//     and the candidate's no longer does, is coverage loss unless the candidate
//     policy explicitly approves that divergence.
//
// Either refuses publication. Discovered debt, carried findings newly detected
// on unchanged obligations, stays visible and unapproved.
type Parity struct {
	Format         int                 `json:"format"`
	Kind           string              `json:"kind"`
	Baseline       string              `json:"baseline"`
	Candidate      string              `json:"candidate"`
	Status         string              `json:"status"`
	Scope          ParityScope         `json:"scope"`
	Regressions    []policy.Finding    `json:"regressions"`
	CoverageLoss   []CoverageLoss      `json:"coverage_loss"`
	DiscoveredDebt []policy.Finding    `json:"discovered_debt"`
	Carried        int                 `json:"carried"`
	Resolved       int                 `json:"resolved"`
	Next           continuation.Action `json:"next"`
}

// ParityScope is either the changed paths or the repository, with the reasons
// a repository audit was required: policy, upstream, concave or size.
type ParityScope struct {
	Repository bool     `json:"repository"`
	Paths      []string `json:"paths"`
	Reasons    []string `json:"reasons"`
}

type ParityOptions struct {
	Baseline  string
	Candidate string
	// BaselineTool is Concave built from the baseline's own source. Empty, or
	// this executable, measures the baseline in process.
	BaselineTool string
}

func CheckParity(repo *repository.Repo, options ParityOptions) (*Parity, error) {
	baseline, err := repo.Resolve(options.Baseline)
	if err != nil {
		return nil, err
	}
	candidate, err := repo.Resolve(options.Candidate)
	if err != nil {
		return nil, err
	}
	if err = repo.Ancestor(baseline, candidate); err != nil {
		return nil, err
	}
	current, err := policy.Load(repo, candidate)
	if err != nil {
		return nil, err
	}
	tree, err := repo.Tree(baseline)
	if err != nil {
		return nil, err
	}
	var previous *policy.Config
	if _, ok := tree[policy.File]; ok {
		if previous, err = policy.Load(repo, baseline); err != nil {
			return nil, err
		}
	}
	tool, err := otherTool(options.BaselineTool)
	if err != nil {
		return nil, err
	}
	result := &Parity{Format: 1, Kind: "concave-parity", Baseline: baseline, Candidate: candidate, Status: "passed",
		Scope: ParityScope{Paths: []string{}, Reasons: []string{}}, Regressions: []policy.Finding{}, CoverageLoss: []CoverageLoss{}, DiscoveredDebt: []policy.Finding{}}
	scope := &result.Scope
	switch {
	case previous == nil:
		scope.Reasons = append(scope.Reasons, "policy")
	case previous.Upstream != current.Upstream:
		scope.Reasons = append(scope.Reasons, "upstream")
	}
	if tool != "" {
		scope.Reasons = append(scope.Reasons, "concave")
	}
	if len(scope.Reasons) == 0 {
		paths, full, err := changedPaths(repo, previous, current, baseline, candidate, false)
		if err != nil {
			return nil, err
		}
		switch {
		case full:
			scope.Reasons = append(scope.Reasons, "policy")
		case len(paths) > parityPathLimit:
			scope.Reasons = append(scope.Reasons, "size")
		default:
			// Documents importing a changed partial or snippet carry its bytes
			// in their identities; the comparison measures them too.
			importers, err := DocumentImporters(repo, current, candidate, paths)
			if err != nil {
				return nil, err
			}
			scope.Paths = append(paths, importers...)
			slices.Sort(scope.Paths)
		}
	}
	scope.Repository = len(scope.Reasons) > 0
	if !scope.Repository && len(scope.Paths) == 0 {
		result.Next = result.action(repo.Root)
		return result, nil
	}
	// Independent measurements run concurrently, each with its own repository
	// reader and engines over read-only policies; the baseline's own Concave is
	// a child process.
	type measurement struct {
		report *Report
		err    error
	}
	measure := func(revision string, config *policy.Config) <-chan measurement {
		result := make(chan measurement, 1)
		go func() {
			report, err := func() (*Report, error) {
				reader, err := repository.Open(repo.Context, repo.Root)
				if err != nil {
					return nil, err
				}
				defer reader.Close()
				o := Options{Rev: revision}
				if !scope.Repository {
					o.Paths, o.ComparisonPaths = scope.Paths, scope.Paths
				}
				return Analyze(reader, config, o)
			}()
			result <- measurement{report, err}
		}()
		return result
	}
	// The candidate's rules, measured where the baseline stood.
	measuring := *current
	if previous != nil {
		measuring.Upstream = previous.Upstream
	}
	pending := []<-chan measurement{measure(candidate, current), measure(baseline, &measuring)}
	switch {
	case tool != "" && previous != nil:
		own := make(chan measurement, 1)
		go func() {
			report, err := baselineInspection(repo, tool, baseline)
			own <- measurement{report, err}
		}()
		pending = append(pending, own)
	case previous != nil && previous.SHA256 != current.SHA256:
		pending = append(pending, measure(baseline, previous))
	}
	reports := make([]*Report, len(pending))
	var failure error
	for i, channel := range pending {
		measured := <-channel
		failure = errors.Join(failure, measured.err)
		reports[i] = measured.report
	}
	if failure != nil {
		return nil, failure
	}
	after, carried := reports[0], reports[1]
	var before *Report
	if len(reports) > 2 {
		before = reports[2]
	}
	carriedFindings := carried.Findings
	regroupedBefore, regroupedAfter := coveredRegroupings(carried, after, repo.Blob)
	result.Regressions = slices.DeleteFunc(difference(after.Findings, carriedFindings, policy.Finding.Identity), func(f policy.Finding) bool {
		return regroupedAfter[f.Identity()]
	})
	result.Carried = len(after.Findings) - len(result.Regressions)
	result.Resolved = len(slices.DeleteFunc(difference(carriedFindings, after.Findings, policy.Finding.Identity), func(f policy.Finding) bool {
		return regroupedBefore[f.Identity()]
	}))
	if previous != nil {
		result.CoverageLoss = retiredLoss(previous, current, after)
	}
	if before != nil {
		result.CoverageLoss = append(result.CoverageLoss, coverageLoss(previous, current, before, carried)...)
		sortLosses(result.CoverageLoss)
		// A rule newly applied to a location, never a reworded or re-subjected one.
		discovered := difference(carriedFindings, before.Findings, ruleLocation)
		discovered = difference(discovered, difference(discovered, after.Findings, policy.Finding.Identity), policy.Finding.Identity)
		// Debt is discovered only where the candidate left the explanation as
		// it was. A finding newly detected where the candidate changed a
		// binding, transformation or derived entry is the contribution's own:
		// reverting a refreshed binding makes it stale on unchanged source.
		changed := changedExplanations(previous, current)
		for _, finding := range discovered {
			if changed.affects(finding) {
				result.Regressions = append(result.Regressions, finding)
			} else {
				result.DiscoveredDebt = append(result.DiscoveredDebt, finding)
			}
		}
	}
	sortFindings(result.Regressions)
	sortFindings(result.DiscoveredDebt)
	if len(result.Regressions) > 0 || len(result.CoverageLoss) > 0 {
		result.Status = "failed"
	}
	result.Next = result.action(repo.Root)
	return result, nil
}

// otherTool returns the baseline's Concave unless it is this executable.
func otherTool(tool string) (string, error) {
	if tool == "" {
		return "", nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	mine, err := os.Stat(self)
	if err != nil {
		return "", err
	}
	theirs, err := os.Stat(tool)
	if err != nil {
		return "", fmt.Errorf("baseline Concave: %w", err)
	}
	if os.SameFile(mine, theirs) {
		return "", nil
	}
	return filepath.Abs(tool)
}

// The baseline Concave's own repository audit of its revision, under that
// revision's policy. Only coordinates every version reports are read: rows,
// documents and findings. Policy identity is each version's own encoding of
// its policy inputs, so this version never recomputes the baseline's.
func baselineInspection(repo *repository.Repo, tool, revision string) (*Report, error) {
	run, err := process.Run(repo.Context, repo.Root, []string{tool, "--repo", repo.Root, "check", "--rev", revision, "--json"}, nil, 512<<20)
	var report Report
	if decode := json.Unmarshal(run.Stdout, &report); decode != nil {
		return nil, fmt.Errorf("baseline Concave report: %w", decode)
	}
	var exit *exec.ExitError
	accepted := err == nil && run.ExitCode == 0 || errors.As(err, &exit) && run.ExitCode == 1
	if !accepted && errors.As(err, &exit) && run.ExitCode == 2 {
		// Old readers appended '/' to an explicitly declared file root. This
		// metadata error may be repaired only while the exact file still exists;
		// every unsupported source or genuinely missing selector still refuses.
		config, load := policy.Load(repo, revision)
		tree, read := repo.Tree(revision)
		if load == nil && read == nil {
			seen := false
			accepted = true
			for _, finding := range report.Findings {
				if finding.Kind != "unmatched_selector" && finding.Kind != "unsupported_change" {
					continue
				}
				path := strings.TrimSuffix(finding.Path, "/")
				entry, exists := tree[path]
				if finding.Kind != "unmatched_selector" || finding.Path == path || !exists ||
					(entry.Mode != "100644" && entry.Mode != "100755") || !slices.Contains(config.RootSelectors(), path) {
					accepted = false
				}
				seen = true
			}
			accepted = accepted && seen
		}
	}
	if !accepted {
		return nil, fmt.Errorf("baseline Concave check failed (%d): %v\n%s", run.ExitCode, err, tail(run.Stderr))
	}
	if report.Format != 1 || report.Kind != "concave-inspection" || report.Revision != revision || report.PolicySHA256 == "" ||
		(report.Status == "passed") != (run.ExitCode == 0) || report.Findings == nil {
		return nil, fmt.Errorf("baseline Concave report does not describe %s under its own policy", revision)
	}
	return &report, nil
}

func tail(data []byte) string {
	if len(data) > 4000 {
		data = data[len(data)-4000:]
	}
	return string(data)
}

// changedExplanations names every path, and every binding ID ("\x00" + ID),
// whose binding, transformation or derived declaration differs between the
// baseline and candidate policies.
type changedExplanationScope struct {
	paths map[string]bool
	ids   map[string]bool
	spans map[string][]policy.Span
}

func (changed changedExplanationScope) affects(finding policy.Finding) bool {
	if changed.paths[finding.Path] || finding.Exception != "" && changed.ids[finding.Exception] {
		return true
	}
	if finding.Exception != "" || finding.Upstream == nil {
		return false
	}
	for _, span := range changed.spans[finding.Path] {
		if policy.ConflictingSpans(span, *finding.Upstream) {
			return true
		}
	}
	return false
}

func changedExplanations(previous, current *policy.Config) changedExplanationScope {
	changed := changedExplanationScope{paths: map[string]bool{}, ids: map[string]bool{}, spans: map[string][]policy.Span{}}
	bindingChanged := func(binding policy.Exception) {
		changed.ids[binding.ID] = true
		if binding.File != nil {
			changed.paths[binding.Path] = true
		} else {
			changed.spans[binding.Path] = append(changed.spans[binding.Path], policy.Span{Start: binding.Start, End: binding.End})
		}
	}
	bindings := map[string]policy.Exception{}
	for _, binding := range previous.Exceptions {
		bindings[binding.ID] = binding
	}
	for _, binding := range current.Exceptions {
		if old, ok := bindings[binding.ID]; !ok || !reflect.DeepEqual(old, binding) {
			bindingChanged(binding)
			if ok {
				bindingChanged(old)
			}
		}
		delete(bindings, binding.ID)
	}
	for _, binding := range bindings {
		bindingChanged(binding)
	}
	transforms := map[string]policy.Transform{}
	for _, transform := range previous.Transforms {
		transforms[transform.ID] = transform
	}
	for _, transform := range current.Transforms {
		if old, ok := transforms[transform.ID]; !ok || !reflect.DeepEqual(old, transform) {
			for _, p := range append(slices.Clone(transform.Paths), old.Paths...) {
				changed.paths[p] = true
			}
		}
		delete(transforms, transform.ID)
	}
	for _, transform := range transforms {
		for _, p := range transform.Paths {
			changed.paths[p] = true
		}
	}
	derived := map[string]policy.Derived{}
	for _, entry := range previous.Derived {
		derived[entry.ID] = entry
	}
	for _, entry := range current.Derived {
		if old, ok := derived[entry.ID]; !ok || !reflect.DeepEqual(old, entry) {
			for _, p := range append(slices.Clone(entry.Paths), old.Paths...) {
				changed.paths[p] = true
			}
		}
		delete(derived, entry.ID)
	}
	for _, entry := range derived {
		for _, p := range entry.Paths {
			changed.paths[p] = true
		}
	}
	return changed
}

// retiredBindings are the baseline's bindings the candidate policy no longer has.
func retiredBindings(previous, current *policy.Config) map[string]policy.Exception {
	kept := map[string]bool{}
	for _, binding := range current.Exceptions {
		kept[binding.ID] = true
	}
	retired := map[string]policy.Exception{}
	for _, binding := range previous.Exceptions {
		if !kept[binding.ID] {
			retired[binding.ID] = binding
		}
	}
	return retired
}

// retiredLoss decides a retired binding, stale or valid, on the candidate's own
// source: its span must be upstream-identical or explained by another approval.
// A finding left on that span means the divergence it covered remains
// unexplained, and its retirement removed coverage rather than debt.
func retiredLoss(previous, current *policy.Config, candidate *Report) []CoverageLoss {
	losses := []CoverageLoss{}
	for _, binding := range retiredBindings(previous, current) {
		for _, finding := range candidate.Findings {
			if finding.Path != binding.Path {
				continue
			}
			if binding.File == nil && (finding.Upstream == nil || !policy.ConflictingSpans(*finding.Upstream, policy.Span{Start: binding.Start, End: binding.End})) {
				continue
			}
			losses = append(losses, CoverageLoss{binding.Path, "binding " + binding.ID + " retired while its divergence remains unexplained: restore upstream source or approve it"})
			break
		}
	}
	sortLosses(losses)
	return losses
}

// coverageLoss compares two measurements of the same baseline source. A
// retired binding's own findings are decided by retiredLoss instead.
func coverageLoss(previous, current *policy.Config, before, after *Report) []CoverageLoss {
	retired := retiredBindings(previous, current)
	losses := lostCoverage(before, after)
	rows := map[string]Row{}
	for _, row := range after.Files {
		rows[row.Path] = row
	}
	for _, row := range before.Files {
		if previous.Exact(row.Path) && !current.Exact(row.Path) {
			losses = append(losses, CoverageLoss{row.Path, "exact preservation requirement removed"})
		}
	}
	// A hunk the candidate policy now covers is an explicitly approved
	// divergence even while other hunks leave its file unexplained.
	approvedHunks := map[string]bool{}
	for _, h := range after.Hunks {
		if h.Approval != "" {
			approvedHunks[policy.Finding{Path: h.Path, Upstream: &policy.Span{Start: h.Start, End: h.End}}.Location()] = true
		}
	}
	for _, finding := range difference(before.Findings, after.Findings, policy.Finding.Location) {
		if _, ok := retired[finding.Exception]; ok && finding.Exception != "" {
			continue
		}
		if finding.Kind == "unmatched_selector" && strings.HasSuffix(finding.Path, "/") {
			path := strings.TrimSuffix(finding.Path, "/")
			if row, ok := rows[path]; ok && row.Selected != nil && (row.Selected.Mode == "100644" || row.Selected.Mode == "100755") &&
				slices.Contains(previous.RootSelectors(), path) && slices.Contains(current.RootSelectors(), path) {
				// The metadata error is gone; the same baseline file is now read.
				// lostCoverage above still refuses any genuinely dropped row.
				continue
			}
		}
		row, ok := rows[finding.Path]
		if ok && approvedDivergence(finding, row, current) || finding.Rule == "source/covered" && approvedHunks[finding.Location()] {
			continue
		}
		losses = append(losses, CoverageLoss{finding.Path, "no longer detected on the baseline source: " + finding.Rule})
	}
	sortLosses(losses)
	return losses
}

func lostCoverage(before, after *Report) []CoverageLoss {
	rows := map[string]Row{}
	for _, row := range after.Files {
		rows[row.Path] = row
	}
	losses := []CoverageLoss{}
	for _, row := range before.Files {
		next, ok := rows[row.Path]
		if !ok {
			losses = append(losses, CoverageLoss{row.Path, "path no longer inspected"})
			continue
		}
		if row.UpstreamPath != "" && row.UpstreamPath != next.UpstreamPath || row.Upstream != nil && next.Upstream == nil {
			losses = append(losses, CoverageLoss{row.Path, "upstream correspondence removed or changed"})
		}
	}
	documents := map[string]bool{}
	for _, doc := range after.Documents {
		documents[doc.Path] = true
	}
	for _, doc := range before.Documents {
		if !documents[doc.Path] {
			losses = append(losses, CoverageLoss{doc.Path, "document dependencies no longer inspected"})
		}
	}
	sortLosses(losses)
	return losses
}

func sortLosses(losses []CoverageLoss) {
	sort.Slice(losses, func(i, j int) bool {
		if losses[i].Path != losses[j].Path {
			return losses[i].Path < losses[j].Path
		}
		return losses[i].Reason < losses[j].Reason
	})
}

// An explicit source approval discharges divergence from upstream: the whole
// file's, or the span an applied binding of the candidate policy covers even
// beside other debt in that file. It cannot excuse lost contract, document or
// reference checks.
func approvedDivergence(finding policy.Finding, row Row, current *policy.Config) bool {
	if len(row.Approved) == 0 {
		return false
	}
	switch {
	case finding.Rule == "source/covered", finding.Rule == "source/exact":
	case finding.Kind == "unsupported_change", finding.Kind == "obsolete_exception", finding.Kind == "stale_exception", finding.Kind == "unknown_fence":
	default:
		return false
	}
	if row.Classification == "transformed" || row.Classification == "excepted" || row.Classification == "derived" {
		return true
	}
	if finding.Upstream == nil {
		return false
	}
	for _, binding := range current.Exceptions {
		if binding.Path == finding.Path && binding.File == nil && slices.Contains(row.Approved, binding.ID) && policy.ConflictingSpans(*finding.Upstream, policy.Span{Start: binding.Start, End: binding.End}) {
			return true
		}
	}
	return false
}

func ruleLocation(f policy.Finding) string { return f.Rule + "\x00" + f.Location() }

// difference is the multiset a - b under key; duplicate occurrences count.
func difference(a, b []policy.Finding, key func(policy.Finding) string) []policy.Finding {
	counts := map[string]int{}
	for _, finding := range b {
		counts[key(finding)]++
	}
	result := []policy.Finding{}
	for _, finding := range a {
		k := key(finding)
		if counts[k] > 0 {
			counts[k]--
			continue
		}
		result = append(result, finding)
	}
	return result
}

func sortFindings(findings []policy.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
}

// action is the verdict's one next step, the same for terminal and JSON readers.
func (p *Parity) action(root string) continuation.Action {
	if len(p.Regressions) > 0 {
		finding := p.Regressions[0]
		report := Report{Revision: p.Candidate, Findings: []policy.Finding{finding}}
		next := report.CheckAction(root, nil)
		if next.Kind == continuation.Inspect {
			next.Message = "Inspect the first regression; restore the upstream lines or record a reviewed exception."
		}
		return next
	}
	if len(p.CoverageLoss) > 0 {
		argv := []string{"concave", "check", "--rev", p.Baseline}
		if path := p.CoverageLoss[0].Path; path != "" {
			argv = append(argv, path)
		}
		return continuation.Action{Kind: continuation.Inspect, Message: "Restore the policy or Concave coverage this candidate drops; the baseline's own check shows it.", Argv: argv}.InRepository(root)
	}
	return continuation.Action{Kind: continuation.Publish, Message: "No regression or coverage loss; publish."}
}
