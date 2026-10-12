// Package command exposes Concave without importing any Bijection product code.
package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"concave/internal/landing"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/syntax"
)

type app struct {
	ctx               context.Context
	out, errout       io.Writer
	repoPath          string
	actionRoot        string
	jsonMode, timings bool
	repos             []*repository.Repo
}

func (a *app) open() (*repository.Repo, error) {
	repo, err := repository.Open(a.ctx, a.repoPath)
	if err == nil {
		a.repos = append(a.repos, repo)
	}
	return repo, err
}

type Exit struct {
	Code int
	Err  error
}

func (e *Exit) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "policy violations"
}
func Run(ctx context.Context, args []string, out, errout io.Writer) int {
	a := &app{ctx: ctx, out: out, errout: errout, repoPath: "."}
	c := a.command()
	started := time.Now()
	defer func() {
		if !a.timings {
			return
		}
		var processes, reads, blobs int
		for _, repo := range a.repos {
			processes += repo.Processes
			reads += repo.Reads
			blobs += repo.BlobReads
		}
		fmt.Fprintf(errout, "elapsed=%s git_processes=%d source_reads=%d blob_reads=%d\n", time.Since(started).Round(time.Millisecond), processes, reads, blobs)
	}()
	c.SetArgs(args)
	timeout := 2 * time.Minute
	if target, _, err := c.Find(args); err == nil {
		if limit := target.Annotations["timeout"]; limit != "" {
			if limit == "policy" {
				timeout = 0 // Execution derives its deadline after reading the selected checks.
			} else if duration, e := time.ParseDuration(limit); e == nil {
				timeout = duration
			}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	if timeout > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(a.ctx, timeout)
	}
	defer cancel()
	ctx = process.WithProgress(ctx, func(observation process.Observation) {
		if !observation.Finished && observation.Detail != "" {
			fmt.Fprintf(errout, "%s: %s\n", observation.Phase, observation.Detail)
		} else if observation.Finished && a.timings {
			fmt.Fprintf(errout, "phase=%s duration_ms=%d\n", observation.Phase, observation.Elapsed.Milliseconds())
		}
	})
	c.SetContext(ctx)
	e := c.Execute()
	if e == nil {
		return 0
	}
	var x *Exit
	if errors.As(e, &x) {
		if x.Err != nil {
			fmt.Fprintln(errout, "concave:", x.Err)
		}
		return x.Code
	}
	var status *landing.Failure
	if errors.As(e, &status) {
		fmt.Fprintln(errout, "concave:", e)
		return status.ExitCode()
	}
	if repository.IsStale(e) {
		fmt.Fprintln(errout, "concave:", e)
		return 3
	}
	fmt.Fprintln(errout, "concave:", e)
	return 2
}
func (a *app) command() *cobra.Command {
	ctx, out, errout := a.ctx, a.out, a.errout
	root := &cobra.Command{Use: "concave", Short: "Inspect, enforce and recover upstream source parity", SilenceUsage: true, SilenceErrors: true, Version: "1"}
	root.SetOut(out)
	root.SetErr(errout)
	root.SetContext(ctx)
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) { a.ctx = cmd.Context() }
	root.PersistentFlags().StringVar(&a.repoPath, "repo", ".", "repository directory")
	root.PersistentFlags().BoolVar(&a.jsonMode, "json", false, "emit versioned JSON")
	root.PersistentFlags().BoolVar(&a.timings, "timings", false, "report elapsed time, available phases, Git processes and input reads on stderr")
	open := func() (*repository.Repo, error) { return a.open() }
	a.registerNaming(root)
	a.registerInspection(root)
	a.registerContribution(root)
	a.registerRules(root)
	a.registerCompare(root)
	a.registerSurface(root)
	a.registerVerification(root)
	engine := &cobra.Command{Use: "engine", Short: "Manage the pinned local syntax engine"}
	engine.AddCommand(&cobra.Command{Use: "install", Args: cobra.NoArgs, Short: "Download and verify the pinned ast-grep binary into ignored build output", RunE: func(cmd *cobra.Command, args []string) error {
		r, err := open()
		if err != nil {
			return err
		}
		defer r.Close()
		path, err := syntax.Install(r)
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(out, map[string]any{"engine": "ast-grep", "version": syntax.Version, "path": path})
		}
		fmt.Fprintln(out, path)
		return nil
	}})
	root.AddCommand(engine)
	root.AddCommand(&cobra.Command{Use: "list", Short: "List families, cases, exceptions, transformations and derived files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		r, e := open()
		if e != nil {
			return e
		}
		defer r.Close()
		c, e := policy.Load(r, "")
		if e != nil {
			return e
		}
		if !a.jsonMode {
			output.List(out, c)
			return nil
		}
		return output.JSON(out, map[string]any{"format": 1, "rules": policy.Rules, "families": c.Families, "cases": c.Cases, "exceptions": c.Exceptions, "transforms": c.Transforms, "restorations": c.Restorations, "derived": c.Derived, "packaging_transformations": c.Manifest, "policy_sha256": c.SHA256})
	}})
	a.registerRecovery(root)
	a.registerExceptions(root)
	return root
}
func absolute(p string) string {
	a, e := filepath.Abs(p)
	if e != nil {
		return p
	}
	return a
}
func reenter(ctx context.Context, r *repository.Repo, c *policy.Config, args []string, out, errout io.Writer) error {
	if len(c.Writer) == 0 {
		return fmt.Errorf("repository must configure a writer command for mutations")
	}
	self, e := os.Executable()
	if e != nil {
		return e
	}
	argv := append(append([]string{}, c.Writer[1:]...), self, "--repo", r.Root)
	argv = append(argv, args...)
	result, err := process.RunOutput(ctx, r.Root, append([]string{c.Writer[0]}, argv...), nil, repository.MaxOutput, out, errout)
	if err != nil {
		code := result.ExitCode
		if code < 1 || code > 3 {
			code = 3
		}
		return &Exit{Code: code, Err: fmt.Errorf("repository writer refused or command failed: %w", err)}
	}
	return nil
}
