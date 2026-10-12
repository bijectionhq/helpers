package output

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"concave/internal/analysis"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

func JSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	e.SetEscapeHTML(false)
	return e.Encode(v)
}
func Count(count int, noun string) string {
	if count != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", count, noun)
}

func Text(w io.Writer, r *analysis.Report, top int) {
	text(w, r, top, true)
}

// CheckText keeps enforcement output focused on findings. Inspection and saved
// reports retain hunk IDs for review and recovery; JSON always keeps the full data.
func CheckText(w io.Writer, report *analysis.Report, top int) {
	if report.Coverage.Scope == "changed" && len(report.Scope.Paths) == 0 {
		fmt.Fprintln(w, "No changed files.")
	}
	status := "passed"
	if len(report.Findings) > 0 {
		status = "failed"
	}
	fmt.Fprintf(w, "Source %s · %s inspected\n", status, Count(len(report.Files), "file"))
	counts := map[string]int{}
	for _, file := range report.Files {
		counts[file.Classification]++
	}
	for _, category := range []struct{ key, label string }{
		{"preserved", "preserved file"}, {"transformed", "transformed file"},
		{"excepted", "excepted file"}, {"derived", "derived file"}, {"fork_owned", "fork-owned file"}, {"unexplained", "unexplained difference"},
	} {
		if count := counts[category.key]; count > 0 {
			fmt.Fprintf(w, "  %s\n", Count(count, category.label))
		}
	}
	fmt.Fprintf(w, "Scope: %s; %d/%d inherited paths inspected; %d uninspected.\nBehavioral checks not run.\n", report.Coverage.Scope, report.Coverage.Inspected, report.Coverage.UpstreamFiles, report.Coverage.Uninspected)
	// Findings group under their file, one line each; each rule's rationale
	// follows once. A binding that no longer matches its source decides every
	// difference inside its span, so those fold into one line beneath it.
	type entry struct {
		finding policy.Finding
		folded  map[string]int
	}
	entries := []*entry{}
	var binding *entry
	for _, finding := range report.Findings {
		if binding != nil && finding.Path == binding.finding.Path && finding.Exception == "" && finding.Line >= binding.finding.Line && finding.Line <= binding.finding.EndLine {
			binding.folded[cmp.Or(finding.Rule, finding.Kind)]++
			continue
		}
		e := &entry{finding: finding}
		binding = nil
		if (finding.Kind == "stale_exception" || finding.Kind == "obsolete_exception") && finding.Line > 0 && finding.EndLine >= finding.Line {
			e.folded, binding = map[string]int{}, e
		}
		entries = append(entries, e)
	}
	shown := entries
	if top > 0 && len(shown) > top {
		shown = shown[:top]
	}
	label := func(finding policy.Finding) string {
		return cmp.Or(finding.Rule, finding.Kind)
	}
	width := 0
	for _, e := range shown {
		width = max(width, len(label(e.finding)))
	}
	rules := []string{}
	path := "\x00"
	remaining := len(report.Findings)
	for _, e := range shown {
		finding := e.finding
		if finding.Path != path {
			path = finding.Path
			fmt.Fprintf(w, "\n%s\n", cmp.Or(path, "(repository)"))
		}
		line := "-"
		if finding.Line > 0 {
			line = strconv.Itoa(finding.Line)
		}
		message := finding.Message
		if finding.Exception != "" {
			message += " (" + finding.Exception + ")"
		}
		fmt.Fprintf(w, "  %6s  %-*s  %s\n", line, width, label(finding), message)
		remaining--
		for _, rule := range append([]string{finding.Rule}, slices.Sorted(maps.Keys(e.folded))...) {
			if rule != "" && !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
		for _, rule := range slices.Sorted(maps.Keys(e.folded)) {
			fmt.Fprintf(w, "  %6s  %-*s  + %s inside this binding, lines %d-%d\n", "", width, "", Count(e.folded[rule], rule+" finding"), finding.Line, finding.EndLine)
			remaining -= e.folded[rule]
		}
	}
	if remaining > 0 {
		fmt.Fprintf(w, "… %d more findings (--top 0 or --json for all)\n", remaining)
	}
	if len(rules) > 0 {
		fmt.Fprintln(w, "\nRules:")
		for _, rule := range policy.Rules {
			if slices.Contains(rules, rule.ID) {
				fmt.Fprintf(w, "  %s: %s\n", rule.ID, rule.Description)
			}
		}
	}
	if report.Next != nil && len(report.Next.Argv) > 0 {
		fmt.Fprintln(w, "\nNext:", report.Next.Message)
		fmt.Fprintln(w, " ", process.ShellCommand(report.Next.Argv))
	}

}

func text(w io.Writer, r *analysis.Report, top int, showHunks bool) {
	fmt.Fprintf(w, "Concave · %s\nupstream %s\ncandidate %s\npolicy %s\n", r.Status, r.Upstream, candidate(r), r.PolicySHA256)
	n := r.Totals
	fmt.Fprintf(w, "Coverage: %s; %d/%d inherited paths inspected; %d outside this view\n", r.Coverage.Scope, r.Coverage.Inspected, r.Coverage.UpstreamFiles, r.Coverage.Uninspected)
	fmt.Fprintf(w, "%d/%d inherited files exact; %d original lines removed; %d lines added\n%d missing paths; %d binary changes; %d fork-owned files\n", n.Exact, n.Inherited, n.Removed, n.Added, n.Missing, n.Binary, n.New)
	if r.Metric != nil {
		m := r.Metric
		fmt.Fprintf(w, "%s divergence: %d\n", m.Method, m.Total)
		crates := make([]string, 0, len(m.PerCrate))
		for name := range m.PerCrate {
			crates = append(crates, name)
		}
		sort.Slice(crates, func(i, j int) bool {
			if m.PerCrate[crates[i]] != m.PerCrate[crates[j]] {
				return m.PerCrate[crates[i]] > m.PerCrate[crates[j]]
			}
			return crates[i] < crates[j]
		})
		for i, name := range crates {
			if top > 0 && i >= top {
				break
			}
			fmt.Fprintf(w, "  %7d %s\n", m.PerCrate[name], name)
		}
		fmt.Fprintln(w, "Largest inherited-file divergence:")
		for i, f := range m.Files {
			if f.Divergence == 0 || top > 0 && i >= top {
				break
			}
			fmt.Fprintf(w, "  %7d %s\n", f.Divergence, f.Path)
		}
		for _, p := range m.Anomalies {
			fmt.Fprintf(w, "metric anomaly: missing inherited path %s\n", p)
		}
	}
	if len(r.Documents) > 0 {
		fmt.Fprintf(w, "Documents: %d parsed; local imports bound to this source snapshot.\n", len(r.Documents))
		if len(r.Files) == 1 && r.Files[0].UpstreamPath != "" {
			fmt.Fprintf(w, "Correspondence: %s ← %s\n", r.Files[0].Path, r.Files[0].UpstreamPath)
		}
	}
	fmt.Fprintln(w, "Source preservation only; behavioral parity is not established.")
	shown := 0
	for _, f := range r.Findings {
		if top > 0 && shown >= top {
			break
		}
		fmt.Fprint(w, f.Path)
		if f.Line > 0 {
			fmt.Fprintf(w, ":%d", f.Line)
		}
		if f.EndLine > f.Line {
			fmt.Fprintf(w, "-%d", f.EndLine)
		}
		fmt.Fprintf(w, ": %s", f.Kind)
		if f.Rule != "" {
			fmt.Fprintf(w, " [%s]", f.Rule)
		}
		if f.Exception != "" {
			fmt.Fprintf(w, " exception=%s", f.Exception)
		}
		fmt.Fprintf(w, ": %s\n", f.Message)
		// Text shows one-based upstream lines like the location above; JSON
		// keeps the zero-based, end-exclusive span.
		switch {
		case f.Upstream == nil:
		case f.Upstream.End > f.Upstream.Start+1:
			fmt.Fprintf(w, "  replaces upstream lines %d-%d\n", f.Upstream.Start+1, f.Upstream.End)
		case f.Upstream.End == f.Upstream.Start+1:
			fmt.Fprintf(w, "  replaces upstream line %d\n", f.Upstream.End)
		default:
			fmt.Fprintf(w, "  inserted before upstream line %d\n", f.Upstream.Start+1)
		}
		shown++
	}
	if shown < len(r.Findings) {
		fmt.Fprintf(w, "… %d more findings (--top 0 or --json for all)\n", len(r.Findings)-shown)
	}
	if len(r.Findings) > 0 {
		fmt.Fprintln(w, "Explain an enforcement rule: concave rule explain <rule-id>")
	}
	for _, p := range r.Probes {
		fmt.Fprintf(w, "%s:%d [%s/%s] %s\n", p.Path, p.Line, p.Version, p.Probe, p.Text)
	}
	if showHunks {
		shown = 0
		for _, h := range r.Hunks {
			if r.Contracts != nil && r.Contracts.Location != nil {
				loc := r.Contracts.Location
				line := loc.Line - 1
				if h.Path != loc.Path || line < h.CurrentStart || line >= max(h.CurrentStart+1, h.CurrentEnd) {
					continue
				}
			}
			if top > 0 && shown >= top {
				break
			}
			fmt.Fprintf(w, "hunk %s %s upstream [%d,%d)\n", h.ID, h.Path, h.Start, h.End)
			shown++
		}
	}
	if r.Scope.History {
		fmt.Fprintln(w, "Historical comparisons (diagnostic only; no approval or test execution):")
		if len(r.History) == 0 {
			fmt.Fprintln(w, "No pure historical deletion blocks in this scope.")
		}
		for i, h := range r.History {
			if top > 0 && i >= top {
				fmt.Fprintf(w, "… %d more historical blocks (--top 0 or --json for all)\n", len(r.History)-i)
				break
			}
			fmt.Fprintf(w, "%s [%s] %s block %s source %s:[%d,%d)\n", h.Path, h.Rule, h.Status, h.ID, h.SourceRevision, h.Source.Start, h.Source.End)
			if h.Selected != nil {
				fmt.Fprintf(w, "  selected [%d,%d), hunk %s (%s); comparison offsets are relative to these blocks\n", h.Selected.Span.Start, h.Selected.Span.End, h.Selected.Hunk, h.Pairing)
			}
			if len(h.Changes) > 0 {
				fmt.Fprint(w, string(repository.Patch(h.Path, h.Changes)))
			}
		}
	}
	for _, s := range r.ReviewOrder {
		fmt.Fprintf(w, "review: %s\n", s)
	}
	for _, s := range r.Verification {
		fmt.Fprintf(w, "verification to run: %s\n", s)
	}
	if len(r.Untracked) > 0 {
		fmt.Fprintf(w, "%d untracked paths listed in JSON; contents unread\n", len(r.Untracked))
	}
}
func candidate(r *analysis.Report) string {
	if r.Revision != "" {
		return r.Revision
	}
	return "working files on " + r.Head
}
func Save(r *repository.Repo, path string, v any, report *analysis.Report) (string, error) {
	dir, e := r.OutputDirectory(path)
	if e != nil {
		return "", e
	}
	name := "plan.json"
	if report != nil {
		name = "inspection.json"
	}
	write := func(n string, b []byte) error { return os.WriteFile(filepath.Join(dir, n), b, 0600) }
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return "", e
	}
	if e = write(name, append(b, '\n')); e != nil {
		return "", e
	}
	if report != nil {
		var s strings.Builder
		Text(&s, report, 0)
		if e = write("report.md", []byte("# Concave inspection\n\n```text\n"+s.String()+"```\n")); e != nil {
			return "", e
		}
		b, e = json.MarshalIndent(report.Hunks, "", "  ")
		if e != nil {
			return "", e
		}
		if e = write("hunks.json", append(b, '\n')); e != nil {
			return "", e
		}
		for n, p := range report.ReviewPatches {
			if e = write(n+".patch", []byte(p)); e != nil {
				return "", e
			}
		}
	}
	return dir, nil
}

func List(w io.Writer, c *policy.Config) {
	fmt.Fprintf(w, "Concave · upstream %s\npolicy %s\n\nFamilies\n", c.Upstream, c.SHA256)
	for _, f := range c.Families {
		fmt.Fprintf(w, "  %-20s %s\n", f.ID, f.Title)
	}
	fmt.Fprintln(w, "\nRules")
	for _, rule := range policy.Rules {
		fmt.Fprintf(w, "  %-20s %s\n", rule.ID, rule.Description)
	}
	fmt.Fprintln(w, "\nCases")
	cases := make([]string, 0, len(c.Cases))
	for name := range c.Cases {
		cases = append(cases, name)
	}
	sort.Strings(cases)
	for _, name := range cases {
		fmt.Fprintf(w, "  %-20s %d surfaces\n", name, len(c.Cases[name].Surfaces))
	}
	fmt.Fprintf(w, "\n%d exact files; %d source transformations; %d derived entries; %d reviewed exceptions\n", len(c.PreserveExactly), len(c.Transforms), len(c.Derived), len(c.Exceptions))
	for _, rule := range c.Transforms {
		fmt.Fprintf(w, "  %s transforms %d inherited paths: %s\n", rule.ID, len(rule.Paths), rule.Explanation.Why)
	}
	for _, rule := range c.Restorations {
		fmt.Fprintf(w, "  %s restores deletions from %s: %s\n", rule.ID, rule.RemovedBy, rule.Explanation.Why)
	}
	for _, entry := range c.Derived {
		fmt.Fprintf(w, "  %s derives %d inherited paths (freshness check %s): %s\n", entry.ID, len(entry.Paths), entry.Check, entry.Explanation.Why)
	}
	for _, e := range c.Exceptions {
		reason := e.Reason
		if e.Fence {
			reason = "explanation in source; use exception explain"
		}
		fmt.Fprintf(w, "  %s  %s: %s\n", e.ID, e.Path, reason)
	}
	fmt.Fprintln(w, "Use --json for selectors, transformation pairs, hashes and witness references.")
}
