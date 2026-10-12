package command

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"concave/internal/analysis"
	"concave/internal/artifact"
	"concave/internal/output"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/surface"
)

func (a *app) registerQueue(root *cobra.Command) {
	flags := reviewFlags{top: 20}
	c := &cobra.Command{Use: "queue [path ...]", Short: "Group unresolved findings by identical edit or by file; --contracts prepares a change context", RunE: func(cmd *cobra.Command, paths []string) error {
		request, err := flags.request(paths)
		if err != nil {
			return err
		}
		return a.runReview(request)
	}}
	c.Flags().StringVar(&flags.revision, "rev", "", "commit to read source and policy from (default: working files)")
	c.Flags().StringArrayVar(&flags.callerPaths, "caller-path", nil, "resolve callers in a literal file or directory/ (repeatable); requires --callers")
	c.Flags().StringVar(&flags.callers, "callers", "", "attach a current surface callers artifact to the change context")
	c.Flags().BoolVar(&flags.contracts, "contracts", false, "prepare the change context: baseline and candidate obligations, sources and callers; does not invoke an agent")
	c.Flags().StringVar(&flags.against, "against", "", "baseline commit (default: HEAD); upstream stays pinned")
	c.Flags().StringVar(&flags.caseName, "case", "", "select a diagnosis case for the change context")
	c.Flags().StringVar(&flags.output, "output", "", "save context.json in a new ignored or external directory")
	c.Flags().StringVar(&flags.family, "family", "", "select an inventory family")
	c.Flags().StringVar(&flags.group, "group", "", "show one exact edit group and its hunk locations")
	c.Flags().BoolVar(&flags.files, "files", false, "list files with findings, ordered by unresolved edit size")
	c.Flags().IntVar(&flags.top, "top", 20, "maximum displayed files, groups or occurrences; 0 displays all; JSON is complete")
	root.AddCommand(c)
}

func (a *app) runReview(request reviewRequest) error {
	repo, err := a.open()
	if err != nil {
		return err
	}
	defer repo.Close()
	if request.options.Rev != "" {
		request.options.Rev, err = repo.Resolve(request.options.Rev)
		if err != nil {
			return err
		}
	}
	config, err := policy.Load(repo, request.options.Rev)
	if err != nil {
		return err
	}
	if request.mode == reviewContracts {
		return a.runContractReview(repo, config, request)
	}
	report, err := analysis.Analyze(repo, config, request.options)
	if err != nil {
		return err
	}
	if request.options.Rev == "" {
		current, err := policy.Load(repo, "")
		if err != nil {
			return err
		}
		if !report.PolicyCurrent(config, current) {
			return &repository.Stale{Message: "input changed: " + policy.File}
		}
	}
	queue := analysis.BuildFindingQueue(report)
	if request.mode == reviewFiles {
		view := analysis.FindingFiles(queue)
		if a.jsonMode {
			return output.JSON(a.out, view)
		}
		output.FindingFiles(a.out, view, request.top)
		if len(view.Files) > 0 {
			next := request
			next.mode = reviewGroups
			next.options.Paths = []string{view.Files[0].Path}
			fmt.Fprintln(a.out, "Open first file:", process.ShellCommand(next.queueArgs(repo.Root)))
		}
		if request.top > 0 && request.top < len(view.Files) {
			request.top = 0
			fmt.Fprintln(a.out, "Show all files:", process.ShellCommand(request.queueArgs(repo.Root)))
		}
		return nil
	}
	if request.group != "" {
		found := false
		for _, group := range queue.Groups {
			if group.ID == request.group {
				queue.Groups = []analysis.FindingGroup{group}
				queue.SelectedGroup = request.group
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("edit group %s is absent from this snapshot and scope; rerun inspect queue", request.group)
		}
	}
	if a.jsonMode {
		return output.JSON(a.out, queue)
	}
	output.FindingQueue(a.out, queue, request.top)
	if queue.SelectedGroup != "" && queue.Groups[0].TextHunk && (request.options.Family != "" || len(request.options.Paths) > 0) {
		next := request
		next.options.Family, next.options.Paths, next.top = "", nil, 0
		fmt.Fprintln(a.out, "Find same edit across repository:", process.ShellCommand(next.queueArgs(repo.Root)))
	}
	if len(queue.Groups) > 0 && (request.group == "" || request.top > 0 && request.top < len(queue.Groups[0].Occurrences)) {
		next := request
		next.group, next.top = queue.Groups[0].ID, 0
		fmt.Fprintln(a.out, "Open group:", process.ShellCommand(next.queueArgs(repo.Root)))
	}
	if request.top > 0 && request.top < len(queue.Groups) {
		request.top = 0
		fmt.Fprintln(a.out, "Show all groups:", process.ShellCommand(request.queueArgs(repo.Root)))
	}
	return nil
}

func (a *app) runContractReview(repo *repository.Repo, config *policy.Config, request reviewRequest) error {
	var callers *surface.Report
	if request.callers != "" {
		callers = &surface.Report{}
		if _, err := artifact.Read(request.callers, callers); err != nil {
			return err
		}
	}
	packet, err := analysis.ReviewContracts(repo, config, request.options, request.against, callers)
	if err != nil {
		return err
	}
	if request.output != "" {
		dir, err := repo.OutputDirectory(request.output)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(dir, "context.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		err = output.JSON(file, packet)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if a.jsonMode {
		return output.JSON(a.out, packet)
	}
	output.ContractReview(a.out, packet, request.top)
	if packet.CallerCoverage == nil || packet.CallerCoverage.NextAction == nil {
		return nil
	}
	action := packet.CallerCoverage.NextAction
	switch action.Kind {
	case analysis.NarrowCallerScope:
		fmt.Fprintln(a.out, "Next caller scope:", process.ShellCommand(request.continuation(repo.Root, packet.Against, action.Path)))
	case analysis.InspectCallerGaps:
		fmt.Fprintln(a.out, "Next: review the unresolved paths and reasons in --json; narrowing this scope cannot resolve them.")
	default:
		return fmt.Errorf("unknown caller action %q", action.Kind)
	}
	return nil
}
