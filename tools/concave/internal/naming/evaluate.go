package naming

import (
	"sort"
	"strings"
	"unicode"
)

func words(name string) []string {
	var result []string
	runes := []rune(strings.TrimPrefix(name, "_"))
	start := 0
	flush := func(end int) {
		if end > start {
			result = append(result, strings.ToLower(string(runes[start:end])))
		}
		start = end
	}
	for i, c := range runes {
		if c == '_' {
			flush(i)
			start = i + 1
			continue
		}
		if i > start && unicode.IsUpper(c) && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1])) {
			flush(i)
		}
	}
	flush(len(runes))
	return result
}
func casing(name, style, language string) string {
	parts := words(name)
	if len(parts) == 0 {
		return name
	}
	initialisms := map[string]string{"id": "ID", "url": "URL", "http": "HTTP", "https": "HTTPS", "json": "JSON", "sql": "SQL", "api": "API", "sdk": "SDK", "utc": "UTC", "utf": "UTF", "sha": "SHA", "sha256": "SHA256", "sha512": "SHA512", "utf8": "UTF8", "io": "IO", "db": "DB", "csv": "CSV"}
	if style == "snake_case" {
		return strings.Join(parts, "_")
	}
	for i, part := range parts {
		if language == "Go" {
			if v, ok := initialisms[part]; ok && (i > 0 || style == "pascal_case") {
				parts[i] = v
				continue
			}
		}
		if i > 0 || style == "pascal_case" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "")
}
func expected(r Rule, f Fact, surface string, canonical map[string]string) (string, bool) {
	switch r.Predicate {
	case "defined_term":
		if f.Value != "declared" {
			return "", false
		}
		_, ok := canonical["defined:"+strings.ToLower(f.Name)]
		if ok {
			return f.Name, true
		}
		return "a defined canonical term", true
	case "canonical_symbol":
		value, ok := canonical["symbol:"+strings.ToLower(f.Name)]
		return value, ok
	case "canonical_term":
		if f.Value == "builder" {
			value, ok := canonical["symbol:"+strings.ToLower(f.Name)]
			return value, ok
		}
		value, ok := canonical[strings.ToLower(f.Name)]
		return value, ok
	case "is_prefix":
		if f.Value != "boolean" {
			return "", false
		}
		style := "camel_case"
		if f.Language == "Rust" || surface == "stored_record" {
			style = "snake_case"
		}
		name := casing(f.Name, style, f.Language)
		if strings.HasPrefix(name, "is_") || strings.HasPrefix(name, "is") && len(name) > 2 && unicode.IsUpper(rune(name[2])) || strings.HasPrefix(name, "Is") {
			return name, true
		}
		prefix := "is_"
		if style != "snake_case" {
			prefix = "is"
			if f.Language == "Go" && unicode.IsUpper(rune(f.Name[0])) {
				prefix = "Is"
			}
			name = casing(f.Name, "pascal_case", f.Language)
		}
		return prefix + name, true
	case "at_suffix":
		if f.Value != "instant" {
			return "", false
		}
		style := "camel_case"
		if f.Language == "Rust" || surface == "stored_record" {
			style = "snake_case"
		}
		name := casing(f.Name, style, f.Language)
		suffix := "At"
		if style == "snake_case" {
			suffix = "_at"
		}
		if strings.HasSuffix(name, suffix) {
			return name, true
		}
		return name + suffix, true
	case "language_case":
		style := "camel_case"
		if f.Role == "type" {
			style = "pascal_case"
		} else if f.Language == "Rust" {
			style = "snake_case"
		} else if f.Language == "Go" && unicode.IsUpper(rune(f.Name[0])) {
			style = "pascal_case"
		}
		if f.Role == "constant" {
			return f.Name, true
		}
		return casing(f.Name, style, f.Language), true
	default:
		return casing(f.Name, r.Predicate, f.Language), true
	}
}
func severity(f Fact) string {
	if f.Value == "prose" {
		return "note"
	}
	return "error"
}

func Evaluate(p Policy, rules []Rule, facts []Fact, canonical map[string]string) ([]Finding, []Coverage) {
	findings := []Finding{}
	coverage := []Coverage{}
	for _, r := range rules {
		c := Coverage{Rule: r.ID, State: "not_applicable"}
		files := map[string]bool{}
		used := map[int]bool{}
		for _, f := range facts {
			surface, ok := p.Applicable(r, f)
			if !ok {
				continue
			}
			want, applicable := expected(r, f, surface, canonical)
			if !applicable {
				continue
			}
			c.Facts++
			files[f.Path] = true
			exempt := false
			for i, e := range p.External {
				if e.Rule == r.ID && e.Path == f.Path && e.Symbol == f.Symbol {
					exempt = true
					used[i] = true
				}
			}
			if exempt || f.Name == want {
				continue
			}
			findings = append(findings, Finding{ID: Digest([]byte(r.ID + "\x00" + f.Path + "\x00" + f.Symbol)), Rule: r.ID, Severity: severity(f), Path: f.Path, Line: f.Line, Column: f.Column, Symbol: f.Symbol, Surface: surface, Actual: f.Name, Expected: want, Message: "Name differs from its declared naming authority.", Authority: p.Vocabulary, Action: "Update the owning declaration and its affected callers together."})
		}
		for i, e := range p.External {
			selectedPath := false
			for _, f := range facts {
				selectedPath = selectedPath || f.Path == e.Path
			}
			if e.Rule == r.ID && selectedPath && !used[i] {
				findings = append(findings, Finding{ID: Digest([]byte(r.ID + e.Path + e.Symbol)), Rule: r.ID, Severity: "error", Path: e.Path, Line: 1, Column: 1, Symbol: e.Symbol, Surface: "external", Message: "External naming binding no longer resolves.", Authority: e.Authority, Action: "Remove or correct the stale binding."})
			}
		}
		c.Files = len(files)
		if c.Facts > 0 {
			c.State = "passed"
		}
		for _, f := range findings {
			if f.Rule == r.ID && f.Severity == "error" {
				c.State = "failed"
			}
		}
		coverage = append(coverage, c)
	}
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Rule < b.Rule
	})
	return findings, coverage
}
