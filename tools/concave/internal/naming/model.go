// Package naming checks owned declarations against one captured naming policy.
// It has no writer, compiler, upstream-comparison or runtime authority.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type Policy struct {
	Vocabulary string     `json:"vocabulary"`
	Scopes     []Scope    `json:"scopes"`
	Rules      []Rule     `json:"rules"`
	External   []External `json:"external,omitempty"`
}
type Scope struct {
	ID      string   `json:"id"`
	Paths   []string `json:"paths"`
	Symbols []string `json:"symbols,omitempty"`
	Surface string   `json:"surface"`
}
type Rule struct {
	ID        string   `json:"id"`
	Scopes    []string `json:"scopes"`
	Roles     []string `json:"roles"`
	Predicate string   `json:"predicate"`
}
type External struct {
	Rule      string `json:"rule"`
	Path      string `json:"path"`
	Symbol    string `json:"symbol"`
	Reason    string `json:"reason"`
	Authority string `json:"authority"`
}
type Input struct {
	Format       int               `json:"format"`
	Root         string            `json:"root"`
	SourceID     string            `json:"source_id"`
	Target       string            `json:"target"`
	Files        map[string]string `json:"files"`
	Rules        []string          `json:"rules"`
	PolicySHA256 string            `json:"policy_sha256"`
	Engine       string            `json:"engine"`
	EngineSHA256 string            `json:"engine_sha256"`
}
type Fact struct {
	Path     string
	Symbol   string
	Parent   string
	Name     string
	Language string
	Surface  string
	Role     string
	Value    string
	Line     int
	Column   int
}
type Finding struct {
	ID        string `json:"id"`
	Rule      string `json:"rule"`
	Severity  string `json:"severity"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	Symbol    string `json:"symbol"`
	Surface   string `json:"surface"`
	Actual    string `json:"actual"`
	Expected  string `json:"expected"`
	Message   string `json:"message"`
	Authority string `json:"authority"`
	Action    string `json:"action"`
}
type Coverage struct {
	Rule  string `json:"rule"`
	State string `json:"state"`
	Files int    `json:"files"`
	Facts int    `json:"facts"`
}
type Report struct {
	Format           int        `json:"format"`
	SourceID         string     `json:"source_id"`
	Target           string     `json:"target"`
	Rules            []string   `json:"rules"`
	PolicySHA256     string     `json:"policy_sha256"`
	VocabularySHA256 string     `json:"vocabulary_sha256"`
	EngineSHA256     string     `json:"engine_sha256"`
	State            string     `json:"state"`
	Coverage         []Coverage `json:"coverage"`
	Findings         []Finding  `json:"findings"`
}

func Digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func Local(path string) bool {
	return filepath.IsLocal(path) && filepath.ToSlash(filepath.Clean(path)) == path && !strings.Contains(path, "\\")
}
func Matches(path, selector string) bool {
	return path == selector || strings.HasSuffix(selector, "/") && strings.HasPrefix(path, selector)
}
func (p Policy) Validate() error {
	if !Local(p.Vocabulary) || len(p.Rules) == 0 || len(p.Rules) > 128 || len(p.Scopes) > 128 || len(p.External) > 1024 {
		return fmt.Errorf("invalid bounded naming policy")
	}
	scopes := map[string]bool{}
	for _, s := range p.Scopes {
		if s.ID == "" || scopes[s.ID] || len(s.Paths) == 0 || len(s.Paths) > 128 || len(s.Symbols) > 128 {
			return fmt.Errorf("invalid or duplicate naming scope %q", s.ID)
		}
		switch s.Surface {
		case "implementation", "sdk_metadata", "stored_record", "wire", "public_declaration", "documentation":
		default:
			return fmt.Errorf("unknown naming surface %q", s.Surface)
		}
		for _, path := range s.Paths {
			if !Local(strings.TrimSuffix(path, "/")) {
				return fmt.Errorf("invalid naming path %q", path)
			}
		}
		scopes[s.ID] = true
	}
	rules := map[string]bool{}
	for _, r := range p.Rules {
		if !strings.HasPrefix(r.ID, "naming/") || rules[r.ID] || len(r.Scopes) == 0 || len(r.Roles) == 0 || len(r.Scopes) > 128 || len(r.Roles) > 6 {
			return fmt.Errorf("invalid or duplicate naming rule %q", r.ID)
		}
		for _, s := range r.Scopes {
			if !scopes[s] {
				return fmt.Errorf("unknown naming scope %q", s)
			}
		}
		for _, role := range r.Roles {
			switch role {
			case "type", "field", "function", "variable", "constant", "term":
			default:
				return fmt.Errorf("unknown naming role %q", role)
			}
		}
		switch r.Predicate {
		case "language_case", "camel_case", "snake_case", "pascal_case", "is_prefix", "at_suffix", "canonical_term", "defined_term", "canonical_symbol":
		default:
			return fmt.Errorf("unknown naming predicate %q", r.Predicate)
		}
		rules[r.ID] = true
	}
	seen := map[string]bool{}
	for _, e := range p.External {
		key := e.Rule + "\x00" + e.Path + "\x00" + e.Symbol
		if !rules[e.Rule] || !Local(e.Path) || e.Symbol == "" || strings.TrimSpace(e.Reason) == "" || strings.TrimSpace(e.Authority) == "" || seen[key] {
			return fmt.Errorf("invalid or duplicate external naming binding")
		}
		seen[key] = true
	}
	return nil
}
func (p Policy) Select(requested []string) ([]Rule, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for _, id := range requested {
		if found[id] {
			return nil, fmt.Errorf("duplicate naming selection %q", id)
		}
		found[id] = true
	}
	all := len(requested) == 0 || len(requested) == 1 && requested[0] == "naming"
	var selected []Rule
	for _, r := range p.Rules {
		if all || found[r.ID] {
			selected = append(selected, r)
			delete(found, r.ID)
		}
	}
	if !all && len(found) > 0 || len(selected) == 0 {
		return nil, fmt.Errorf("unknown or empty naming rule selection")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	return selected, nil
}
func (p Policy) Applicable(r Rule, f Fact) (string, bool) {
	role := false
	for _, v := range r.Roles {
		role = role || v == f.Role
	}
	if !role {
		return "", false
	}
	for _, id := range r.Scopes {
		for _, s := range p.Scopes {
			if s.ID != id {
				continue
			}
			path := false
			for _, v := range s.Paths {
				path = path || Matches(f.Path, v)
			}
			if !path {
				continue
			}
			symbol := len(s.Symbols) == 0
			for _, v := range s.Symbols {
				symbol = symbol || f.Parent == v || f.Name == v || strings.HasPrefix(f.Symbol, v+"/")
			}
			if symbol && (f.Surface == "" && s.Surface != "wire" || f.Surface == s.Surface) {
				return s.Surface, true
			}
		}
	}
	return "", false
}
