package document

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"concave/internal/artifact"
	"concave/internal/process"
	"concave/internal/repository"
)

// execute runs one parser process. Its deadline is 30 s plus 1 s per 64 sources.
func (e *Engine) execute(request []byte, sources int) (process.Result, error) {
	if len(request) > repository.MaxOutput {
		return process.Result{}, fmt.Errorf("MDX request exceeds bound")
	}
	if e.node == "" {
		version, err := os.ReadFile(filepath.Join(e.root, ".nvmrc"))
		if err != nil {
			return process.Result{}, err
		}
		e.node, err = process.Node(e.ctx, e.root, strings.TrimPrefix(strings.TrimSpace(string(version)), "v"))
		if err != nil {
			return process.Result{}, err
		}
	}
	e.Calls++
	dir, err := os.MkdirTemp("", "concave-mdx-")
	if err != nil {
		return process.Result{}, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "adapter.mjs")
	if err = os.WriteFile(script, adapter, 0600); err != nil {
		return process.Result{}, err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second+time.Duration(sources)*time.Second/64)
	defer cancel()
	result, err := process.Run(ctx, e.root, []string{e.node, "--max-old-space-size=256", script}, request, repository.MaxOutput)
	if err != nil {
		return result, fmt.Errorf("MDX parser unavailable: %w: %.1000s", err, result.Stderr)
	}
	return result, nil
}

type Example struct {
	Line         int    `json:"line"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	Source       string `json:"source,omitempty"`
	SourceSHA256 string `json:"source_sha256,omitempty"`
	CodeSHA256   string `json:"code_sha256,omitempty"`
	CodeLines    int    `json:"code_lines,omitempty"`
}
type Proposal struct {
	Format  int       `json:"format"`
	Content string    `json:"content"`
	Records []Example `json:"records"`
}

func (e *Engine) Recover(path, original, upstream, current string, inputs map[string]string) (Proposal, error) {
	for _, source := range []Source{{original, upstream}, {path, current}} {
		facts, err := e.Parse(source.Path, source.Source)
		if err != nil {
			return Proposal{}, err
		}
		if facts.Error != "" {
			return Proposal{}, fmt.Errorf("invalid recovery document %s: %s", source.Path, facts.Error)
		}
	}
	request, err := json.Marshal(map[string]any{"format": 1, "root": e.root, "files": []Source{{path, current}}, "recovery": map[string]any{"upstream": upstream, "source_path": original, "inputs": inputs}})
	if err != nil {
		return Proposal{}, err
	}
	result, err := e.execute(request, 1)
	if err != nil {
		return Proposal{}, err
	}
	var proposal Proposal
	if err = artifact.Decode(result.Stdout, &proposal); err != nil {
		return proposal, err
	}
	if proposal.Format != 1 || len(proposal.Content) > repository.MaxBytes || len(proposal.Records) > 4096 {
		return proposal, fmt.Errorf("invalid MDX recovery response")
	}
	return proposal, nil
}
