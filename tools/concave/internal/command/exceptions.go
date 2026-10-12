package command

import (
	"concave/internal/repository"
	"fmt"

	"concave/internal/exceptions"
	"concave/internal/output"
	"concave/internal/policy"
	"github.com/spf13/cobra"
)

func (a *app) registerExceptions(root *cobra.Command) {
	group := &cobra.Command{Use: "exception", Short: "Explain, record and explicitly refresh bounded exceptions"}
	var rev string
	explain := &cobra.Command{Use: "explain <id> [id...]", Args: cobra.RangeArgs(1, exceptions.MaxChanges), Short: "Explain selected bindings with one source and history analysis", RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		ref := rev
		if ref != "" {
			ref, err = repo.Resolve(ref)
			if err != nil {
				return err
			}
		}
		config, err := policy.Load(repo, ref)
		if err != nil {
			return err
		}
		explanations, err := exceptions.Explain(repo, config, args, ref)
		if err != nil {
			return err
		}
		if a.jsonMode {
			return output.JSON(a.out, explanations)
		}
		for index, explanation := range explanations {
			if index > 0 {
				fmt.Fprintln(a.out)
			}
			if err := output.ExceptionExplanation(a.out, explanation); err != nil {
				return err
			}
		}
		return nil
	}}
	explain.Flags().StringVar(&rev, "rev", "", "commit to read source and policy from (default: working files)")
	group.AddCommand(explain)
	for _, action := range []exceptions.Action{exceptions.Record, exceptions.Refresh, exceptions.Remove} {
		options := exceptions.Options{}
		write := false
		active := false
		expect := ""
		contributionRef := ""
		use, argsCheck := string(action)+" <id> [id...]", cobra.RangeArgs(1, exceptions.MaxChanges)
		cmd := &cobra.Command{Use: use, Args: argsCheck, Short: map[exceptions.Action]string{exceptions.Record: "Preview a bounded source or file exception, then write that exact proposal", exceptions.Refresh: "Preview refreshed or obsolete bindings, then write that exact proposal", exceptions.Remove: "Preview retiring bindings and the findings they leave, then write that exact proposal"}[action], RunE: func(cmd *cobra.Command, args []string) error {
			if write && expect == "" {
				return fmt.Errorf("--write requires --expect <proposal_sha256> from a preview")
			}
			if !write && expect != "" {
				return fmt.Errorf("--expect requires --write")
			}
			repo, err := a.open()
			if err != nil {
				return err
			}
			defer repo.Close()
			config, err := policy.Load(repo, "")
			if err != nil {
				return err
			}
			request := exceptionRequest{Action: action, IDs: args, Options: options, Write: write, Expect: expect, Contribution: contributionRef}
			if write && !active {
				argv := append(request.args(), "--writer-active")
				if a.jsonMode {
					argv = append(argv, "--json")
				}
				return reenter(a.ctx, repo, config, argv, a.out, a.errout)
			}
			proposal, err := exceptions.Prepare(repo, config, action, args, options)
			if err != nil {
				return err
			}
			if write && proposal.ProposalSHA256 != expect {
				return &repository.Stale{Message: "proposal fingerprint changed; preview again"}
			}
			paths := []string{}
			for _, change := range proposal.Changes {
				for _, binding := range change.Previous {
					paths = append(paths, binding.Path)
				}
				if change.Next != nil {
					paths = append(paths, change.Next.Path)
				}
			}
			parent, err := editCapture(repo, config, contributionRef, paths)
			if err != nil {
				return err
			}
			if write {
				if err = exceptions.Write(repo, config, proposal); err != nil {
					return err
				}
				if proposal.Status != exceptions.Unchanged {
					proposal.Status = exceptions.Recorded
				}
				if parent != nil {
					child, err := editedCapture(repo, parent)
					if err != nil {
						return err
					}
					child.Approvals = append(child.Approvals, *proposal)
					return a.saveContribution(repo, child)
				}
			}
			if a.jsonMode {
				return output.JSON(a.out, proposal)
			}
			var writeCommand []string
			if !write && proposal.Status != exceptions.Unchanged {
				request.IDs = nil
				for _, change := range proposal.Changes {
					request.IDs = append(request.IDs, change.ID)
				}
				request.Write, request.Expect = true, proposal.ProposalSHA256
				writeCommand = append([]string{"concave", "--repo", repo.Root}, request.args()...)
			}
			return output.ExceptionProposal(a.out, proposal, writeCommand)
		}}
		if action != exceptions.Remove {
			cmd.Flags().StringVar(&options.Path, "path", "", "repo-relative source path; optional when the fence or binding identifies it")
		}
		if action == exceptions.Remove {
		} else if action == exceptions.Record {
			cmd.Flags().BoolVar(&options.File, "file", false, "preview a bounded binary replacement or file retirement; requires --path")
			cmd.Flags().StringArrayVar(&options.Hunks, "hunk", nil, "explicit current hunk ID (repeatable); omit for a new fence, otherwise select every enclosed hunk")
		} else {
			cmd.Flags().BoolVar(&options.File, "file", false, "refresh a file binding: binary replacement or retirement")
			cmd.Flags().StringArrayVar(&options.Hunks, "hunk", nil, "explicit current hunk ID (repeatable); required when the upstream anchor or original span changed")
		}
		if action == exceptions.Record {
			cmd.Flags().BoolVar(&options.Annotate, "annotate", false, "preview source fences and exact bindings together for selected hunks")
			cmd.Flags().StringArrayVar(&options.Replace, "replace", nil, "existing contained binding to replace (repeatable)")
		}
		if action != exceptions.Remove {
			cmd.Flags().StringVar(&options.Metadata.Why, "why", "", "reason for an unfenced span or file binding")
			cmd.Flags().StringArrayVar(&options.Metadata.Witnesses, "witness", nil, "witness reference for an unfenced span or file binding (repeatable)")
			cmd.Flags().StringArrayVar(&options.Metadata.Preserves, "preserves", nil, "source/path#invariant for an unfenced span or file binding (repeatable)")
			cmd.Flags().StringVar(&options.Metadata.RemoveWhen, "remove-when", "", "removal condition for an unfenced span or file binding")
		}
		cmd.Flags().BoolVar(&write, "write", false, "write the exact proposal selected by --expect")
		cmd.Flags().StringVar(&expect, "expect", "", "proposal fingerprint (proposal_sha256) from the preview")
		cmd.Flags().StringVar(&contributionRef, "contribution", "", "capture this contribution again after the guarded write; scope never expands")
		cmd.Flags().BoolVar(&active, "writer-active", false, "internal repository writer reentry")
		cmd.Flags().MarkHidden("writer-active")
		group.AddCommand(cmd)
	}
	migrateWrite, migrateActive := false, false
	migrate := &cobra.Command{Use: "migrate", Args: cobra.NoArgs, Short: "Convert the retired " + exceptions.RetiredBindings + " into per-path binding files", RunE: func(cmd *cobra.Command, args []string) error {
		repo, err := a.open()
		if err != nil {
			return err
		}
		defer repo.Close()
		config, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		if migrateWrite && !migrateActive {
			argv := []string{"exception", "migrate", "--write", "--writer-active"}
			if a.jsonMode {
				argv = append(argv, "--json")
			}
			return reenter(a.ctx, repo, config, argv, a.out, a.errout)
		}
		migration, err := exceptions.PrepareMigration(repo, config)
		if err != nil {
			return err
		}
		if migrateWrite {
			if err := migration.Write(repo); err != nil {
				return err
			}
		}
		if a.jsonMode {
			return output.JSON(a.out, migration)
		}
		verb := "Would write %d binding files and delete %d"
		if migrateWrite {
			verb = "Wrote %d binding files and deleted %d"
		}
		fmt.Fprintf(a.out, verb+" for the %d bindings in %s; %d already current.\n", len(migration.Written), len(migration.Deleted), migration.Bindings, exceptions.RetiredBindings, migration.Unchanged)
		if migration.Base != "" {
			fmt.Fprintf(a.out, "Only paths whose bindings differ from %s at %s were converted.\n", exceptions.RetiredBindings, migration.Base)
		}
		if !migrateWrite {
			fmt.Fprintf(a.out, "Next: concave --repo %s exception migrate --write\n", repo.Root)
		} else {
			fmt.Fprintf(a.out, "Removed %s. Land the binding files under %s/ beside it.\n", exceptions.RetiredBindings, policy.BindingsDirectory)
		}
		return nil
	}}
	migrate.Flags().BoolVar(&migrateWrite, "write", false, "write the binding files and delete the retired file")
	migrate.Flags().BoolVar(&migrateActive, "writer-active", false, "internal repository writer reentry")
	migrate.Flags().MarkHidden("writer-active")
	group.AddCommand(migrate)
	root.AddCommand(group)
}

// One serializer owns both writer reentry and the preview's write command.
// Transport flags (--writer-active and --json) are added only by the caller.
type exceptionRequest struct {
	Action       exceptions.Action
	IDs          []string
	Options      exceptions.Options
	Write        bool
	Expect       string
	Contribution string
}

func (request exceptionRequest) args() []string {
	args := append([]string{"exception", string(request.Action)}, request.IDs...)
	if request.Write {
		args = append(args, "--write", "--expect", request.Expect)
	}
	add := func(flag, value string) {
		if value != "" {
			args = append(args, flag, value)
		}
	}
	options := request.Options
	add("--contribution", request.Contribution)
	if options.Annotate {
		args = append(args, "--annotate")
	}
	add("--path", options.Path)
	if options.File {
		args = append(args, "--file")
	}
	for _, id := range options.Hunks {
		add("--hunk", id)
	}
	for _, id := range options.Replace {
		add("--replace", id)
	}
	add("--why", options.Metadata.Why)
	for _, witness := range options.Metadata.Witnesses {
		add("--witness", witness)
	}
	add("--remove-when", options.Metadata.RemoveWhen)
	for _, reference := range options.Metadata.Preserves {
		add("--preserves", reference)
	}
	return args
}
