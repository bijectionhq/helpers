package command

import (
	"concave/internal/analysis"
	"fmt"
	"strconv"
)

type reviewMode uint8

const (
	reviewGroups reviewMode = iota
	reviewFiles
	reviewContracts
)

type reviewFlags struct {
	revision, family, group, against, caseName, output, callers string
	files, contracts                                            bool
	callerPaths                                                 []string
	top                                                         int
}

type reviewRequest struct {
	mode                            reviewMode
	options                         analysis.Options
	group, against, output, callers string
	top                             int
}

// Flags become one validated request before any repository work begins.
func (flags reviewFlags) request(paths []string) (reviewRequest, error) {
	request := reviewRequest{options: analysis.Options{Rev: flags.revision, Paths: paths, Family: flags.family, Case: flags.caseName, CallerPaths: flags.callerPaths}, group: flags.group, against: flags.against, output: flags.output, callers: flags.callers, top: flags.top}
	switch {
	case !flags.contracts && (flags.against != "" || flags.caseName != "" || flags.output != ""):
		return request, fmt.Errorf("--against, --case and --output require --contracts")
	case flags.contracts && (flags.files || flags.group != ""):
		return request, fmt.Errorf("--contracts is separate from --files and --group")
	case len(flags.callerPaths) > 0 && flags.callers == "":
		return request, fmt.Errorf("--caller-path requires --callers")
	case flags.callers != "" && (!flags.contracts || flags.revision != ""):
		return request, fmt.Errorf("--callers requires a working-file --contracts change context")
	case flags.caseName != "" && (flags.family != "" || len(paths) > 0):
		return request, fmt.Errorf("choose a case or path/family scope")
	case flags.top < 0:
		return request, fmt.Errorf("--top must be nonnegative")
	case flags.files && flags.group != "":
		return request, fmt.Errorf("--files and --group are separate views; select a path to narrow the file view")
	}
	if flags.contracts {
		request.mode = reviewContracts
	} else if flags.files {
		request.mode = reviewFiles
	}
	return request, nil
}

func (request reviewRequest) continuation(root, baseline, path string) []string {
	args := []string{"concave", "--repo", root, "inspect", "queue", "--contracts"}
	args = append(args, request.options.Paths...)
	if request.options.Family != "" {
		args = append(args, "--family", request.options.Family)
	}
	if request.options.Case != "" {
		args = append(args, "--case", request.options.Case)
	}
	return append(args, "--against", baseline, "--callers", absolute(request.callers), "--caller-path", path)
}

// Queue navigation carries the resolved revision and explicit scope forward.
// A working review stays working; presentation never promises a frozen snapshot.
func (request reviewRequest) queueArgs(root string) []string {
	args := []string{"concave", "--repo", root, "inspect", "queue"}
	if request.options.Rev != "" {
		args = append(args, "--rev", request.options.Rev)
	}
	if request.options.Family != "" {
		args = append(args, "--family", request.options.Family)
	}
	if request.mode == reviewFiles {
		args = append(args, "--files")
	}
	if request.group != "" {
		args = append(args, "--group", request.group)
	}
	args = append(args, "--top", strconv.Itoa(request.top))
	if len(request.options.Paths) > 0 {
		args = append(args, "--")
		args = append(args, request.options.Paths...)
	}
	return args
}
