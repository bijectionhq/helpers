package syntax

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"concave/internal/repository"
)

// CommentInput names the logical source; only private copies reach ast-grep.
type CommentInput struct {
	Path   string
	Source []byte
}

// Comments are the parser's facts about one source: each real comment and,
// for JSX, each expression container holding one. Syntax errors make the
// facts incomplete; callers must not infer annotations from them.
type Comments struct {
	Comments []Offset
	JSX      []Offset
	Errors   []Offset
}

// CommentLanguage is the grammar whose comment nodes carry annotations for
// path, or "" when Concave's lexical scanner owns that format.
func CommentLanguage(path string) string {
	switch language := Language(path); language {
	case "TypeScript", "Tsx", "JavaScript":
		return language
	}
	return ""
}

// CommentBatch parses each source once per invocation, at most maxBatchFiles
// per engine run, and returns facts in input order. Results share the rewrite
// cache's bounds.
func (e *Engine) CommentBatch(inputs []CommentInput) ([]Comments, error) {
	if e.ctx.Err() != nil {
		return nil, &Unavailable{e.ctx.Err()}
	}
	if len(inputs) > 4096 {
		return nil, fmt.Errorf("comment request count exceeds 4096")
	}
	if e.comments == nil {
		e.comments = map[string]Comments{}
	}
	keys := make([]string, len(inputs))
	pending := []int{}
	seen := map[string]bool{}
	total := 0
	for i, in := range inputs {
		if CommentLanguage(in.Path) == "" {
			return nil, fmt.Errorf("no comment grammar for %s", in.Path)
		}
		if err := sourceAllowed(in.Source); err != nil {
			return nil, fmt.Errorf("%s: %w", in.Path, err)
		}
		total += len(in.Source)
		if total > repository.MaxOutput {
			return nil, fmt.Errorf("comment inputs exceed 128 MiB")
		}
		keys[i] = repository.Digest([]byte(CommentLanguage(in.Path) + "\x00" + repository.Digest(in.Source)))
		if _, ok := e.comments[keys[i]]; ok || seen[keys[i]] {
			continue
		}
		seen[keys[i]] = true
		pending = append(pending, i)
	}
	for len(pending) > 0 {
		count := min(maxBatchFiles, len(pending))
		chosen := make([]CommentInput, count)
		for i, index := range pending[:count] {
			chosen[i] = inputs[index]
		}
		results, err := e.commentBatch(chosen)
		if err != nil {
			return nil, err
		}
		for i, index := range pending[:count] {
			size := 64 * (len(results[i].Comments) + len(results[i].JSX) + len(results[i].Errors) + 1)
			if e.rewriteBytes+size > repository.MaxOutput || len(e.comments) >= 4096 {
				return nil, fmt.Errorf("invocation comment results exceed memory bound")
			}
			e.rewriteBytes += size
			e.comments[keys[index]] = results[i]
		}
		pending = pending[count:]
	}
	out := make([]Comments, len(inputs))
	for i, key := range keys {
		out[i] = e.comments[key]
	}
	return out, nil
}

func (e *Engine) commentBatch(inputs []CommentInput) ([]Comments, error) {
	if err := e.Check(); err != nil {
		return nil, err
	}
	languages := map[string]bool{}
	for _, in := range inputs {
		languages[CommentLanguage(in.Path)] = true
	}
	rules := []map[string]any{}
	for _, language := range []string{"TypeScript", "Tsx", "JavaScript"} {
		if !languages[language] {
			continue
		}
		rules = append(rules,
			NativeRule("comment-"+language, language, map[string]any{"kind": "comment"}),
			NativeRule("error-"+language, language, map[string]any{"kind": "ERROR"}))
		if language != "TypeScript" {
			rules = append(rules, NativeRule("jsx-"+language, language, map[string]any{"kind": "jsx_expression", "has": map[string]any{"kind": "comment", "stopBy": "neighbor"}}))
		}
	}
	config, err := encodeRules(rules)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "concave-comments-")
	if err != nil {
		return nil, &Unavailable{err}
	}
	defer os.RemoveAll(dir)
	args := []string{"scan", "--json=stream", "--threads", "2", "--inline-rules", config, "--config", os.DevNull}
	for _, ignore := range []string{"hidden", "dot", "exclude", "global", "parent", "vcs"} {
		args = append(args, "--no-ignore", ignore)
	}
	sources := map[string]scanSource{}
	for i, in := range inputs {
		language := CommentLanguage(in.Path)
		name := batchName(i, language)
		if err = os.WriteFile(filepath.Join(dir, name), in.Source, 0600); err != nil {
			return nil, &Unavailable{err}
		}
		allowed := map[string]string{"comment-" + language: language, "error-" + language: language}
		if language != "TypeScript" {
			allowed["jsx-"+language] = language
		}
		sources[name] = scanSource{in.Source, allowed}
		args = append(args, name)
	}
	output, err := e.runAt(dir, nil, args...)
	if err != nil {
		return nil, err
	}
	matches, err := decodeMatches(output, sources)
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	for i, in := range inputs {
		index[batchName(i, CommentLanguage(in.Path))] = i
	}
	results := make([]Comments, len(inputs))
	for _, m := range matches {
		r := &results[index[m.File]]
		switch {
		case strings.HasPrefix(m.Rule, "comment-"):
			r.Comments = append(r.Comments, m.Range.Bytes)
		case strings.HasPrefix(m.Rule, "jsx-"):
			r.JSX = append(r.JSX, m.Range.Bytes)
		default:
			r.Errors = append(r.Errors, m.Range.Bytes)
		}
	}
	return results, nil
}
