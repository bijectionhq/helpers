package naming

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/syntax"
)

func Read(root *os.Root, path string) ([]byte, error) {
	if !Local(path) {
		return nil, fmt.Errorf("naming input path is not local: %q", path)
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("naming input must be a bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, fmt.Errorf("naming input exceeds 4 MiB")
	}
	return body, nil
}
func Run(ctx context.Context, input Input, p Policy) (Report, error) {
	report := Report{Format: 1, SourceID: input.SourceID, Target: input.Target, Rules: []string{}, PolicySHA256: input.PolicySHA256, EngineSHA256: input.EngineSHA256, State: "unavailable", Coverage: []Coverage{}, Findings: []Finding{}}
	if input.Format != 1 || !filepath.IsAbs(input.Root) || !Local(input.Target) || len(input.Files) == 0 || len(input.Files) > 4096 || len(input.SourceID) != 64 || len(input.PolicySHA256) != 64 {
		return report, fmt.Errorf("invalid bounded naming input")
	}
	rules, err := p.Select(input.Rules)
	if err != nil {
		return report, err
	}
	for _, r := range rules {
		report.Rules = append(report.Rules, r.ID)
	}
	root, err := os.OpenRoot(input.Root)
	if err != nil {
		return report, err
	}
	defer root.Close()
	vocabulary, err := Read(root, p.Vocabulary)
	if err != nil {
		return report, err
	}
	if input.Files[p.Vocabulary] != Digest(vocabulary) {
		return report, fmt.Errorf("vocabulary differs from captured input")
	}
	report.VocabularySHA256 = Digest(vocabulary)
	v, err := ReadVocabulary(string(vocabulary))
	if err != nil {
		return report, err
	}
	for term := range v.Terms {
		v.Canonical["defined:"+term] = term
	}
	for term := range proseWords {
		v.Canonical["defined:"+term] = term
	}
	for old, replacement := range v.Symbols {
		v.Canonical["symbol:"+old] = replacement
	}
	paths := []string{}
	for path := range input.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	facts := []Fact{}
	total := 0
	engine := syntax.New(ctx, "")
	if input.Engine != "" {
		engine = syntax.NewPinned(ctx, input.Engine)
	}
	selected := map[string]bool{}
	for _, path := range paths {
		if path == "concave.json" || path == p.Vocabulary || !strings.HasPrefix(path, input.Target+"/") {
			continue
		}
		active := false
		for _, r := range rules {
			for _, id := range r.Scopes {
				for _, scope := range p.Scopes {
					if scope.ID == id {
						for _, selector := range scope.Paths {
							active = active || Matches(path, selector)
						}
					}
				}
			}
		}
		if !active {
			continue
		}
		if syntax.Language(path) == "" && !strings.HasSuffix(path, ".md") {
			switch filepath.Ext(path) {
			case ".json", ".sum", ".mod", ".toml", ".yaml", ".yml", ".txt":
				continue
			}
		}
		selected[path] = true
		body, err := Read(root, path)
		if err != nil {
			return report, err
		}
		if Digest(body) != input.Files[path] {
			return report, fmt.Errorf("source differs from captured input: %s", path)
		}
		total += len(body)
		if total > 128<<20 {
			return report, fmt.Errorf("naming selection exceeds 128 MiB")
		}
		if strings.HasSuffix(path, ".md") {
			facts = append(facts, DocumentFacts(path, string(body), v)...)
			continue
		}
		if input.Engine == "" || len(input.EngineSHA256) != 64 {
			return report, fmt.Errorf("naming syntax engine is not pinned")
		}
		parser, err := os.ReadFile(input.Engine)
		if err != nil || Digest(parser) != input.EngineSHA256 {
			return report, fmt.Errorf("naming syntax engine changed")
		}
		names, err := engine.Names(path, body)
		if err != nil {
			return report, err
		}
		for _, name := range names {
			facts = append(facts, Fact{Path: path, Name: name.Name, Symbol: name.Symbol, Parent: name.Parent, Language: name.Language, Surface: name.Surface, Role: name.Role, Value: name.Value, Line: name.Range.Start.Line + 1, Column: name.Range.Start.Column + 1})
		}
	}
	if len(selected) == 0 {
		return report, fmt.Errorf("naming selection contains no applicable source files")
	}
	for path := range selected {
		facts = append(facts, Fact{Path: path, Role: "file"})
	}
	report.Findings, report.Coverage = Evaluate(p, rules, facts, v.Canonical)
	for i := range report.Coverage {
		count := 0
		for path := range selected {
			for _, id := range rules[i].Scopes {
				for _, s := range p.Scopes {
					if s.ID == id {
						for _, selector := range s.Paths {
							if Matches(path, selector) {
								count++
								goto counted
							}
						}
					}
				}
			}
		counted:
		}
		report.Coverage[i].Files = count
	}
	if len(report.Findings) > 16384 {
		return report, fmt.Errorf("naming findings exceed 16384")
	}
	report.State = "passed"
	for _, f := range report.Findings {
		if f.Severity == "error" {
			report.State = "failed"
		}
	}
	return report, nil
}
