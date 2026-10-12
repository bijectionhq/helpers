package command

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"concave/internal/artifact"
	"concave/internal/output"
	"concave/internal/surface"
)

func (a *app) registerSurface(root *cobra.Command) {
	group := &cobra.Command{Use: "surface", Short: "Capture TypeScript APIs and discover imports using the installed compiler"}
	for _, mode := range []surface.Mode{surface.Snapshot, surface.Callers} {
		options := surface.Options{Mode: mode}
		var modules []string
		top := 20
		cmd := &cobra.Command{Use: string(mode) + " <entry.ts> [entry.ts...]", Args: cobra.MinimumNArgs(1), Short: map[surface.Mode]string{surface.Snapshot: "Capture exported names, signatures, owners and runtime names of TypeScript entries", surface.Callers: "Find the source files that import TypeScript entries, with their references"}[mode], RunE: func(cmd *cobra.Command, args []string) error {
			if top < 0 {
				return fmt.Errorf("--top must be nonnegative")
			}
			options.Modules = map[string]string{}
			for _, binding := range modules {
				module, entry, ok := strings.Cut(binding, "=")
				if !ok || module == "" || entry == "" {
					return fmt.Errorf("--module requires specifier=entry-path")
				}
				if _, exists := options.Modules[module]; exists {
					return fmt.Errorf("duplicate module mapping %q", module)
				}
				options.Modules[module] = entry
			}
			repo, err := a.open()
			if err != nil {
				return err
			}
			defer repo.Close()
			options.Entries = args
			report, err := surface.Capture(repo, options)
			if err != nil {
				return err
			}
			if a.timings {
				phases := make([]string, 0, len(report.Timings))
				for phase := range report.Timings {
					phases = append(phases, phase)
				}
				sort.Strings(phases)
				for _, phase := range phases {
					fmt.Fprintf(a.errout, "phase=surface.%s duration=%s\n", phase, report.Timings[phase].Round(time.Millisecond))
				}
			}
			if a.jsonMode {
				err = output.JSON(a.out, report)
			} else {
				output.Surface(a.out, report, top)
			}
			if err != nil {
				return err
			}
			if report.Status == surface.Incomplete {
				return &Exit{Code: 1}
			}
			return nil
		}}
		cmd.Flags().IntVar(&top, "top", 20, "maximum displayed compiler owners, callers and coverage issues; 0 displays all; JSON is complete")
		cmd.Flags().StringVar(&options.Project, "project", "", "repo-relative tsconfig (default: ES2022/bundler compiler options)")
		if mode == surface.Callers {
			cmd.Flags().StringArrayVar(&modules, "module", nil, "explicit specifier=entry-path mapping (repeatable)")
		}
		group.AddCommand(cmd)
	}
	group.AddCommand(&cobra.Command{Use: "compare <before.json> <after.json>", Args: cobra.ExactArgs(2), Short: "Compare matching API snapshots or caller artifacts, including coverage and analysis basis", RunE: func(cmd *cobra.Command, args []string) error {
		var before, after surface.Report
		if _, err := artifact.Read(args[0], &before); err != nil {
			return err
		}
		if _, err := artifact.Read(args[1], &after); err != nil {
			return err
		}
		report, err := surface.Compare(&before, &after)
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(a.out, report)
		}
		output.SurfaceComparison(a.out, report)
		return nil
	}})
	root.AddCommand(group)
}
