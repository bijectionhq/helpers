package output

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"concave/internal/review"
)

// CheckResult renders the same runner accounting for named checks and candidates.
func CheckResult(w io.Writer, check review.CheckOutcome) {
	if check.Results.Format == "command" {
		fmt.Fprintf(w, "%s: %s; command check\n", check.Name, check.Status)
	} else {
		fmt.Fprintf(w, "%s: %s; %d executed, %d passed, %d failed, %d skipped\n", check.Name, check.Status, check.Results.Executed, check.Results.Passed, check.Results.Failed, check.Results.Skipped)
	}
	if check.Mode != "" {
		fmt.Fprintf(w, "  Evidence: %s\n", check.Mode)
	}
	shown, explained := 0, 0
	for _, result := range check.Results.Cases {
		if result.Status != "failed" {
			continue
		}
		if shown == 5 {
			fmt.Fprintln(w, "  More failures in the saved receipt.")
			break
		}
		fmt.Fprintln(w, "  Failed:", result.ID)
		if result.Diagnostic != "" {
			fmt.Fprintln(w, "    "+strings.ReplaceAll(shortDiagnostic(checkDiagnostic(check.Results.Format, result.Diagnostic)), "\n", "\n    "))
			explained++
		}
		shown++
	}
	for _, problem := range check.Problems {
		fmt.Fprintf(w, "  %s/%s: %s\n", problem.Phase, problem.Code, problem.Message)
	}
	if len(check.Problems) > 0 {
		if !check.ExecutionStarted {
			fmt.Fprintln(w, "  Check execution did not start.")
		}
		if explained == 0 {
			if tail := logTail(filepath.Join(check.Directory, "stdout.log")); tail != "" {
				fmt.Fprintln(w, "  Output tail:\n"+tail)
			}
		}
		if explained == 0 {
			if tail := logTail(filepath.Join(check.Directory, "stderr.log")); tail != "" {
				fmt.Fprintln(w, "  Error output tail:\n"+tail)
			}
		}
		fmt.Fprintln(w, "  Logs:", filepath.Join(check.Directory, "stdout.log"), filepath.Join(check.Directory, "stderr.log"))
	}
}

// Node's TAP diagnostics wrap the assertion in timing, location and stack
// metadata. Display its literal error block; retain the original in the receipt.
func checkDiagnostic(format, diagnostic string) string {
	if format != "node-test-tap" {
		return diagnostic
	}
	lines := strings.Split(diagnostic, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "error: |-" && strings.TrimSpace(line) != "error: |" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		message := []string{}
		for _, next := range lines[index+1:] {
			depth := len(next) - len(strings.TrimLeft(next, " "))
			if strings.TrimSpace(next) != "" && depth <= indent {
				break
			}
			message = append(message, strings.TrimPrefix(next, strings.Repeat(" ", indent+2)))
		}
		if text := strings.TrimSpace(strings.Join(message, "\n")); text != "" {
			return text
		}
	}
	return diagnostic
}

// Bound human feedback; structured receipts and runner logs keep the full data.
func shortDiagnostic(diagnostic string) string {
	const maxBytes = 2048
	lines := strings.Split(diagnostic, "\n")
	shortened := len(lines) > 12
	if shortened {
		lines = lines[:12]
	}
	text := strings.Join(lines, "\n")
	if len(text) > maxBytes {
		text = strings.ToValidUTF8(text[:maxBytes], "�")
		shortened = true
	}
	if shortened {
		text += "\n… full diagnostic in the saved receipt and logs."
	}
	return text
}

func logTail(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	const limit = 2048
	start := max(int64(0), info.Size()-limit)
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(data), "�"))
}
