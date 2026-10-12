package output

import (
	"concave/internal/analysis"
	"fmt"
	"io"
	"strings"

	"concave/internal/repository"
)

func FindingQueue(w io.Writer, q *analysis.FindingQueue, top int) {
	findingSummary(w, &q.FindingSummary)
	fmt.Fprintln(w, "Repeated bytes are review candidates, not shared approval. Use check for enforcement.")
	if q.SelectedGroup == "" {
		findingFamilies(w, &q.FindingSummary)
	}
	findingGroups(w, q, top)
}

func findingSummary(w io.Writer, q *analysis.FindingSummary) {
	candidate := q.Revision
	if candidate == "" {
		candidate = "working files on " + q.Head
	}
	fmt.Fprintf(w, "Concave findings · %s\nupstream %s\n", candidate, q.Upstream)
	fmt.Fprintf(w, "Coverage: %s; %d/%d inherited paths inspected; %d outside this view\n", q.Coverage.Scope, q.Coverage.Inspected, q.Coverage.UpstreamFiles, q.Coverage.Uninspected)
	fmt.Fprintf(w, "%d exact; %d approved; %d unresolved files\n%d findings; %d unresolved text hunks; source review complete: %t\n", q.Exact, q.Approved, q.Unresolved, q.FindingCount, q.UnresolvedHunks, q.Complete)
	fmt.Fprintln(w, "Source preservation only; behavioral parity is not established.")
}

func findingFamilies(w io.Writer, q *analysis.FindingSummary) {
	fmt.Fprintln(w, "\nFamily                  exact  approved  unresolved  findings")
	for _, f := range q.Families {
		fmt.Fprintf(w, "%-23s %5d %9d %11d %9d\n", f.ID, f.Exact, f.Approved, f.Unresolved, f.Findings)
	}
}

func findingGroups(w io.Writer, q *analysis.FindingQueue, top int) {
	for i, g := range q.Groups {
		if top > 0 && i >= top {
			break
		}
		fmt.Fprintf(w, "\n%s · %d occurrences · %s [%s]\n", g.ID, len(g.Occurrences), g.Kind, g.Rule)
		if q.SelectedGroup != "" && g.TextHunk {
			for _, side := range []struct{ prefix, text string }{{"-", g.Before}, {"+", g.After}} {
				lines := repository.Lines(side.text)
				for i, line := range lines {
					if i >= 60 {
						fmt.Fprintln(w, "  … remaining edit bytes in --json")
						break
					}
					fmt.Fprintf(w, "%s %s\n", side.prefix, strings.TrimSuffix(line, "\n"))
				}
			}
		} else if g.TextHunk {
			summary := strings.TrimSpace(g.After)
			if summary == "" {
				summary = "remove: " + strings.TrimSpace(g.Before)
			}
			runes := []rune(summary)
			if len(runes) > 140 {
				summary = string(runes[:140]) + "…"
			}
			fmt.Fprintf(w, "  %q\n", summary)
		} else {
			fmt.Fprintf(w, "  %s\n", g.Message)
		}
		limit := 2
		if q.SelectedGroup != "" {
			limit = top
		}
		for j, o := range g.Occurrences {
			if limit > 0 && j >= limit {
				fmt.Fprintf(w, "  … %d more locations\n", len(g.Occurrences)-j)
				break
			}
			fmt.Fprintf(w, "  %s:%d", o.Path, o.Line)
			if o.Hunk != "" {
				fmt.Fprintf(w, " hunk=%s", o.Hunk)
			}
			if o.Exception != "" {
				fmt.Fprintf(w, " exception=%s", o.Exception)
			}
			fmt.Fprintln(w)
		}
	}
	if top > 0 && top < len(q.Groups) {
		fmt.Fprintf(w, "\n… %d more groups\n", len(q.Groups)-top)
	}
}

func FindingFiles(w io.Writer, q *analysis.FindingFileView, top int) {
	findingSummary(w, &q.FindingSummary)
	fmt.Fprintln(w, "Files ordered by unresolved hunk count, then changed bytes; other findings follow.")
	fmt.Fprintln(w, "Size is not a risk score. Use check for enforcement.")
	findingFamilies(w, &q.FindingSummary)
	fmt.Fprintln(w, "\nHunks  -lines  +lines     bytes  other  family                  path")
	for i, f := range q.Files {
		if top > 0 && i >= top {
			break
		}
		fmt.Fprintf(w, "%5d %7d %7d %9d %6d  %-23s %s\n", f.TextHunks, f.RemovedLines, f.AddedLines, f.ChangedBytes, f.OtherFindings, f.Family, f.Path)
		if f.OtherFindings > 0 {
			fmt.Fprintf(w, "      %s (text counts cover only located hunks)\n", strings.Join(f.Kinds, ", "))
		}
	}
	if top > 0 && top < len(q.Files) {
		fmt.Fprintf(w, "\n… %d more files\n", len(q.Files)-top)
	}
}
