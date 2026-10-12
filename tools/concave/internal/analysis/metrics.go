package analysis

import (
	"regexp"
	"sort"
	"strings"
)

type sourcePair struct{ a, b []byte }
type FileMetric struct {
	Path       string `json:"path"`
	Added      int    `json:"added_total"`
	AddedTest  int    `json:"added_test"`
	Removed    int    `json:"removed"`
	Divergence int    `json:"divergence"`
	TestFile   bool   `json:"test_file"`
}
type Metric struct {
	Method    string         `json:"method"`
	Total     int            `json:"total"`
	Files     []FileMetric   `json:"files"`
	PerCrate  map[string]int `json:"per_crate"`
	Anomalies []string       `json:"anomalies"`
}

// These lexical exclusions reproduce the historical metric only. They are
// never used to approve source changes, fences, or exception coverage.
func stripCode(s string) string {
	b := []byte(s)
	blank := func(a, z int) {
		if z > len(b) {
			z = len(b)
		}
		for i := a; i < z; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "//") {
			z := strings.IndexByte(s[i:], '\n')
			if z < 0 {
				z = len(s) - i
			}
			blank(i, i+z)
			i += z
			continue
		}
		if strings.HasPrefix(s[i:], "/*") {
			z := i + 2
			d := 1
			for z < len(s) && d > 0 {
				if strings.HasPrefix(s[z:], "/*") {
					d++
					z += 2
				} else if strings.HasPrefix(s[z:], "*/") {
					d--
					z += 2
				} else {
					z++
				}
			}
			blank(i, z)
			i = z
			continue
		}
		if s[i] == 'r' {
			j := i + 1
			for j < len(s) && s[j] == '#' {
				j++
			}
			if j < len(s) && s[j] == '"' {
				close := "\"" + strings.Repeat("#", j-i-1)
				end := strings.Index(s[j+1:], close)
				z := len(s)
				if end >= 0 {
					z = j + 1 + end + len(close)
				}
				blank(i, z)
				i = z
				continue
			}
		}
		if s[i] == '"' {
			z := i + 1
			for z < len(s) {
				if s[z] == '\\' {
					z += 2
					continue
				}
				if s[z] == '"' {
					z++
					break
				}
				z++
			}
			blank(i, z)
			i = z
			continue
		}
		if s[i] == '\'' {
			loc := charLiteral.FindStringIndex(s[i:])
			if loc != nil && loc[0] == 0 {
				blank(i, i+loc[1])
				i += loc[1]
				continue
			}
		}
		i++
	}
	return string(b)
}

var charLiteral = regexp.MustCompile(`^'(?:\\u\{[0-9a-fA-F]+\}|\\.|[^'\\])'`)
var attribute = regexp.MustCompile(`(?s)^#\[\s*([A-Za-z_][A-Za-z0-9_:]*)\s*(.*)\]\s*$`)
var cfgTest = regexp.MustCompile(`\btest\b|feature\s*=\s*"testing"`)
var visibility = regexp.MustCompile(`^pub(?:\s*\([^)]*\))?\s+`)
var itemHead = regexp.MustCompile(`^(mod|fn|impl|use|struct|enum|trait|type|const|static|union|let|unsafe|async|extern|macro|macro_rules!|default)\b`)

type attr struct {
	path, inner string
	start, end  int
}

func attributes(ls []string) []attr {
	out := []attr{}
	for i := 0; i < len(ls); {
		s := strings.TrimSpace(ls[i])
		if !strings.HasPrefix(s, "#[") {
			i++
			continue
		}
		depth := 0
		j := i
		for j < len(ls) {
			depth += strings.Count(ls[j], "[") - strings.Count(ls[j], "]")
			j++
			if depth <= 0 {
				break
			}
		}
		if m := attribute.FindStringSubmatch(strings.TrimSpace(strings.Join(ls[i:j], " "))); m != nil {
			out = append(out, attr{m[1], m[2], i, j})
		}
		i = j
	}
	return out
}
func itemEnd(ls []string, i int) int {
	for i < len(ls) && (strings.TrimSpace(ls[i]) == "" || strings.HasPrefix(strings.TrimSpace(ls[i]), "#[")) {
		i++
	}
	if i == len(ls) {
		return i
	}
	field := !itemHead.MatchString(visibility.ReplaceAllString(strings.TrimSpace(ls[i]), ""))
	depth := 0
	brace := false
	for ; i < len(ls); i++ {
		for _, ch := range ls[i] {
			switch ch {
			case '(', '[':
				depth++
			case ')', ']':
				depth--
			case '{':
				depth++
				brace = true
			case '}':
				if depth == 0 {
					return i
				}
				depth--
				if brace && depth <= 0 {
					return i + 1
				}
			case ';':
				if depth == 0 && !brace {
					return i + 1
				}
			case ',':
				if depth == 0 && !brace && field {
					return i + 1
				}
			}
		}
	}
	return len(ls)
}
func RustTestLines(s string, naive bool) map[int]bool {
	ls := strings.Split(stripCode(s), "\n")
	if len(ls) > 0 && ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	out := map[int]bool{}
	as := attributes(ls)
	mark := func(a, z int) {
		for i := a; i < z; i++ {
			out[i+1] = true
		}
	}
	for _, a := range as {
		if a.path == "cfg" && cfgTest.MatchString(a.inner) {
			mark(a.start, itemEnd(ls, a.end))
		}
	}
	if !naive {
		for _, a := range as {
			if out[a.start+1] {
				continue
			}
			if a.path == "cfg_attr" && cfgTest.MatchString(a.inner) {
				mark(a.start, a.end)
			} else if a.path == "test" || a.path == "tokio::test" || a.path == "convex_macro::test_runtime" || a.path == "convex_macro::prod_rt_test" {
				mark(a.start, itemEnd(ls, a.end))
			}
		}
	}
	return out
}
func Measure(r *Report, contents map[string]sourcePair, method string) *Metric {
	m := &Metric{Method: method, Files: []FileMetric{}, PerCrate: map[string]int{}, Anomalies: []string{}}
	for _, row := range r.Files {
		p := row.Path
		if !strings.HasPrefix(p, "crates/") && !strings.HasPrefix(p, "npm-packages/convex/") {
			continue
		}
		if row.Status == "missing" {
			m.Anomalies = append(m.Anomalies, p)
			continue
		}
		if row.Status != "modified" {
			continue
		}
		f := FileMetric{Path: p, Added: row.Added, Removed: row.Removed}
		for _, s := range []string{".test.ts", ".test.tsx", ".test.js", ".test.jsx"} {
			if strings.HasSuffix(p, s) {
				f.TestFile = true
			}
		}
		if strings.HasSuffix(p, ".rs") {
			marked := RustTestLines(string(contents[p].b), method == "non-test-2026-09-16")
			for _, h := range r.Hunks {
				if h.Path == p {
					for i := h.CurrentStart + 1; i <= h.CurrentEnd; i++ {
						if marked[i] {
							f.AddedTest++
						}
					}
				}
			}
		}
		if !f.TestFile {
			f.Divergence = f.Added - f.AddedTest + f.Removed
		}
		m.Total += f.Divergence
		parts := strings.Split(p, "/")
		key := parts[0]
		if len(parts) > 1 {
			key = strings.Join(parts[:2], "/")
		}
		m.PerCrate[key] += f.Divergence
		m.Files = append(m.Files, f)
	}
	sort.Slice(m.Files, func(i, j int) bool {
		if m.Files[i].Divergence != m.Files[j].Divergence {
			return m.Files[i].Divergence > m.Files[j].Divergence
		}
		return m.Files[i].Path < m.Files[j].Path
	})
	return m
}
