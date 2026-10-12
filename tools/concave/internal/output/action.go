package output

import (
	"fmt"
	"io"

	"concave/internal/continuation"
	"concave/internal/process"
)

// Next presents the same complete continuation exposed by JSON, including
// required human input. Callers bind its repository before rendering.
func Next(out io.Writer, action continuation.Action) {
	fmt.Fprintln(out, "Next:", action.Message)
	if len(action.Argv) > 0 {
		fmt.Fprintln(out, " ", process.ShellCommand(action.Argv))
	}
	for _, input := range action.Requires {
		fmt.Fprintln(out, "  Supply:", input)
	}
}
