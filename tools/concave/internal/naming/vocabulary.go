package naming

import (
	"fmt"
	"regexp"
	"strings"
)

type Vocabulary struct {
	Canonical map[string]string
	Terms     map[string]bool
	Symbols   map[string]string
}

func cell(value string) string { return strings.Trim(strings.TrimSpace(value), "`") }
func ReadVocabulary(body string) (Vocabulary, error) {
	v := Vocabulary{Canonical: map[string]string{}, Terms: map[string]bool{}, Symbols: map[string]string{}}
	active := false
	header := []string{}
	fence := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			active = false
			continue
		}
		raw := strings.Split(strings.Trim(line, "|"), "|")
		cells := []string{}
		for _, c := range raw {
			cells = append(cells, cell(c))
		}
		if len(cells) < 3 {
			if len(cells) == 2 && len(header) == 2 && header[0] == "Retired symbol" && header[1] == "Canonical symbol" && !strings.HasPrefix(cells[0], "---") {
				v.Symbols[strings.ToLower(cells[0])] = cells[1]
				continue
			}
			if len(cells) == 2 && cells[0] == "Retired symbol" {
				header = cells
			}
			continue
		}
		if strings.HasPrefix(cells[0], "---") {
			active = len(header) >= 3 && (header[0] == "Word" || header[0] == "Bijection") && header[1] == "Meaning" && header[2] == "Not"
			continue
		}
		if !active {
			header = cells
			continue
		}
		for _, term := range strings.Split(cells[0], ",") {
			term = strings.ToLower(cell(term))
			if term == "" {
				continue
			}
			if v.Terms[term] {
				return v, fmt.Errorf("duplicate vocabulary term %q", term)
			}
			v.Terms[term] = true
			for _, old := range strings.Split(cells[2], ",") {
				old = strings.ToLower(cell(old))
				if old != "" {
					v.Canonical[old] = term
				}
			}
		}
	}
	for word := range v.Terms {
		delete(v.Canonical, word)
	}
	for word := range proseWords {
		delete(v.Canonical, word)
	}
	if len(v.Terms) == 0 {
		return v, fmt.Errorf("vocabulary has no canonical declaration tables")
	}
	return v, nil
}

var bold = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
var heading = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
var proseAttribute = regexp.MustCompile(`(?i)\b(predecessor|restate|dbos|postgresql|xtdb|convex today|convex's)\b`)

func DocumentFacts(path, text string, v Vocabulary) []Fact {
	facts := []Fact{}
	mermaid := false
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```mermaid") {
			mermaid = true
			continue
		}
		if mermaid {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				mermaid = false
			}
			continue
		}
		declared := []string{}
		if match := heading.FindStringSubmatch(line); len(match) > 1 {
			key := strings.ToLower(strings.TrimSpace(match[1]))
			if _, ok := v.Canonical[key]; ok {
				declared = append(declared, key)
			}
		}
		for _, match := range bold.FindAllStringSubmatch(line, -1) {
			raw := strings.TrimSpace(match[1])
			if len(raw) < 2 || len(raw) > 40 || strings.HasSuffix(raw, ".") && strings.Contains(raw, " ") {
				continue
			}
			key := strings.ToLower(strings.TrimRight(raw, "."))
			key = regexp.MustCompile(`[^a-z' -]`).ReplaceAllString(key, "")
			for _, prefix := range []string{"a ", "an ", "the "} {
				key = strings.TrimPrefix(key, prefix)
			}
			switch key {
			case "held", "holding", "holds":
				key = "hold"
			}
			if strings.HasSuffix(key, "s") && v.Terms[strings.TrimSuffix(key, "s")] {
				key = strings.TrimSuffix(key, "s")
			}
			declared = append(declared, key)
		}
		for _, name := range declared {
			facts = append(facts, Fact{Path: path, Name: name, Symbol: name, Role: "term", Value: "declared", Language: "Markdown", Line: i + 1, Column: 1})
		}
		if !proseAttribute.MatchString(line) {
			for old := range v.Canonical {
				if strings.HasPrefix(old, "defined:") || strings.HasPrefix(old, "symbol:") {
					continue
				}
				pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(old) + `\b`)
				if !pattern.MatchString(line) {
					continue
				}
				isDeclared := false
				for _, name := range declared {
					isDeclared = isDeclared || name == old
				}
				if !isDeclared {
					facts = append(facts, Fact{Path: path, Name: old, Symbol: old, Role: "term", Value: "prose", Language: "Markdown", Line: i + 1, Column: 1})
				}
			}
			for old, replacement := range v.Symbols {
				_ = replacement
				pattern := regexp.MustCompile(`(?i)\b(` + regexp.QuoteMeta(old) + `)\s*\(`)
				for _, match := range pattern.FindAllStringSubmatchIndex(line, -1) {
					name := line[match[2]:match[3]]
					if match[0] > 0 && line[match[0]-1] == '`' {
						continue
					}
					facts = append(facts, Fact{Path: path, Name: name, Symbol: name, Role: "term", Value: "builder", Language: "Markdown", Line: i + 1, Column: match[0] + 1})
				}
			}
		}
	}
	return facts
}
