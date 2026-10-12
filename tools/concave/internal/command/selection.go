package command

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"concave/internal/repository"
)

// Selection decisions stay beside each hunk. Revisiting a file changes only its
// decisions; the final ID list and summary are projections of the same state.
type hunkChoice struct {
	hunk     repository.Hunk
	included bool
}

type fileChoices struct {
	path    string
	changes []hunkChoice
}

func (f *fileChoices) count() int {
	count := 0
	for _, change := range f.changes {
		if change.included {
			count++
		}
	}
	return count
}

type selectionPrompt struct {
	ctx     context.Context
	scanner *bufio.Scanner
	output  io.Writer
}

func (p *selectionPrompt) answer(prompt, choices string) (string, error) {
	for {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprint(p.output, prompt)
		if !p.scanner.Scan() {
			if err := p.ctx.Err(); err != nil {
				return "", err
			}
			if err := p.scanner.Err(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("selection ended before confirmation; no candidate prepared")
		}
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
		value := strings.ToLower(strings.TrimSpace(p.scanner.Text()))
		if value == "q" {
			return "", fmt.Errorf("selection cancelled; no candidate prepared")
		}
		if choices == "" || len(value) == 1 && strings.Contains(choices, value) {
			return value, nil
		}
		fmt.Fprintf(p.output, "Choose %s, or q to cancel.\n", strings.Join(strings.Split(choices, ""), ", "))
	}
}

// file returns true when the user requests the preceding file.
func (p *selectionPrompt) file(file *fileChoices) (bool, error) {
	hunks := make([]repository.Hunk, len(file.changes))
	for i, change := range file.changes {
		hunks[i] = change.hunk
	}
	for {
		fmt.Fprintf(p.output, "\n%s (%d/%d changes selected)\n%s", file.path, file.count(), len(hunks), repository.Patch(file.path, hunks))
		choice, err := p.answer("Include this file? [y/n/h hunks/b back/q cancel] ", "ynhb")
		if err != nil {
			return false, err
		}
		switch choice {
		case "b":
			return true, nil
		case "y", "n":
			for i := range file.changes {
				file.changes[i].included = choice == "y"
			}
			return false, nil
		}
		i := 0
		for i >= 0 && i < len(file.changes) {
			change := &file.changes[i]
			state := "excluded"
			if change.included {
				state = "included"
			}
			fmt.Fprintf(p.output, "\n%s (%s)\n%s", change.hunk.ID, state, repository.Patch(file.path, []repository.Hunk{change.hunk}))
			choice, err = p.answer("Include this hunk? [y/n/b back/q cancel] ", "ynb")
			if err != nil {
				return false, err
			}
			if choice == "b" {
				i--
			} else {
				change.included = choice == "y"
				i++
			}
		}
		if i >= 0 {
			return false, nil
		}
		// Back from the first hunk returns to this file's choices.
	}
}

func (p *selectionPrompt) edit(files []fileChoices, index int) (int, error) {
	for {
		back, err := p.file(&files[index])
		if err != nil || !back {
			return index, err
		}
		if index > 0 {
			index--
		} else {
			fmt.Fprintln(p.output, "Already at the first file.")
		}
	}
}

func selectHunks(ctx context.Context, input io.Reader, output io.Writer, hunks []repository.Hunk) ([]string, error) {
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks available; no candidate prepared")
	}
	files := []fileChoices{}
	byPath := map[string]int{}
	for _, hunk := range hunks {
		i, ok := byPath[hunk.Path]
		if !ok {
			i = len(files)
			byPath[hunk.Path] = i
			files = append(files, fileChoices{path: hunk.Path})
		}
		files[i].changes = append(files[i].changes, hunkChoice{hunk: hunk})
	}
	prompt := selectionPrompt{ctx: ctx, scanner: bufio.NewScanner(input), output: output}
	for i := 0; i < len(files); i++ {
		var err error
		i, err = prompt.edit(files, i)
		if err != nil {
			return nil, err
		}
	}
	for {
		selected := []string{}
		selectedFiles := 0
		for _, file := range files {
			if file.count() > 0 {
				selectedFiles++
			}
			for _, change := range file.changes {
				if change.included {
					selected = append(selected, change.hunk.ID)
				}
			}
		}
		fmt.Fprintf(output, "\nSelected: %d/%d changes in %d/%d files\n", len(selected), len(hunks), selectedFiles, len(files))
		for i, file := range files {
			fmt.Fprintf(output, "  [%d] %s (%d/%d changes)\n", i+1, file.path, file.count(), len(file.changes))
			for _, change := range file.changes {
				if change.included {
					fmt.Fprintf(output, "      %s:%d %s\n", file.path, change.hunk.CurrentStart+1, change.hunk.ID)
				}
			}
		}
		choice, err := prompt.answer(fmt.Sprintf("Prepare, edit file number, or cancel? [p/1-%d/q] ", len(files)), "")
		if err != nil {
			return nil, err
		}
		if choice == "p" {
			if len(selected) > 0 {
				return selected, nil
			}
			fmt.Fprintln(output, "No changes selected. Edit a file to include changes, or q to cancel.")
			continue
		}
		i, err := strconv.Atoi(choice)
		if err != nil || i < 1 || i > len(files) {
			fmt.Fprintf(output, "Choose p to prepare, a file number from 1 to %d to edit, or q to cancel.\n", len(files))
			continue
		}
		if _, err := prompt.edit(files, i-1); err != nil {
			return nil, err
		}
	}
}

func terminalSelection(ctx context.Context, hunks []repository.Hunk) ([]string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("--select requires a terminal; use explicit --hunk IDs: %w", err)
	}
	defer tty.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			tty.Close()
		case <-done:
		}
	}()
	return selectHunks(ctx, tty, tty, hunks)
}
