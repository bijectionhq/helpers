package syntax

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"concave/internal/repository"
)

// RewriteInput names the logical source; only private copies reach ast-grep.
type RewriteInput struct {
	Path   string
	Source []byte
	Recipe Recipe
}
type RewriteResult struct {
	Source  []byte
	Matches []Match
	Err     error
}

const maxBatchFiles = 64

// RewriteBatch shares bounded results for this invocation only. Reconstruction
// and classification consume the same result, never two independent scans.
func (e *Engine) RewriteBatch(inputs []RewriteInput) ([]RewriteResult, error) {
	if e.ctx.Err() != nil {
		return nil, &Unavailable{e.ctx.Err()}
	}
	if len(inputs) > 4096 {
		return nil, fmt.Errorf("structural request count exceeds 4096")
	}
	if e.rewrites == nil {
		e.rewrites = map[string]RewriteResult{}
	}
	keys := make([]string, len(inputs))
	pending := []int{}
	seen := map[string]bool{}
	total := 0
	for i, in := range inputs {
		total += len(in.Source)
		if total > repository.MaxOutput {
			return nil, fmt.Errorf("structural inputs exceed 128 MiB")
		}
		recipe, err := json.Marshal(in.Recipe)
		if err != nil {
			return nil, err
		}
		if len(recipe) > 60<<10 {
			return nil, fmt.Errorf("structural recipe exceeds 60 KiB")
		}
		key := repository.Digest([]byte(in.Path + "\x00" + string(recipe) + "\x00" + repository.Digest(in.Source)))
		keys[i] = key
		if _, ok := e.rewrites[key]; ok || seen[key] {
			continue
		}
		seen[key] = true
		err = sourceAllowed(in.Source)
		if err == nil && (Language(in.Path) == "" || Language(in.Path) != in.Recipe.Language) {
			err = fmt.Errorf("structural language does not match %s", in.Path)
		}
		if err != nil {
			if err = e.remember(key, RewriteResult{Err: err}); err != nil {
				return nil, err
			}
			continue
		}
		pending = append(pending, i)
	}
	for len(pending) > 0 {
		count := min(maxBatchFiles, len(pending))
		// Config and input bounds apply to every batch, including multi-rule batches.
		for count > 0 {
			chosen := make([]RewriteInput, count)
			for i, index := range pending[:count] {
				chosen[i] = inputs[index]
			}
			rules, _, err := batchRules(chosen)
			if err != nil {
				return nil, err
			}
			if _, err = encodeRules(rules); err == nil {
				break
			}
			count--
		}
		if count == 0 {
			return nil, fmt.Errorf("structural rule exceeds batch configuration bound")
		}
		chosen := make([]RewriteInput, count)
		for i, index := range pending[:count] {
			chosen[i] = inputs[index]
		}
		results, err := e.rewriteBatch(chosen)
		if err != nil {
			return nil, err
		}
		for i, index := range pending[:count] {
			if err = e.remember(keys[index], results[i]); err != nil {
				return nil, err
			}
		}
		pending = pending[count:]
	}
	results := make([]RewriteResult, len(inputs))
	for i, key := range keys {
		results[i] = e.rewrites[key]
	}
	return results, nil
}
func (e *Engine) remember(key string, result RewriteResult) error {
	matches, err := json.Marshal(result.Matches)
	if err != nil {
		return err
	}
	size := len(result.Source) + len(matches) + len(key) + 128
	if result.Err != nil {
		size += len(result.Err.Error())
	}
	if e.rewriteBytes+size > repository.MaxOutput || len(e.rewrites) >= 4096 {
		return fmt.Errorf("invocation structural results exceed memory bound")
	}
	e.rewriteBytes += size
	e.rewrites[key] = result
	return nil
}
func batchName(index int, language string) string {
	ext := map[string]string{"TypeScript": "ts", "Tsx": "tsx", "JavaScript": "js", "Rust": "rs", "Go": "go"}[language]
	return fmt.Sprintf("source-%03d.%s", index, ext)
}
func batchRules(inputs []RewriteInput) ([]map[string]any, map[string]scanSource, error) {
	rules := []map[string]any{}
	sources := map[string]scanSource{}
	for i, in := range inputs {
		name := batchName(i, in.Recipe.Language)
		id := fmt.Sprintf("transform-%d", i)
		parseID := fmt.Sprintf("parse-error-%d", i)
		rule, err := recipeRule(id, in.Recipe)
		if err != nil {
			return nil, nil, err
		}
		rule["files"] = []string{name}
		parse := NativeRule(parseID, in.Recipe.Language, map[string]any{"kind": "ERROR"})
		parse["files"] = []string{name}
		rules = append(rules, rule, parse)
		sources[name] = scanSource{in.Source, map[string]string{id: in.Recipe.Language, parseID: in.Recipe.Language}}
	}
	return rules, sources, nil
}
func (e *Engine) rewriteBatch(inputs []RewriteInput) ([]RewriteResult, error) {
	if err := e.Check(); err != nil {
		return nil, err
	}
	rules, sources, err := batchRules(inputs)
	if err != nil {
		return nil, err
	}
	config, err := encodeRules(rules)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "concave-syntax-")
	if err != nil {
		return nil, &Unavailable{err}
	}
	defer os.RemoveAll(dir)
	args := []string{"scan", "--json=stream", "--threads", "2", "--inline-rules", config, "--config", os.DevNull}
	for _, ignore := range []string{"hidden", "dot", "exclude", "global", "parent", "vcs"} {
		args = append(args, "--no-ignore", ignore)
	}
	for i, in := range inputs {
		name := batchName(i, in.Recipe.Language)
		if err = os.WriteFile(filepath.Join(dir, name), in.Source, 0600); err != nil {
			return nil, &Unavailable{err}
		}
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
	byFile := map[string][]Match{}
	for _, m := range matches {
		byFile[m.File] = append(byFile[m.File], m)
	}
	results := make([]RewriteResult, len(inputs))
	for i, in := range inputs {
		ms := byFile[batchName(i, in.Recipe.Language)]
		for j := range ms {
			ms[j].File = in.Path
			if strings.HasPrefix(ms[j].Rule, "parse-error-") {
				ms[j].Rule = "parse-error"
			} else {
				ms[j].Rule = "transform"
			}
		}
		results[i].Source, results[i].Matches, results[i].Err = applyMatches(in.Source, ms)
	}
	return results, nil
}
