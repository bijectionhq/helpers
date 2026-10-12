package policy

import (
	"concave/internal/repository"
	"concave/internal/syntax"
	"fmt"
	"sort"
	"strings"
)

func (t Transform) Matches(path string) bool {
	for _, p := range t.Paths {
		if p == path {
			return true
		}
	}
	return false
}

func (t Transform) Pairs(path string, manifest Manifest) ([][2]string, error) {
	if !t.Matches(path) {
		return nil, fmt.Errorf("path is outside transformation scope")
	}
	if t.Module != "" {
		pairs := manifest.Rewrites[t.Module]
		if len(pairs) == 0 {
			return nil, fmt.Errorf("module is absent from transformation manifest")
		}
		return pairs, nil
	}
	pairs := make([][2]string, 0, len(t.Replacements))
	for _, p := range t.Replacements {
		if p.Paths != nil && !repository.Matches(path, p.Paths) {
			continue
		}
		pairs = append(pairs, [2]string{p.From, p.To})
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no literal replacements declared")
	}
	return pairs, nil
}

// Apply evaluates the declared recipe exactly, with a bound before allocating
// each replacement. It is shared by source reconstruction and hunk accounting.
func (t Transform) Apply(path string, manifest Manifest, source string, engines ...*syntax.Engine) (string, error) {
	if t.ASTGrep != nil {
		if !t.Matches(path) || len(engines) != 1 || engines[0] == nil {
			return "", fmt.Errorf("structural transformation requires its scoped syntax engine")
		}
		want, _, err := engines[0].Transform(path, []byte(source), *t.ASTGrep)
		return string(want), err
	}
	if t.Tokens != nil {
		if !t.Matches(path) {
			return "", fmt.Errorf("path is outside transformation scope")
		}
		return t.Tokens.apply(path, source)
	}
	pairs, err := t.Pairs(path, manifest)
	if err != nil {
		return "", err
	}
	want := source
	if len(want) > repository.MaxBytes {
		return "", fmt.Errorf("transformed source exceeds size bound")
	}
	for _, p := range pairs {
		if p[0] == "" {
			return "", fmt.Errorf("empty source literal")
		}
		n := strings.Count(want, p[0])
		if n == 0 {
			return "", fmt.Errorf("expected source text absent: %q", p[0])
		}
		if growth := len(p[1]) - len(p[0]); growth > 0 && n > (repository.MaxBytes-len(want))/growth {
			return "", fmt.Errorf("transformed source exceeds size bound")
		}
		want = strings.ReplaceAll(want, p[0], p[1])
	}
	if want == source {
		return "", fmt.Errorf("transformation makes no source change")
	}
	return want, nil
}

func nameByte(b byte) bool {
	return b == '_' || b == '$' || b == '-' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// urlSpans are the opaque `scheme://` runs of source.
func urlSpans(source string) []Span {
	spans := []Span{}
	for i := 0; ; {
		j := strings.Index(source[i:], "://")
		if j < 0 {
			return spans
		}
		start := i + j
		for start > 0 && ('a' <= source[start-1] && source[start-1] <= 'z' || 'A' <= source[start-1] && source[start-1] <= 'Z') {
			start--
		}
		end := i + j + 3
		for end < len(source) && !strings.ContainsRune(" \t\r\n\"'`<>()[]{}", rune(source[end])) {
			end++
		}
		if start < i+j {
			spans = append(spans, Span{start, end})
		}
		i = end
	}
}

func (k Tokens) apply(path, source string) (string, error) {
	if len(source) > repository.MaxBytes {
		return "", fmt.Errorf("transformed source exceeds size bound")
	}
	byFirst := map[byte][]int{}
	scoped := map[int]bool{}
	for i, r := range k.Rules {
		if r.Paths != nil {
			if !repository.Matches(path, r.Paths) {
				continue
			}
			scoped[i] = true
		}
		byFirst[r.From[0]] = append(byFirst[r.From[0]], i)
	}
	for _, list := range byFirst {
		sort.SliceStable(list, func(a, b int) bool { return len(k.Rules[list[a]].From) > len(k.Rules[list[b]].From) })
	}
	excluded := []Span{}
	for _, e := range k.Except {
		for i := 0; ; {
			j := strings.Index(source[i:], e)
			if j < 0 {
				break
			}
			excluded = append(excluded, Span{i + j, i + j + len(e)})
			i += j + 1
		}
	}
	urls := urlSpans(source)
	allowed := func(r Replacement, start, end int) bool {
		if nameByte(r.From[0]) && start > 0 && nameByte(source[start-1]) || nameByte(r.From[len(r.From)-1]) && end < len(source) && nameByte(source[end]) {
			return false
		}
		for _, u := range urls {
			if start < u.End && u.Start < end && !(start == u.Start && end == u.End && strings.Contains(r.From, "://")) {
				return false
			}
		}
		for _, e := range excluded {
			if start < e.End && e.Start < end && !(start <= e.Start && e.End <= end) {
				return false
			}
		}
		return true
	}
	counts := map[int]int{}
	var out strings.Builder
	for i := 0; i < len(source); {
		matched := false
		for _, index := range byFirst[source[i]] {
			r := k.Rules[index]
			end := i + len(r.From)
			if !strings.HasPrefix(source[i:], r.From) || !allowed(r, i, end) {
				continue
			}
			if out.Len()+len(r.To)+len(source)-end > repository.MaxBytes {
				return "", fmt.Errorf("transformed source exceeds size bound")
			}
			out.WriteString(r.To)
			counts[index]++
			i, matched = end, true
			break
		}
		if !matched {
			out.WriteByte(source[i])
			i++
		}
	}
	for index := range scoped {
		if counts[index] == 0 {
			return "", fmt.Errorf("expected token absent: %q", k.Rules[index].From)
		}
	}
	if out.String() == source {
		return "", fmt.Errorf("transformation makes no source change")
	}
	return out.String(), nil
}
