package command

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"concave/internal/analysis"
	"concave/internal/artifact"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/recovery"
)

func (a *app) registerRecovery(root *cobra.Command) {
	open := a.open
	out, errout := a.out, a.errout
	recoverCmd := &cobra.Command{Use: "recover", Short: "Plan and apply explicitly selected source recovery"}
	var ids, creates []string
	var patchPath, reason, destination, documentPath string
	planCmd := &cobra.Command{Use: "plan <inspection.json>", Args: cobra.ExactArgs(1), Short: "Prepare an inert patch with exact before and after hashes", RunE: func(cmd *cobra.Command, args []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer r.Close()
		c, e := policy.Load(r, "")
		if e != nil {
			return e
		}
		report := new(analysis.Report)
		raw, e := artifact.Read(args[0], report)
		if e != nil {
			return e
		}
		var patch []byte
		if patchPath != "" {
			f, err := os.Open(patchPath)
			if err != nil {
				return err
			}
			defer f.Close()
			patch, e = io.ReadAll(io.LimitReader(f, (4<<20)+1))
			if e != nil {
				return e
			}
			if len(patch) > 4<<20 {
				return fmt.Errorf("patch exceeds 4 MiB")
			}
		}
		var p *recovery.Plan
		if documentPath != "" {
			if len(ids) > 0 || patchPath != "" || len(creates) > 0 {
				return fmt.Errorf("choose --document, --hunk or --patch")
			}
			p, e = recovery.PrepareDocument(r, c, report, raw, documentPath, reason)
		} else {
			p, e = recovery.Prepare(r, c, report, raw, ids, patch, reason, creates...)
		}
		if e != nil {
			return e
		}
		dir, e := output.Save(r, destination, p, nil)
		if e != nil {
			return e
		}
		if e = os.WriteFile(filepath.Join(dir, "recovery.patch"), []byte(p.Patch), 0600); e != nil {
			return e
		}
		if a.jsonMode {
			return output.JSON(out, map[string]any{"format": 1, "plan": filepath.Join(dir, "plan.json"), "applied": false, "patch_sha256": p.PatchSHA256})
		}
		output.RecoveryPreview(out, p, report, c, r.Root, dir)
		return nil
	}}
	planCmd.Flags().StringSliceVar(&ids, "hunk", nil, "explicit hunk ID (repeatable)")
	planCmd.Flags().StringArrayVar(&creates, "create", nil, "explicit absent fork-owned regular file to create with --patch (repeatable)")
	planCmd.Flags().StringVar(&documentPath, "document", "", "restore equivalent copied examples in one native Docusaurus document")
	planCmd.Flags().StringVar(&patchPath, "patch", "", "reviewed boundary patch instead of hunk selection")
	planCmd.Flags().StringVar(&reason, "reason", "", "concrete recovery reason")
	planCmd.Flags().StringVar(&destination, "output", "", "new ignored or external plan directory")
	planCmd.MarkFlagRequired("reason")
	planCmd.MarkFlagRequired("output")
	applied := false
	checkCmd := &cobra.Command{Use: "check <plan.json>", Args: cobra.ExactArgs(1), Short: "Recheck a saved plan without editing source or invoking the writer", RunE: func(cmd *cobra.Command, args []string) error {
		r, err := open()
		if err != nil {
			return err
		}
		defer r.Close()
		c, err := policy.Load(r, "")
		if err != nil {
			return err
		}
		p := new(recovery.Plan)
		if _, err := artifact.Read(args[0], p); err != nil {
			return err
		}
		validate, status := recovery.Validate, "applicable"
		if applied {
			validate, status = recovery.ValidateApplied, "applied"
		}
		if err := validate(r, c, p); err != nil {
			return err
		}
		return output.JSON(out, map[string]any{"format": 1, "status": status, "applied": applied, "files": p.Files, "verification": "pending", "published": false})
	}}
	checkCmd.Flags().BoolVar(&applied, "applied", false, "validate the recorded result instead of applicability")
	active := false
	var contributionRef string
	applyCmd := &cobra.Command{Use: "apply <plan.json>", Args: cobra.ExactArgs(1), Short: "Validate and apply a prepared plan through the repository writer", RunE: func(cmd *cobra.Command, args []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer r.Close()
		c, e := policy.Load(r, "")
		if e != nil {
			return e
		}
		if !active {
			argv := []string{"recover", "apply", absolute(args[0]), "--writer-active"}
			if contributionRef != "" {
				argv = append(argv, "--contribution", contributionRef)
			}
			if a.jsonMode {
				argv = append(argv, "--json")
			}
			return reenter(a.ctx, r, c, argv, out, errout)
		}
		p := new(recovery.Plan)
		if _, e = artifact.Read(args[0], p); e != nil {
			return e
		}
		paths := []string{}
		for path := range p.Files {
			paths = append(paths, path)
		}
		parent, e := editCapture(r, c, contributionRef, paths)
		if e != nil {
			return e
		}
		if e = recovery.Apply(r, c, p); e != nil {
			return e
		}
		if parent != nil {
			child, e := editedCapture(r, parent)
			if e != nil {
				return e
			}
			child.Recoveries = append(child.Recoveries, *p)
			return a.saveContribution(r, child)
		}
		if a.jsonMode {
			return output.JSON(out, map[string]any{"format": 1, "status": "applied", "files": p.Files, "verification": "pending", "published": false})
		}
		fmt.Fprintf(out, "Recovery applied · %s. Behavioral checks not run.\n", output.Count(len(paths), "file"))
		sort.Strings(paths)
		next := append([]string{"concave", "--repo", r.Root, "review"}, paths...)
		next = append(next, "--from", absolute(args[0]))
		fmt.Fprintln(out, "Next:", process.ShellCommand(next))
		return nil
	}}
	applyCmd.Flags().BoolVar(&active, "writer-active", false, "internal writer reentry")
	applyCmd.Flags().MarkHidden("writer-active")
	applyCmd.Flags().StringVar(&contributionRef, "contribution", "", "capture this contribution again after the guarded write; scope never expands")
	recoverCmd.AddCommand(planCmd, checkCmd, applyCmd)
	root.AddCommand(recoverCmd)
}
