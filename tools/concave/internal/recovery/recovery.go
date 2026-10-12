// Package recovery owns inert plans and exact-preimage patch application.
package recovery

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"concave/internal/analysis"
	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/syntax"
)

func stale(s string) error { return &repository.Stale{Message: s} }

// Recovery applies Git patches only where Git will preserve reviewed source
// bytes. Content converters execute arbitrary filters or rewrite those bytes;
// inspection stays available, but this mutation path must refuse them first.
func checkContentConversions(r *repository.Repo, paths []string) error {
	args := []string{"check-attr", "-z", "filter", "ident", "working-tree-encoding", "--"}
	args = append(args, paths...)
	raw, err := r.Git(nil, args...)
	if err != nil {
		return err
	}
	fields := bytes.Split(raw, []byte{0})
	if len(fields) == 0 || len(fields[len(fields)-1]) != 0 || (len(fields)-1)%3 != 0 {
		return fmt.Errorf("invalid Git attribute response")
	}
	for i := 0; i+2 < len(fields); i += 3 {
		value := string(fields[i+2])
		if value != "unspecified" && value != "unset" {
			return fmt.Errorf("recovery refuses Git content conversion %s=%s on %s; reviewed raw bytes require manual source editing", fields[i+1], fields[i+2], fields[i])
		}
	}
	return nil
}

type Effect struct {
	Before *repository.State `json:"before"`
	After  *repository.State `json:"after"`
}
type Plan struct {
	Findings         []policy.Finding   `json:"source_findings"`
	Examples         []document.Example `json:"examples,omitempty"`
	DocumentInputs   repository.Inputs  `json:"document_inputs,omitempty"`
	DocumentEngine   string             `json:"document_engine,omitempty"`
	CreatedPaths     []string           `json:"created_paths,omitempty"`
	DeletedPaths     []string           `json:"deleted_paths,omitempty"`
	SyntaxEngine     string             `json:"syntax_engine,omitempty"`
	Format           int                `json:"format"`
	Kind             string             `json:"kind"`
	Head             string             `json:"head"`
	Upstream         string             `json:"upstream"`
	Revision         string             `json:"revision"`
	PolicySHA256     string             `json:"policy_sha256"`
	InspectionSHA256 string             `json:"inspection_sha256"`
	Reason           string             `json:"reason"`
	SelectedHunks    []string           `json:"selected_hunks"`
	Files            map[string]Effect  `json:"files"`
	Patch            string             `json:"patch"`
	PatchSHA256      string             `json:"patch_sha256"`
	Verification     []string           `json:"verification"`
	BehavioralParity string             `json:"behavioral_parity"`
}

// CheckedPatch computes a patch's exact effects in private scratch. Only the
// declared creations may be absent before and the declared deletions after.
func CheckedPatch(r *repository.Repo, patch []byte, creates, deletes []string) (map[string]Effect, error) {
	return checkedPatch(r, patch, creates, deletes, nil)
}

func checkedPatch(r *repository.Repo, patch []byte, creates, deletes []string, inspect func(string, []byte) error) (map[string]Effect, error) {
	removed := map[string]bool{}
	for _, p := range deletes {
		if !repository.ValidPath(p) || strings.HasSuffix(p, "/") || removed[p] || slices.Contains(creates, p) {
			return nil, fmt.Errorf("invalid or duplicate deletion path %q", p)
		}
		removed[p] = true
	}
	allowed := map[string]bool{}
	for _, p := range creates {
		if !repository.ValidPath(p) || strings.HasSuffix(p, "/") || allowed[p] {
			return nil, fmt.Errorf("invalid or duplicate creation path %q", p)
		}
		allowed[p] = true
		tracked, err := r.Git(nil, "ls-files", "--cached", "--", p)
		if err != nil {
			return nil, err
		}
		// A tracked path absent from the working tree is someone's pending
		// deletion; only Concave's own binding file for a source may come back,
		// because removing a path's last binding deletes that file.
		if _, binding := policy.BindingSource(p); len(tracked) > 0 && !binding {
			return nil, fmt.Errorf("creation path is tracked: %s", p)
		}
		_, err = r.Git(nil, "check-ignore", "-q", "--", p)
		var status *exec.ExitError
		if err == nil {
			return nil, fmt.Errorf("creation path is ignored: %s", p)
		}
		if !errors.As(err, &status) || status.ExitCode() != 1 {
			return nil, err
		}
	}
	if len(patch) == 0 || len(patch) > repository.MaxBytes {
		return nil, fmt.Errorf("patch must be nonempty and at most 4 MiB")
	}
	raw, e := r.Git(patch, "apply", "--numstat", "-z", "-")
	if e != nil {
		return nil, e
	}
	states := map[string]Effect{}
	scratch, e := os.MkdirTemp("", "concave-patch-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(scratch)
	private := &repository.Repo{Root: scratch, Context: r.Context}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		v := strings.SplitN(string(record), "\t", 3)
		if len(v) != 3 || v[0] == "-" || v[1] == "-" || !repository.ValidPath(v[2]) {
			return nil, fmt.Errorf("patch needs unique regular text paths")
		}
		p := v[2]
		if _, ok := states[p]; ok {
			return nil, fmt.Errorf("repeated patch path")
		}
		before, data, err := r.Snapshot(p)
		if err != nil {
			return nil, err
		}
		if before == nil && allowed[p] {
			states[p] = Effect{}
			continue
		}
		if allowed[p] {
			return nil, stale("creation destination already exists: " + p)
		}
		if before == nil || before.Kind != "file" || !repository.Text(data) {
			return nil, fmt.Errorf("patch needs existing regular UTF-8 file: %s", p)
		}
		states[p] = Effect{Before: before}
		dst := filepath.Join(scratch, filepath.FromSlash(p))
		if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
			return nil, e
		}
		mode := os.FileMode(0600)
		if before.Mode == "100755" {
			mode = 0700
		}
		if e = os.WriteFile(dst, data, mode); e != nil {
			return nil, e
		}
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("empty patch")
	}
	for p := range allowed {
		if _, ok := states[p]; !ok {
			return nil, fmt.Errorf("unused creation path: %s", p)
		}
	}
	for p := range removed {
		if _, ok := states[p]; !ok {
			return nil, fmt.Errorf("unused deletion path: %s", p)
		}
	}
	paths := make([]string, 0, len(states))
	for path := range states {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if err := checkContentConversions(r, paths); err != nil {
		return nil, err
	}
	if _, e = private.Git(patch, "apply", "--unidiff-zero", "--whitespace=nowarn", "-"); e != nil {
		return nil, e
	}
	r.Processes += private.Processes
	for p, v := range states {
		after, data, err := private.Snapshot(p)
		if err != nil {
			return nil, err
		}
		if removed[p] {
			if after != nil {
				return nil, fmt.Errorf("declared deletion remains: %s", p)
			}
			continue
		}
		if after == nil || after.Kind != "file" || !repository.Text(data) || v.Before == nil && after.Mode != "100644" || v.Before != nil && (after.Mode != v.Before.Mode || after.SHA256 == v.Before.SHA256) {
			return nil, fmt.Errorf("recovery refuses deletions, mode changes and empty effects")
		} // Scratch has the same default object format; use the owning repository's OID.
		_, b, err := private.Snapshot(p)
		if err != nil {
			return nil, err
		}
		after.OID = r.OID(b)
		if inspect != nil {
			if err := inspect(p, b); err != nil {
				return nil, err
			}
		}
		v.After = after
		states[p] = v
	}
	return states, nil
}
func Prepare(r *repository.Repo, c *policy.Config, report *analysis.Report, inspection []byte, ids []string, manual []byte, reason string, creates ...string) (*Plan, error) {
	if len(creates) > 0 && manual == nil {
		return nil, fmt.Errorf("--create requires a reviewed manual patch")
	}
	if report.Format != 1 || report.Kind != "concave-inspection" || strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("expected inspection and concrete reason")
	}
	if report.DocumentEngine != "" && report.DocumentEngine != document.Identity() {
		return nil, stale("MDX engine changed; inspect again")
	}
	if report.Revision == "" {
		if err := report.DocumentInputs.Verify(r, repository.SourceInputs); err != nil {
			return nil, err
		}
	}
	if c.SHA256 != report.PolicySHA256 {
		return nil, stale("policy changed; inspect again")
	}
	head, e := r.Resolve("HEAD")
	if e != nil {
		return nil, e
	}
	if head != report.Head {
		return nil, stale("HEAD changed; inspect again")
	}
	fresh, e := analysis.Analyze(r, c, analysis.Options{Rev: report.Revision, Paths: report.Scope.Paths, Family: report.Scope.Family, Case: report.Scope.Case, Diagnose: true})
	if e != nil {
		return nil, e
	}
	if fresh.Upstream != report.Upstream {
		return nil, stale("upstream changed")
	}
	recorded := map[string]analysis.Row{}
	rows := map[string]analysis.Row{}
	for _, v := range report.Files {
		recorded[v.Path] = v
	}
	for _, v := range fresh.Files {
		rows[v.Path] = v
	}
	// Resolve tracked ownership once for the entire plan, not once per hunk.
	ht, e := r.Tree(head)
	if e != nil {
		return nil, e
	}
	patch := manual
	if manual != nil && len(ids) > 0 {
		return nil, fmt.Errorf("choose hunk IDs or a manual patch")
	}
	if manual == nil {
		if len(ids) == 0 {
			return nil, fmt.Errorf("select hunk IDs explicitly")
		}
		inspected := map[string]bool{}
		for _, hunk := range report.Hunks {
			inspected[hunk.ID] = true
		}
		selected := map[string]bool{}
		for _, id := range ids {
			if !inspected[id] {
				return nil, fmt.Errorf("hunk ID %s is absent from inspection; select an ID from that report", id)
			}
			if selected[id] {
				return nil, fmt.Errorf("repeated hunk ID")
			}
			selected[id] = true
		}
		found := 0
		groups := map[string][]repository.Hunk{}
		for _, h := range fresh.Hunks {
			if !selected[h.ID] {
				continue
			}
			found++
			v := rows[h.Path]
			old, ok := recorded[h.Path]
			if !ok || !v.RecoverySupported || !repository.Same(old.Working, v.Working) || !repository.Same(v.Selected, v.Working) {
				return nil, stale("selected source differs from working file; inspect and review a boundary patch")
			}
			// A working inspection explicitly reviewed these dirty bytes. Its
			// observed preimage and freshly derived hunk must still match;
			// committed inspections retain the stricter HEAD equality rule.
			if report.Revision != "" && ht[h.Path].OID != v.Selected.OID {
				return nil, stale("selected source differs from HEAD")
			}
			groups[h.Path] = append(groups[h.Path], h)
		}
		if found != len(ids) {
			return nil, stale("selected source hunks changed; inspect again")
		}
		paths := []string{}
		for p := range groups {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			hs := groups[p]
			before, err := r.Blob(rows[p].Upstream.OID)
			if err != nil {
				return nil, err
			}
			session := policy.NewSession(r, c)
			plans, err := session.TransformationPlans(r, p, before)
			if err != nil {
				return nil, err
			}
			if document.Supports(p) && c.UpstreamPath(p) != p && !document.Native(p) {
				facts, err := session.Documents.Parse(p, string(before))
				if err != nil {
					return nil, err
				}
				for _, h := range hs {
					if structuralDocumentEdit(facts, string(before), h) {
						return nil, fmt.Errorf("%s: converted document metadata or imports require a reviewed --patch preserving its publishing format and product names", p)
					}
				}
			}
			sort.Slice(hs, func(i, j int) bool { return hs[i].CurrentStart < hs[j].CurrentStart })
			reverse := []repository.Hunk{}
			offset := 0
			for _, h := range hs {
				original, err := policy.Compose(before, plans, policy.Span{Start: h.Start, End: h.End})
				if err != nil {
					return nil, err
				}
				length := len(repository.Lines(original.Original))
				if original.Original == h.After {
					return nil, fmt.Errorf("%s: selected hunk already equals the declared transformation; nothing to recover", p)
				}
				reverse = append(reverse, repository.Hunk{Start: h.CurrentStart, End: h.CurrentEnd, CurrentStart: h.CurrentStart + offset, CurrentEnd: h.CurrentStart + offset + length, Before: h.After, After: original.Original})
				offset += length - (h.CurrentEnd - h.CurrentStart)
			}
			patch = append(patch, repository.Patch(p, reverse)...)
		}
	}
	findings := []policy.Finding{}
	effects, e := checkedPatch(r, patch, creates, nil, func(path string, after []byte) error {
		row := rows[path]
		if row.Upstream == nil {
			return nil
		}
		before, err := r.Blob(row.Upstream.OID)
		if err != nil {
			return err
		}
		found, err := analysis.SourceFindings(r, c, path, before, after)
		findings = append(findings, found...)
		return err
	})
	policy.NormalizeFindings(findings)
	if e != nil {
		return nil, e
	}
	for p, v := range effects {
		if v.Before == nil {
			upstream, err := c.UpstreamTree(r)
			if err != nil {
				return nil, err
			}
			if ht[p].OID != "" || upstream[p].OID != "" {
				return nil, fmt.Errorf("creation cannot replace a deleted inherited or tracked file: %s", p)
			}
			continue
		}
		old, ok := recorded[p]
		_, inScope := rows[p]
		if !ok || !inScope {
			return nil, fmt.Errorf("patch path absent from inspection: %s", p)
		}
		if ht[p].OID == "" {
			return nil, fmt.Errorf("recovery needs locally tracked path: %s", p)
		}
		if !repository.Same(old.Working, v.Before) {
			return nil, stale("stale preimage: " + p)
		}
	}
	requiredEngine := recoveryEngine(c, effects)
	if requiredEngine != "" && (report.SyntaxEngine != requiredEngine || fresh.SyntaxEngine != requiredEngine) {
		return nil, stale("syntax engine changed; inspect again")
	}
	inputs := repository.Inputs{}
	for path, state := range fresh.DocumentInputs {
		if _, changed := effects[path]; !changed {
			inputs[path] = state
		}
	}
	return &Plan{Findings: findings, DocumentInputs: inputs, DocumentEngine: fresh.DocumentEngine, CreatedPaths: creates, SyntaxEngine: requiredEngine, Format: 1, Kind: "concave-recovery-plan", Head: head, Upstream: report.Upstream, Revision: report.Revision, PolicySHA256: c.SHA256, InspectionSHA256: repository.Digest(inspection), Reason: reason, SelectedHunks: ids, Files: effects, Patch: string(patch), PatchSHA256: repository.Digest(patch), Verification: report.Verification, BehavioralParity: "not_established"}, nil
}

// Validate checks the current applicability of a saved plan without editing
// repository files or the index. Apply always repeats this validation.
func Validate(r *repository.Repo, c *policy.Config, p *Plan) error {
	if p.Format != 1 || p.Kind != "concave-recovery-plan" || p.Reason == "" {
		return fmt.Errorf("unsupported recovery plan")
	}
	if p.DocumentEngine != "" && p.DocumentEngine != document.Identity() {
		return stale("MDX engine changed; prepare a new plan")
	}
	if err := p.DocumentInputs.Verify(r, repository.SourceInputs); err != nil {
		return err
	}
	if c.SHA256 != p.PolicySHA256 {
		return stale("policy changed; prepare a new plan")
	}
	requiredEngine := recoveryEngine(c, p.Files)
	if p.SyntaxEngine != requiredEngine {
		return stale("syntax engine requirement changed; prepare a new plan")
	}
	if requiredEngine != "" {
		if err := syntax.New(r.Context, r.Root).Check(); err != nil {
			return err
		}
	}
	head, e := r.Resolve("HEAD")
	if e != nil {
		return e
	}
	if head != p.Head {
		return stale("HEAD changed; prepare a new plan")
	}
	if len(p.CreatedPaths) > 0 {
		tree, err := c.UpstreamTree(r)
		if err != nil {
			return err
		}
		for _, path := range p.CreatedPaths {
			if tree[path].OID != "" {
				return fmt.Errorf("creation cannot reintroduce an inherited file: %s", path)
			}
		}
	}
	if repository.Digest([]byte(p.Patch)) != p.PatchSHA256 {
		return fmt.Errorf("patch digest mismatch")
	}
	paths := []string{}
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		got, _, err := r.Snapshot(path)
		if err != nil {
			return err
		}
		if !repository.Same(got, p.Files[path].Before) {
			return stale("stale preimage; no edits applied: " + path)
		}
	}
	actual, e := CheckedPatch(r, []byte(p.Patch), p.CreatedPaths, p.DeletedPaths)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(actual, p.Files) {
		return fmt.Errorf("patch paths or effects differ from plan")
	}
	if _, e = r.Git([]byte(p.Patch), "apply", "--check", "--unidiff-zero", "--whitespace=nowarn", "-"); e != nil {
		return e
	}
	return nil
}

func Apply(r *repository.Repo, c *policy.Config, p *Plan) error {
	if err := Validate(r, c, p); err != nil {
		return err
	}
	if _, err := r.Git([]byte(p.Patch), "apply", "--unidiff-zero", "--whitespace=nowarn", "-"); err != nil {
		return err
	}
	paths := make([]string, 0, len(p.Files))
	for path := range p.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		got, _, err := r.Snapshot(path)
		if err != nil {
			return err
		}
		if !repository.Same(got, p.Files[path].After) {
			return stale("postimage changed; inspect concurrent edits: " + path)
		}
	}
	return nil
}

// FilePatch changes, creates or deletes one complete regular text file.
func FilePatch(path string, before []byte, existed bool, after []byte, exists bool) []byte {
	if existed && exists {
		return SinglePatch(path, before, after)
	}
	var b strings.Builder
	a, c := strconv.Quote("a/"+path), strconv.Quote("b/"+path)
	lines, prefix := repository.Lines(string(after)), "+"
	switch {
	case exists:
		fmt.Fprintf(&b, "diff --git %s %s\nnew file mode 100644\n--- /dev/null\n+++ %s\n@@ -0,0 +1,%d @@\n", a, c, c, len(lines))
	case existed:
		lines, prefix = repository.Lines(string(before)), "-"
		fmt.Fprintf(&b, "diff --git %s %s\ndeleted file mode 100644\n--- %s\n+++ /dev/null\n@@ -1,%d +0,0 @@\n", a, c, a, len(lines))
	default:
		return nil
	}
	for _, line := range lines {
		b.WriteString(prefix + line)
		if !strings.HasSuffix(line, "\n") {
			b.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return []byte(b.String())
}
func SinglePatch(path string, before, after []byte) []byte {
	a, b := repository.Lines(string(before)), repository.Lines(string(after))
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		start++
	}
	ae, be := len(a), len(b)
	for ae > start && be > start && a[ae-1] == b[be-1] {
		ae--
		be--
	}
	return repository.Patch(path, []repository.Hunk{{Start: start, End: ae, CurrentStart: start, CurrentEnd: be, Before: strings.Join(a[start:ae], ""), After: strings.Join(b[start:be], "")}})
}

func recoveryEngine(c *policy.Config, files map[string]Effect) string {
	for _, rule := range c.Transforms {
		if rule.ASTGrep == nil {
			continue
		}
		for _, path := range rule.Paths {
			if _, ok := files[path]; ok {
				return syntax.Identity
			}
		}
	}
	return ""
}
