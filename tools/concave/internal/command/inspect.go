package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"concave/internal/analysis"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/repository"
)

func (a *app) registerInspection(root *cobra.Command) {
	open := a.open
	out := a.out
	for _, name := range []string{"inspect", "check"} {
		checking := name == "check"
		var rev, family, caseName, metric, save, against, policyFrom string
		var history, withContracts, rdjson, changed bool
		var historyPairs []string
		top := 25
		c := &cobra.Command{Use: name + " [path ...]", Short: map[bool]string{true: "Enforce every difference; source equality is not behavioral proof", false: "Inspect source differences, hunks, obligations, history and metrics"}[checking], RunE: func(cmd *cobra.Command, paths []string) error {
			if rdjson && a.jsonMode {
				return fmt.Errorf("choose --json or --rdjson")
			}
			if top < 0 {
				return fmt.Errorf("--top must be nonnegative")
			}
			pairs := []analysis.HistoryPair{}
			for _, value := range historyPairs {
				block, hunk, ok := strings.Cut(value, "=")
				if !ok || block == "" || hunk == "" || strings.Contains(hunk, "=") {
					return fmt.Errorf("--history-pair requires block-id=hunk-id")
				}
				pairs = append(pairs, analysis.HistoryPair{Block: block, Hunk: hunk})
			}
			var location *analysis.Location
			for i, path := range paths {
				loc, err := analysis.ParseLocation(path)
				if err != nil {
					return err
				}
				if loc != nil {
					if checking || len(paths) != 1 {
						return fmt.Errorf("source locations require inspect with one file:line[:column]")
					}
					location, paths[i] = loc, loc.Path
				}
			}
			if caseName != "" && (len(paths) > 0 || family != "") {
				return fmt.Errorf("choose a case or path/family scope")
			}
			r, e := open()
			if e != nil {
				return e
			}
			defer r.Close()
			ref := rev
			if ref != "" {
				ref, e = r.Resolve(ref)
				if e != nil {
					return e
				}
			}
			cfgRef := ref
			if policyFrom != "" {
				if policyFrom != "worktree" {
					return fmt.Errorf("--policy-from accepts worktree for historical inspection only")
				}
				cfgRef = ""
			}
			cfg, e := policy.Load(r, cfgRef)
			if e != nil {
				return e
			}
			if metric != "source" && metric != "non-test" && metric != "non-test-2026-09-16" {
				return fmt.Errorf("unknown metric %q", metric)
			}
			var report *analysis.Report
			if checking {
				report, e = analysis.Check(r, cfg, analysis.CheckOptions{Rev: ref, Paths: paths, Family: family, Case: caseName, Against: against, Changed: changed})
			} else {
				report, e = analysis.Analyze(r, cfg, analysis.Options{Rev: ref, Paths: paths, Family: family, Case: caseName, Metric: metric, Diagnose: true, History: history, HistoryPairs: pairs, Contracts: withContracts, Location: location})
			}
			if e != nil {
				return e
			}
			if policyFrom != "" {
				report.PolicySource = "working policy for historical inspection"
			}
			if !checking && cfgRef == "" {
				now, err := policy.Load(r, "")
				if err != nil {
					return err
				}
				if !report.PolicyCurrent(cfg, now) {
					return &repository.Stale{Message: "input changed: " + policy.File}
				}
			}
			if save != "" {
				if _, e = output.Save(r, save, report, report); e != nil {
					return e
				}
			}
			if rdjson {
				e = output.RDJSON(out, report.Findings)
			} else if a.jsonMode {
				e = output.JSON(out, report)
			} else if checking {
				output.CheckText(out, report, top)
			} else {
				output.Text(out, report, top)
				output.Contracts(out, report.Contracts, top)
			}
			if e != nil {
				return e
			}
			if checking {
				for _, f := range report.Findings {
					if f.Kind == "unsupported_change" || f.Kind == "unmatched_selector" {
						return &Exit{2, fmt.Errorf("inspection includes unsupported or missing inputs")}
					}
				}
				if len(report.Findings) > 0 {
					return &Exit{Code: 1}
				}
			}
			return nil
		}}
		c.Flags().StringVar(&rev, "rev", "", "commit to read source and policy from (default: working files)")
		c.Flags().StringVar(&family, "family", "", "select an inventory family")
		c.Flags().StringVar(&caseName, "case", "", "select a diagnosis case")
		c.Flags().StringVar(&save, "output", "", "save reports in a new ignored or external directory")
		c.Flags().IntVar(&top, "top", 25, "maximum entries per text section; 0 displays all; JSON is complete")
		if checking {
			c.Flags().BoolVar(&changed, "changed", false, "check changed files and their contracts; policy changes expand to a full audit")
			c.Flags().StringVar(&against, "against", "", "baseline commit for policy changes and --changed (default: HEAD); never permits unexplained differences")
			c.Flags().BoolVar(&rdjson, "rdjson", false, "emit all findings as reviewdog diagnostics for local editor tooling")
		} else {
			c.Flags().BoolVar(&withContracts, "contracts", false, "discover applicable obligations and resolve witness symbols without executing tests")
			c.Flags().StringArrayVar(&historyPairs, "history-pair", nil, "compare block-id=hunk-id from this inspection; requires --history; diagnostic only")
			c.Flags().BoolVar(&history, "history", false, "compare retained deletions with selected source; diagnostic only")
			c.Flags().StringVar(&metric, "metric", "source", "source, non-test, or non-test-2026-09-16")
			c.Flags().StringVar(&policyFrom, "policy-from", "", "use worktree policy to inspect revisions predating Concave")
		}
		if checking {
			metric = "source"
		}
		if !checking {
			a.registerQueue(c)
		}
		root.AddCommand(c)
	}
}
