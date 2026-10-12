// Package analysis produces one immutable report consumed by the CLI and recovery.
package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"concave/internal/continuation"
	"concave/internal/contracts"
	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type Options struct {
	CallerPaths  []string
	Rev          string
	Paths        []string
	Family       string
	Case         string
	Metric       string
	Diagnose     bool
	HistoryPairs []HistoryPair
	History      bool
	Contracts    bool
	Location     *Location
	// Comparisons inspect a verified union of paths on both sides, including an
	// empty side after deletion. Ordinary selectors never bypass scope checks.
	ComparisonPaths []string
}
type Counts struct {
	Inherited int `json:"inherited_files"`
	Exact     int `json:"unchanged_files"`
	Removed   int `json:"removed_upstream_lines"`
	Added     int `json:"added_inherited_lines"`
	Missing   int `json:"missing_files"`
	Binary    int `json:"binary_changes"`
	New       int `json:"new_files"`
	NewLines  int `json:"added_new_file_lines"`
}

// Coverage distinguishes a repository audit from a deliberately narrowed view.
// Configured roots select additional fork-owned inventory, never exempt inherited
// paths from a full audit.
type Coverage struct {
	Scope             string `json:"scope"`
	UpstreamFiles     int    `json:"upstream_files"`
	Inspected         int    `json:"inspected_inherited_files"`
	Uninspected       int    `json:"uninspected_inherited_files"`
	OutsideConfigured int    `json:"inherited_outside_configured_paths"`
}
type Row struct {
	UpstreamPath         string                        `json:"upstream_path,omitempty"`
	AnnotationFreeSHA256 string                        `json:"annotation_free_sha256,omitempty"`
	Path                 string                        `json:"path"`
	Family               string                        `json:"family,omitempty"`
	Surfaces             []string                      `json:"surfaces,omitempty"`
	Status               string                        `json:"status"`
	Classification       string                        `json:"classification"`
	Upstream             *repository.State             `json:"upstream"`
	Selected             *repository.State             `json:"selected"`
	Head                 *repository.State             `json:"head"`
	Working              *repository.State             `json:"working"`
	Added                int                           `json:"added"`
	Removed              int                           `json:"removed"`
	Approved             []string                      `json:"approved"`
	Explanations         map[string]policy.Explanation `json:"explanations,omitempty"`
	HistoricalMatches    []HistoricalMatch             `json:"historical_matches,omitempty"`
	IdenticalNewPaths    []string                      `json:"identical_new_paths,omitempty"`
	Derived              *policy.Derived               `json:"derived,omitempty"`
	RecoverySupported    bool                          `json:"recovery_supported"`
}

// HistoricalMatch is byte-and-location evidence, not test execution evidence.
// Source is the deleted range in the removal commit's single parent.
type HistoricalMatch struct {
	Rule      string      `json:"rule"`
	Hunk      string      `json:"hunk"`
	RemovedBy string      `json:"removed_by"`
	Source    policy.Span `json:"source"`
}
type Observation struct {
	Probe   string `json:"probe"`
	Path    string `json:"path"`
	Version string `json:"version"`
	Line    int    `json:"line"`
	Text    string `json:"text"`
	Meaning string `json:"meaning"`
}
type Report struct {
	Documents            []Document           `json:"documents,omitempty"`
	DocumentEngine       string               `json:"document_engine,omitempty"`
	DocumentInputs       repository.Inputs    `json:"document_inputs,omitempty"`
	Next                 *continuation.Action `json:"next,omitempty"`
	Format               int                  `json:"format"`
	Kind                 string               `json:"kind"`
	Upstream             string               `json:"upstream"`
	Revision             string               `json:"revision"`
	Head                 string               `json:"head"`
	PolicySHA256         string               `json:"policy_sha256"`
	SyntaxEngine         string               `json:"syntax_engine"`
	PolicySource         string               `json:"policy_source"`
	SnapshotSHA256       string               `json:"snapshot_sha256"`
	ContractInputsSHA256 string               `json:"contract_inputs_sha256"`
	Scope                OptionsJSON          `json:"scope"`
	Coverage             Coverage             `json:"coverage"`
	Totals               Counts               `json:"totals"`
	Families             map[string]*Counts   `json:"families"`
	Files                []Row                `json:"files"`
	Hunks                []repository.Hunk    `json:"hunks"`
	History              []HistoricalBlock    `json:"history,omitempty"`
	Findings             []policy.Finding     `json:"findings"`
	Probes               []Observation        `json:"probes"`
	Untracked            []string             `json:"untracked_paths"`
	Unmatched            []string             `json:"unmatched_selectors"`
	ReviewOrder          []string             `json:"review_order,omitempty"`
	Verification         []string             `json:"verification,omitempty"`
	ReviewPatches        map[string]string    `json:"review_patches"`
	Metric               *Metric              `json:"metric,omitempty"`
	Status               string               `json:"status"`
	BehavioralParity     string               `json:"behavioral_parity"`
	Limits               []string             `json:"limits"`
	Contracts            *ContractContext     `json:"contracts,omitempty"`
}
type OptionsJSON struct {
	HistoryPairs []HistoryPair `json:"history_pairs,omitempty"`
	Paths        []string      `json:"paths"`
	Family       string        `json:"family,omitempty"`
	Case         string        `json:"case,omitempty"`
	History      bool          `json:"history,omitempty"`
}

func union(trees ...map[string]repository.Entry) []string {
	m := map[string]bool{}
	for _, t := range trees {
		for p := range t {
			m[p] = true
		}
	}
	out := []string{}
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func scopes(c *policy.Config, o Options) ([]string, error) {
	if o.ComparisonPaths != nil {
		for _, path := range o.ComparisonPaths {
			if !repository.ValidPath(path) {
				return nil, fmt.Errorf("unsafe comparison path %q", path)
			}
		}
		return o.ComparisonPaths, nil
	}
	s := append(c.RootSelectors(), c.Boundaries...)
	if o.Case != "" {
		k, ok := c.Cases[o.Case]
		if !ok {
			return nil, fmt.Errorf("unknown case %q", o.Case)
		}
		s = nil
		for _, v := range k.Surfaces {
			s = append(s, v.Paths...)
		}
	}
	if len(o.Paths) > 0 {
		s = o.Paths
	}
	if o.Family != "" {
		found := false
		for _, f := range c.Families {
			if f.ID == o.Family {
				found = true
				if o.Case == "" && len(o.Paths) == 0 {
					s = f.Paths
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown family %q", o.Family)
		}
	}
	for _, p := range s {
		if !repository.ValidPath(p) {
			return nil, fmt.Errorf("unsafe selector %q", p)
		}
	}
	return s, nil
}
func blob(r *repository.Repo, t map[string]repository.Entry, p string) (*repository.State, []byte, error) {
	e, ok := t[p]
	if !ok {
		return nil, nil, nil
	}
	if e.Kind != "blob" {
		return repository.BlobState(e, nil), nil, nil
	}
	b, err := r.Blob(e.OID)
	if err != nil {
		return nil, nil, err
	}
	state, err := r.BlobState(e, b)
	return state, b, err
}
func Analyze(repo *repository.Repo, config *policy.Config, options Options) (*Report, error) {
	return analyze(repo, config, options, policy.NewSession(repo, config))
}

// A scoped check shares parser caches with its importer discovery. All source
// reads and freshness validation remain owned by the analysis snapshot.
func analyze(repo *repository.Repo, config *policy.Config, options Options, session *policy.Session) (*Report, error) {
	defer process.Phase(repo.Context, "analysis", "")()
	if err := normalizeHistoryPairs(&options); err != nil {
		return nil, err
	}
	selectors, e := scopes(config, options)
	if e != nil {
		return nil, e
	}
	head, e := repo.Resolve("HEAD")
	if e != nil {
		return nil, e
	}
	rev := options.Rev
	if rev != "" {
		rev, e = repo.Resolve(rev)
		if e != nil {
			return nil, e
		}
	}
	base, e := repo.Resolve(config.Upstream)
	if e != nil {
		return nil, e
	}
	target := rev
	if target == "" {
		target = head
	}
	if e = repo.Ancestor(base, target); e != nil {
		return nil, e
	}
	bt, e := config.UpstreamTree(repo)
	if e != nil {
		return nil, e
	}
	ct, e := repo.Tree(target)
	if e != nil {
		return nil, e
	}
	ht := ct
	if target != head {
		ht, e = repo.Tree(head)
		if e != nil {
			return nil, e
		}
	}
	// Explicit source/contract discovery includes new working files. Ordinary
	// inventory retains its established untracked-path-only behavior.
	if rev == "" && (options.Contracts || options.Location != nil || len(options.Paths) > 0) {
		untracked, err := repo.Untracked()
		if err != nil {
			return nil, err
		}
		expanded := make(map[string]repository.Entry, len(ct)+len(untracked))
		for path, entry := range ct {
			expanded[path] = entry
		}
		for _, path := range untracked {
			if repository.Matches(path, selectors) {
				expanded[path] = repository.Entry{Mode: "100644", Kind: "blob"}
			}
		}
		ct = expanded
	}
	for _, p := range config.PreserveExactly {
		if _, ok := bt[p]; !ok {
			return nil, fmt.Errorf("protected file absent from upstream: %s", p)
		}
	}
	for _, x := range config.Exceptions {
		if _, ok := bt[x.Path]; !ok {
			return nil, fmt.Errorf("exception %s names absent upstream path %s", x.ID, x.Path)
		}
	}
	for _, x := range config.Transforms {
		for _, path := range x.Paths {
			if _, ok := bt[path]; !ok {
				return nil, fmt.Errorf("transform %s names absent upstream path %s", x.ID, path)
			}
		}
	}
	for _, x := range config.Derived {
		for _, path := range x.Paths {
			if _, ok := bt[path]; !ok {
				return nil, fmt.Errorf("derived %s names absent upstream path %s", x.ID, path)
			}
		}
		for _, source := range x.Sources {
			found := false
			for path := range ct {
				if repository.Matches(path, []string{source}) {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("derived %s source %s matches no selected path", x.ID, source)
			}
		}
	}
	report := &Report{Format: 1, Kind: "concave-inspection", Upstream: base, Revision: rev, Head: head, PolicySHA256: config.SHA256, PolicySource: "candidate", Scope: OptionsJSON{Paths: options.Paths, Family: options.Family, Case: options.Case, History: options.History, HistoryPairs: options.HistoryPairs}, Families: map[string]*Counts{}, Files: []Row{}, Hunks: []repository.Hunk{}, Findings: []policy.Finding{}, Probes: []Observation{}, Untracked: []string{}, Unmatched: []string{}, ReviewPatches: map[string]string{}, Status: "inspected", BehavioralParity: "not_established", Limits: []string{"Scope is explicit; it is not an inferred complete call graph.", "Untracked files are listed without reading contents. Ignored build outputs are not qualified.", "Binary replacements and retired paths need explicit file bindings; oversized or nonregular changes prevent a complete check.", "Source probes and test references do not establish executed behavior."}}
	if rev == "" && (options.Contracts || options.Location != nil || len(options.Paths) > 0) {
		report.Limits[1] = "Selected untracked files are inspected. Ignored build outputs are not qualified."
	}
	trees := []map[string]repository.Entry{bt, ct}
	if options.Case != "" {
		trees = append(trees, ht)
	}
	paths := []string{}
	fullScope := len(options.Paths) == 0 && options.Family == "" && options.Case == "" && (options.ComparisonPaths == nil || len(options.ComparisonPaths) > 0)
	report.Coverage.Scope = "selected"
	if fullScope {
		report.Coverage.Scope = "repository"
	}
	configured := append(config.RootSelectors(), config.Boundaries...)
	for p := range bt {
		report.Coverage.UpstreamFiles++
		if !repository.Matches(p, configured) {
			report.Coverage.OutsideConfigured++
		}
	}
	for _, p := range union(trees...) {
		_, inherited := bt[p]
		if !repository.Matches(p, selectors) && !(fullScope && inherited) {
			continue
		}
		if options.Family != "" && options.ComparisonPaths == nil {
			f, err := config.Family(p)
			if err != nil || f != options.Family {
				continue
			}
		}
		paths = append(paths, p)
		if inherited {
			report.Coverage.Inspected++
		}
	}
	report.Coverage.Uninspected = report.Coverage.UpstreamFiles - report.Coverage.Inspected
	if len(paths) == 0 && options.ComparisonPaths == nil {
		return nil, fmt.Errorf("empty inspection scope")
	}
	for _, s := range selectors {
		found := false
		for _, p := range paths {
			if repository.Matches(p, []string{s}) {
				found = true
				break
			}
		}
		if !found && options.ComparisonPaths == nil {
			report.Unmatched = append(report.Unmatched, s)
		}
	}
	contents := map[string]sourcePair{}
	work := map[string]*repository.State{}
	diffSources := map[string]repository.SourcePair{}
	retainedBytes := 0
	annotatedSources := map[string]string{}
	reader := repo.Reader()
	defer reader.Close()
	for _, p := range paths {
		be, ce := bt[p], ct[p]
		row := Row{Path: p, Status: "identical", Classification: "preserved", Approved: []string{}}
		if original := config.UpstreamPath(p); original != p {
			row.UpstreamPath = original
		}
		if be.OID != "" {
			row.Family, e = config.Family(p)
			if e != nil {
				if repository.Matches(p, config.RootSelectors()) {
					return nil, e
				}
				row.Family = "repository"
				if repository.Matches(p, config.Boundaries) {
					row.Family = "boundaries"
				}
			}
		} else {
			row.Family = "fork-owned"
		}
		if options.Case != "" {
			for _, s := range config.Cases[options.Case].Surfaces {
				if repository.Matches(p, s.Paths) {
					row.Surfaces = append(row.Surfaces, s.ID)
				}
			}
		}
		var a, b []byte
		var ws *repository.State
		if rev == "" || options.Case != "" || (options.Diagnose && (be.OID != "" && be != ce || len(options.Paths) > 0)) {
			ws, b, e = reader.Snapshot(p)
			if e != nil {
				return nil, e
			}
			work[p] = ws
		}
		changed := be != ce
		if rev == "" {
			changed = be.OID == "" && ws != nil || be.OID != "" && (ws == nil || ws.OID != be.OID || ws.Mode != be.Mode)
		}
		if !changed && be.OID != "" && options.Case == "" && !options.History && !hasRule(config, p) {
			row.Upstream = &repository.State{Kind: be.Kind, Mode: be.Mode, OID: be.OID}
			row.Selected = row.Upstream
			if rev == "" {
				row.Working = ws
			}
			report.Files = append(report.Files, row)
			continue
		}
		row.Upstream, a, e = blob(repo, bt, p)
		if e != nil {
			return nil, e
		}
		if rev != "" {
			row.Selected, b, e = blob(repo, ct, p)
		} else {
			row.Selected = ws
		}
		if e != nil {
			return nil, e
		}
		row.Working = ws
		if row.Selected != nil && row.Selected.Kind == "file" && repository.Text(b) {
			row.AnnotationFreeSHA256 = row.Selected.SHA256
			if bytes.Contains(b, []byte("concave:")) {
				annotatedSources[p] = string(b)
			}
		}
		if row.Upstream == nil && row.Selected == nil {
			continue
		}
		if options.Case != "" {
			var hb []byte
			row.Head, hb, e = blob(repo, ht, p)
			if e != nil {
				return nil, e
			}
			if rev != "" {
				_, b, e = blob(repo, ct, p)
				if e != nil {
					return nil, e
				}
			}
			for _, probe := range config.Cases[options.Case].Probes {
				if probe.Path != p {
					continue
				}
				wb := []byte(nil)
				if ws != nil {
					_, wb, e = reader.Snapshot(p)
					if e != nil {
						return nil, e
					}
				}
				for _, v := range []struct {
					name string
					data []byte
				}{{"upstream", a}, {"selected", b}, {"head", hb}, {"working", wb}} {
					for i, line := range repository.Lines(string(v.data)) {
						for _, needle := range probe.Needles {
							if strings.Contains(line, needle) {
								report.Probes = append(report.Probes, Observation{probe.ID, p, v.name, i + 1, strings.TrimSuffix(line, "\n"), probe.Meaning})
								break
							}
						}
					}
				}
			}
		}
		fileApproved := false
		if x := config.FileException(p); x != nil {
			_, ids, _ := x.ClassifyFile(row.Upstream, row.Selected, a, b)
			fileApproved = len(ids) > 0
		}
		if row.Upstream == nil {
			row.Status = "fork-only"
			row.Classification = "fork_owned"
		} else if row.Selected == nil {
			row.Status = "missing"
			row.Classification = "unexplained"
			if !fileApproved {
				report.Findings = append(report.Findings, policy.Finding{Kind: "missing_file", Path: p, Message: "original path is absent"})
			}
		} else if changed {
			row.Status = "modified"
			row.Classification = "unexplained"
		}
		if row.Upstream != nil && row.Selected != nil && row.Upstream.Mode != row.Selected.Mode {
			report.Findings = append(report.Findings, policy.Finding{Kind: "mode_or_type_changed", Path: p, Subject: policy.Subject(row.Selected.Mode, row.Selected.Kind), Message: "original file mode or type changed"})
		}
		if changed && be.OID != "" && row.Selected != nil && !fileApproved && (!repository.Text(a) || !repository.Text(b)) {
			report.Findings = append(report.Findings, policy.Finding{Kind: "unsupported_change", Path: p, Subject: policy.Subject(row.Selected.Mode, row.Selected.OID), Message: "changed binary, oversized or nonregular source requires explicit review"})
		}
		row.RecoverySupported = row.Status == "modified" && row.Upstream != nil && row.Selected != nil && row.Upstream.Mode == "100644" && row.Selected.Mode == "100644" && repository.Text(a) && repository.Text(b)
		if changed && row.Upstream != nil && repository.Text(a) && (row.Selected == nil || repository.Text(b)) {
			after := b
			if row.Selected == nil {
				after = []byte{}
			}
			diffSources[p] = repository.SourcePair{Before: a, After: after}
		}
		if row.Status == "fork-only" && b != nil && !bytes.ContainsRune(b, 0) {
			row.Added = len(repository.Lines(string(b)))
		}
		if row.Upstream == nil {
			report.Files = append(report.Files, row)
			continue
		}
		retainedBytes += len(a) + len(b)
		if retainedBytes > repository.MaxOutput {
			return nil, fmt.Errorf("analysis text exceeds %d bytes; select a family", repository.MaxOutput)
		}
		contents[p] = sourcePair{a, b}
		report.Files = append(report.Files, row)
	}
	documents := []document.Source{}
	for path, source := range annotatedSources {
		if document.Supports(path) {
			documents = append(documents, document.Source{Path: path, Source: source})
		}
	}
	if len(documents) > 0 {
		report.DocumentEngine = document.Identity()
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Path < documents[j].Path })
	if err := session.Documents.Batch(documents); err != nil {
		return nil, err
	}
	if err := session.PrepareComments(annotatedSources); err != nil {
		return nil, err
	}
	for i := range report.Files {
		row := &report.Files[i]
		if source, ok := annotatedSources[row.Path]; ok {
			if stripped, valid := session.AnnotationFree(row.Path, source); valid {
				row.AnnotationFreeSHA256 = repository.Digest([]byte(stripped))
			} else {
				row.AnnotationFreeSHA256 = ""
			}
		}
	}
	diffs := map[string][]repository.Hunk{}
	if len(diffSources) > 0 {
		// Compare the captured bytes, never Git-cleaned working content. Git
		// attributes and CRLF normalization cannot hide or relocate source edits.
		diffs, e = repo.DiffSources(diffSources)
		if e != nil {
			return nil, e
		}
	}
	references := map[string][]byte{}
	observedReferences := repository.Inputs{}
	var history *[]HistoricalBlock
	if options.History {
		report.History = []HistoricalBlock{}
		history = &report.History
	}
	historical, e := restorationCandidates(repo, config, base, contents, history)
	if e != nil {
		return nil, e
	}
	if options.History {
		if e = compareHistory(repo, report.History, contents, diffs, options.HistoryPairs); e != nil {
			return nil, e
		}
	}
	engine := session.Syntax
	transformed, e := transformationHunks(repo, config, contents, engine)
	if e != nil {
		return nil, e
	}
	readReference := func(path string) ([]byte, error) {
		if data, ok := references[path]; ok {
			return data, nil
		}
		if source, ok := contents[path]; ok && repository.Text(source.b) {
			references[path] = source.b
			return source.b, nil
		}
		var data []byte
		if rev == "" {
			state, b, err := repo.Snapshot(path)
			if err != nil {
				return nil, err
			}
			observedReferences[path] = state
			if state == nil || state.Kind != "file" {
				return nil, fmt.Errorf("missing regular text file")
			}
			if previous, ok := work[path]; ok && !repository.Same(previous, state) {
				return nil, &repository.Stale{Message: "reference changed during inspection"}
			}
			work[path] = state
			data = b
		} else {
			entry, ok := ct[path]
			if !ok || (entry.Mode != "100644" && entry.Mode != "100755") {
				observedReferences[path] = nil
				return nil, fmt.Errorf("missing regular text file")
			}
			b, err := repo.Blob(entry.OID)
			if err != nil {
				return nil, err
			}
			data = b
		}
		if !repository.Text(data) {
			return nil, fmt.Errorf("reference is not bounded UTF-8 text")
		}
		retainedBytes += len(data)
		if retainedBytes > repository.MaxOutput {
			return nil, fmt.Errorf("source and references exceed memory bound")
		}
		references[path] = data
		return data, nil
	}
	if err := inspectDocuments(repo, session, report, readReference); err != nil {
		return nil, err
	}
	refs := contracts.New(readReference, engine)
	for i := range report.Files {
		row := &report.Files[i]
		d := contents[row.Path]
		raw := diffs[row.Path]
		if bytes.Contains(d.b, []byte("concave:")) {
			fences, _ := session.Annotations(row.Path, string(d.b))
			raw = policy.SplitAtFences(fences, string(d.b), raw)
		}
		hs := repository.Identify(row.Path, d.a, d.b, raw)
		for _, h := range hs {
			row.Added += h.CurrentEnd - h.CurrentStart
			row.Removed += h.End - h.Start
		}
		if row.Status == "fork-only" && d.b != nil && !bytes.ContainsRune(d.b, 0) {
			row.Added = len(repository.Lines(string(d.b)))
		}
		if entry := config.DerivedFor(row.Path); entry != nil && row.Upstream != nil {
			// A generator output is explained by its declaration, not by hunks.
			// Missing, mode-changed and non-text outputs keep their findings.
			row.Derived = entry
			row.Explanations = map[string]policy.Explanation{entry.ID: entry.Explanation}
			if row.Status == "modified" && row.Upstream.Mode == row.Selected.Mode && row.Upstream.Kind == "file" && row.Selected.Kind == "file" && repository.Text(d.a) && repository.Text(d.b) {
				row.Classification, row.Approved = "derived", []string{entry.ID}
			}
			if err := policy.CheckReferences(row.Path, entry.Explanation, refs); err != nil {
				if syntax.IsUnavailable(err) {
					return nil, err
				}
				report.Findings = append(report.Findings, policy.Finding{Kind: "invalid_reference", Rule: "derived/declared", Path: row.Path, Line: 1, Exception: entry.ID, Message: err.Error()})
				row.Classification = "unexplained"
			}
		} else if x := config.FileException(row.Path); x != nil {
			class, ids, fs := x.ClassifyFile(row.Upstream, row.Selected, d.a, d.b)
			row.Classification, row.Approved = class, ids
			row.Explanations = map[string]policy.Explanation{x.ID: x.Explanation()}
			if err := policy.CheckReferences(row.Path, x.Explanation(), refs); err != nil {
				if syntax.IsUnavailable(err) {
					return nil, err
				}
				fs = append(fs, policy.Finding{Kind: "invalid_reference", Path: row.Path, Exception: x.ID, Message: err.Error()})
				row.Classification = "unexplained"
			}
			report.Findings = append(report.Findings, fs...)
		} else if row.Upstream != nil && row.Selected != nil && repository.Text(d.a) && repository.Text(d.b) {
			class, ids, fs, owners := session.ClassifyHunks(row.Path, d.a, d.b, hs, historical[row.Path], transformed[row.Path])
			for i := range hs {
				hs[i].Approval = owners[i]
			}
			row.Classification = class
			if row.Upstream.Mode != row.Selected.Mode {
				row.Classification = "unexplained"
			}
			row.Approved = ids
			row.Explanations = map[string]policy.Explanation{}
			annotations, _ := session.Annotations(row.Path, string(d.b))
			for _, rule := range config.Restorations {
				applied := false
				for _, id := range ids {
					if id == rule.ID {
						applied = true
						break
					}
				}
				if !applied {
					continue
				}
				row.Explanations[rule.ID] = rule.Explanation
				for _, old := range historical[row.Path] {
					if old.Rule != rule.ID {
						continue
					}
					for _, h := range hs {
						if old.Matches(h) {
							row.HistoricalMatches = append(row.HistoricalMatches, HistoricalMatch{Rule: rule.ID, Hunk: h.ID, RemovedBy: old.RemovedBy, Source: old.Source})
						}
					}
				}
				if err := policy.CheckReferences(row.Path, rule.Explanation, refs); err != nil {
					if syntax.IsUnavailable(err) {
						return nil, err
					}
					fs = append(fs, policy.Finding{Kind: "invalid_reference", Rule: "history/restored", Path: row.Path, Line: 1, Subject: rule.ID, Message: "restoration " + rule.ID + ": " + err.Error()})
					row.Classification = "unexplained"
				}
			}
			for _, x := range config.Exceptions {
				if x.Path != row.Path {
					continue
				}
				m := x.Explanation()
				line := 1
				if x.Fence {
					a, ok := annotations[x.ID]
					if !ok {
						continue
					}
					m = a.Explanation
					line = a.Start + 1
				}
				row.Explanations[x.ID] = m
				if err := policy.CheckReferences(row.Path, m, refs); err != nil {
					if syntax.IsUnavailable(err) {
						return nil, err
					}
					fs = append(fs, policy.Finding{Kind: "invalid_reference", Path: row.Path, Line: line, Exception: x.ID, Message: err.Error()})
					row.Classification = "unexplained"
				}
			}
			report.Findings = append(report.Findings, fs...)
		}
		for _, rule := range config.Transforms {
			if !rule.Matches(row.Path) {
				continue
			}
			if row.Explanations == nil {
				row.Explanations = map[string]policy.Explanation{}
			}
			row.Explanations[rule.ID] = rule.Explanation
			if row.Upstream == nil || row.Selected == nil || row.Upstream.Kind != "file" || row.Selected.Kind != "file" || !repository.Text(d.a) || !repository.Text(d.b) {
				report.Findings = append(report.Findings, policy.Finding{Kind: "stale_transform", Path: row.Path, Exception: rule.ID, Message: "transformation requires existing bounded regular UTF-8 source"})
				row.Classification = "unexplained"
			}
			if err := policy.CheckReferences(row.Path, rule.Explanation, refs); err != nil {
				if syntax.IsUnavailable(err) {
					return nil, err
				}
				report.Findings = append(report.Findings, policy.Finding{Kind: "invalid_reference", Rule: "transform/scoped", Path: row.Path, Line: 1, Exception: rule.ID, Message: err.Error()})
				row.Classification = "unexplained"
			}
		}
		report.Hunks = append(report.Hunks, hs...)
		count := func(n *Counts) {
			if row.Upstream == nil {
				n.New++
				n.NewLines += row.Added
				return
			}
			n.Inherited++
			if row.Status == "identical" {
				n.Exact++
			}
			n.Removed += row.Removed
			n.Added += row.Added
			if row.Status == "missing" {
				n.Missing++
			}
			if row.Status == "modified" && (!repository.Text(d.a) || !repository.Text(d.b)) {
				n.Binary++
			}
		}
		count(&report.Totals)
		if report.Families[row.Family] == nil {
			report.Families[row.Family] = &Counts{}
		}
		count(report.Families[row.Family])
		if row.Status == "missing" {
			for p, ce := range ct {
				if _, old := bt[p]; !old && ce.OID == bt[row.Path].OID {
					row.IdenticalNewPaths = append(row.IdenticalNewPaths, p)
				}
			}
			sort.Strings(row.IdenticalNewPaths)
		}
	}
	for _, row := range report.Files {
		if row.Classification == "derived" {
			report.Limits = append(report.Limits, "Derived files are accepted as declared generator outputs; inspection does not run their freshness checks.")
			break
		}
	}
	if options.Metric != "" && options.Metric != "source" {
		report.Metric = Measure(report, contents, options.Metric)
	}
	untracked, e := repo.Untracked()
	if e != nil {
		return nil, e
	}
	for _, p := range untracked {
		if repository.Matches(p, selectors) {
			report.Untracked = append(report.Untracked, p)
		}
	}
	if options.Case != "" && len(paths) > 0 {
		k := config.Cases[options.Case]
		report.ReviewOrder = k.ReviewOrder
		report.Verification = k.Verification
		for _, v := range []struct{ name, from, to string }{{"selected", base, target}, {"local", target, head}, {"working", head, ""}} {
			patch, err := repo.DiffPatch(v.from, v.to, paths)
			if err != nil {
				return nil, err
			}
			report.ReviewPatches[v.name] = string(patch)
		}
	}
	for _, s := range report.Unmatched {
		report.Findings = append(report.Findings, policy.Finding{Kind: "unmatched_selector", Path: s, Message: "selector matches no tracked source"})
	}
	if err := enrichContracts(repo, session, report, options); err != nil {
		return nil, err
	}
	report.SyntaxEngine = "ast-grep " + syntax.Version
	endHead, e := repo.Resolve("HEAD")
	if e != nil {
		return nil, e
	}
	if endHead != head && (rev == "" || options.Diagnose || options.Case != "") {
		return nil, &repository.Stale{Message: "snapshot changed: HEAD advanced"}
	}
	var signature strings.Builder
	workPaths := make([]string, 0, len(work))
	for p := range work {
		workPaths = append(workPaths, p)
	}
	sort.Strings(workPaths)
	// A fresh reader reopens every directory: validation never trusts a handle
	// opened before the source it confirms.
	sweep := repo.Reader()
	defer sweep.Close()
	for _, p := range workPaths {
		if s, ok := work[p]; ok {
			now, err := sweep.Identity(p)
			if err != nil {
				return nil, err
			}
			if !repository.Same(s, now) {
				return nil, &repository.Stale{Message: "input changed: " + p}
			}
			fmt.Fprintf(&signature, "%s\x00%+v\n", p, s)
		}
	}
	for _, row := range report.Files {
		fmt.Fprintf(&signature, "%s\x00%+v\x00%+v\n", row.Path, row.Upstream, row.Selected)
	}
	selection, err := json.Marshal(options)
	if err != nil {
		return nil, err
	}
	if report.DocumentEngine != "" {
		if err := session.Documents.ValidateRuntime(readReference); err != nil {
			return nil, err
		}
		report.DocumentInputs = observedReferences
		if rev == "" {
			if err := observedReferences.Verify(repo, repository.SourceInputs); err != nil {
				return nil, err
			}
		}
		referencePaths := make([]string, 0, len(references))
		for p := range references {
			referencePaths = append(referencePaths, p)
		}
		sort.Strings(referencePaths)
		for _, p := range referencePaths {
			data := references[p]
			var state *repository.State
			var observed []byte
			var err error
			if rev == "" {
				state, observed, err = sweep.Snapshot(p)
			} else {
				state, observed, err = blob(repo, ct, p)
			}
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(data, observed) {
				return nil, &repository.Stale{Message: "document dependency changed: " + p}
			}
			report.DocumentInputs[p] = state
			fmt.Fprintf(&signature, "%s\x00%+v\n", p, state)
		}
	}
	documentInputsJSON, err := json.Marshal(report.DocumentInputs)
	if err != nil {
		return nil, err
	}
	report.SnapshotSHA256 = repository.Digest([]byte(strings.Join([]string{string(documentInputsJSON), report.DocumentEngine, report.SyntaxEngine, config.SHA256, base, rev, string(selection), report.ContractInputsSHA256, signature.String()}, "\x00")))
	policy.NormalizeFindings(report.Findings)
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Rule < b.Rule
	})
	return report, nil
}

func hasRule(c *policy.Config, p string) bool {
	for _, x := range c.Exceptions {
		if x.Path == p {
			return true
		}
	}
	for _, x := range c.Transforms {
		if x.Matches(p) {
			return true
		}
	}
	return false
}
