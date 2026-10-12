// Package policy defines the single versioned source-preservation contract.
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"concave/internal/artifact"
	"concave/internal/contracts"
	"concave/internal/naming"
	"concave/internal/process"
	"concave/internal/repository"
	"concave/internal/syntax"
)

const File = "concave.json"

type Family struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Paths     []string `json:"paths"`
	Contracts []string `json:"contracts,omitempty"`
}
type Surface struct {
	ID      string   `json:"id"`
	Purpose string   `json:"purpose"`
	Paths   []string `json:"paths"`
}
type Probe struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Needles []string `json:"needles"`
	Meaning string   `json:"meaning"`
}
type Case struct {
	Contracts    []string  `json:"contracts,omitempty"`
	Witnesses    []string  `json:"witnesses,omitempty"`
	Surfaces     []Surface `json:"surfaces"`
	Probes       []Probe   `json:"probes"`
	ReviewOrder  []string  `json:"review_order"`
	Verification []string  `json:"verification"`
}

// A transform is one exact recipe over an explicit list of inherited paths.
// Its replacements are authored here or borrowed from the facade manifest.
type Transform struct {
	ID           string          `json:"id"`
	Paths        []string        `json:"paths"`
	Module       string          `json:"module,omitempty"`
	Replacements []Replacement   `json:"replacements,omitempty"`
	Tokens       *Tokens         `json:"tokens,omitempty"`
	Explanation  Explanation     `json:"explanation"`
	ASTGrep      *syntax.Recipe  `json:"ast_grep,omitempty"`
	Tests        []TransformTest `json:"tests,omitempty"`
}
type TransformTest struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type Replacement struct {
	From string `json:"from"`
	To   string `json:"to"`
	// An omitted scope applies to every transform path. A declared scope must
	// be a nonempty subset, allowing one explained rule to name distinct owners.
	Paths []string `json:"paths,omitempty"`
}

// Tokens rename whole names. A rule's `from` matches only where neither
// neighbouring character continues a name ([A-Za-z0-9_$-]) on a side where it
// begins or ends with one; at each position the longest rule wins. Text inside
// a URL (`scheme://` to the next space, quote, bracket or parenthesis) is
// opaque unless the rule names that whole URL, and a match
// overlapping an `except` occurrence is skipped unless it contains it. A rule
// with `paths` must match in each; an unscoped rule applies where it matches.
type Tokens struct {
	Rules  []Replacement `json:"rules"`
	Except []string      `json:"except,omitempty"`
}

// Restoration authorizes exact reintroduction of pure deletions from a reviewed
// upstream commit. It never accepts arbitrary code merely because it is a test.
type Restoration struct {
	ID          string      `json:"id"`
	RemovedBy   string      `json:"removed_by"`
	Paths       []string    `json:"paths"`
	Explanation Explanation `json:"explanation"`
}

// Derived declares inherited files that a named generator produces from
// declared sources, such as lockfiles and generated API contracts. Their
// divergence follows from the separately enforced divergence of those sources;
// Check names the policy check that runs the generator's frozen or validating
// mode. Inspection never runs it, and a derived file is never behavioral proof.
type Derived struct {
	ID          string      `json:"id"`
	Paths       []string    `json:"paths"`
	Sources     []string    `json:"sources"`
	Generator   string      `json:"generator"`
	Check       string      `json:"check"`
	Explanation Explanation `json:"explanation"`
}

// RestoredHunk is derived from immutable Git history by analysis, not authored.
type RestoredHunk struct {
	Rule      string
	At        int
	Before    string
	Text      string
	RemovedBy string
	Source    Span
}

func (h RestoredHunk) Matches(candidate repository.Hunk) bool {
	return candidate.Start == h.At && candidate.End == h.At+len(repository.Lines(h.Before)) &&
		candidate.Before == h.Before && candidate.After == h.Text
}

// An exception binds one original line span and its replacement bytes, or a
// bounded binary replacement or retirement through File.
// Start and End are zero-based, end-exclusive. Upstream hashes bind anchors to
// an immutable blob; an upstream edit requires review, never fuzzy relocation.
type Exception struct {
	ID                string       `json:"id"`
	Path              string       `json:"path"`
	UpstreamSHA256    string       `json:"upstream_sha256"`
	Start             int          `json:"start"`
	End               int          `json:"end"`
	ReplacementSHA256 string       `json:"replacement_sha256"`
	Fence             bool         `json:"fence"`
	File              *FileBinding `json:"file,omitempty"`
	Reason            string       `json:"reason,omitempty"`
	Witnesses         []string     `json:"witnesses,omitempty"`
	RemoveWhen        string       `json:"remove_when,omitempty"`
	Preserves         []string     `json:"preserves,omitempty"`
}

// UnmarshalJSON requires pairs of exactly two strings, unlike Go's permissive
// fixed-array decoder which would discard extra elements.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	var raw struct {
		ContractNames []string              `json:"contract_names"`
		Rewrites      map[string][][]string `json:"rewrites"`
	}
	if err := artifact.Decode(data, &raw); err != nil {
		return err
	}
	m.ContractNames = raw.ContractNames
	m.Rewrites = map[string][][2]string{}
	for name, pairs := range raw.Rewrites {
		for _, pair := range pairs {
			if len(pair) != 2 {
				return fmt.Errorf("transformation %s requires pairs of exactly two strings", name)
			}
			m.Rewrites[name] = append(m.Rewrites[name], [2]string{pair[0], pair[1]})
		}
	}
	return nil
}

type Manifest struct {
	ContractNames []string               `json:"contract_names"`
	Rewrites      map[string][][2]string `json:"rewrites"`
}
type Config struct {
	Naming                 *naming.Policy             `json:"naming,omitempty"`
	Documents              *Documents                 `json:"documents,omitempty"`
	Environments           map[string]Environment     `json:"environments,omitempty"`
	Checks                 map[string]CheckDefinition `json:"checks,omitempty"`
	Format                 int                        `json:"format"`
	Contract               string                     `json:"contract,omitempty"`
	Upstream               string                     `json:"upstream"`
	Roots                  []string                   `json:"roots"`
	Families               []Family                   `json:"families"`
	PreserveExactly        []string                   `json:"preserve_exactly"`
	Cases                  map[string]Case            `json:"cases"`
	Boundaries             []string                   `json:"boundaries"`
	Writer                 []string                   `json:"writer"`
	TransformationManifest string                     `json:"transformation_manifest,omitempty"`
	Transforms             []Transform                `json:"transforms"`
	Restorations           []Restoration              `json:"restorations,omitempty"`
	Derived                []Derived                  `json:"derived,omitempty"`
	Exceptions             []Exception                `json:"-"`
	// Bindings holds each binding file's exact bytes by source path.
	Bindings       map[string][]byte `json:"-"`
	SHA256         string            `json:"-"`
	Raw            []byte            `json:"-"`
	Manifest       Manifest          `json:"-"`
	ManifestSHA256 string            `json:"-"`
}
type Finding struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Upstream  *Span  `json:"upstream,omitempty"`
	Exception string `json:"exception,omitempty"`
	Rule      string `json:"rule,omitempty"`
	Message   string `json:"message"`
	// Subject names the violated source beyond its path, upstream span and
	// binding: replacement bytes, an annotation line, a document's dependency
	// identity or a restoration rule. It never derives from wording or positions.
	Subject string `json:"subject,omitempty"`
}

// Identity is the finding's rule and the source obligation it represents: path,
// binding and subject. Message, kind and line coordinates only explain it, so a
// reworded diagnostic, a renamed kind or lines shifted on either side keep it.
func (f Finding) Identity() string {
	return identityKey(f.Rule, f.Path, nil, f.Exception, f.Subject)
}

// Location is where an obligation applies against one upstream pin, without its
// rule or subject: the coordinates every Concave version reports. Coverage
// compares two measurements of one source through it, so a stricter rule or a
// newly named subject is no loss.
func (f Finding) Location() string {
	return identityKey("", f.Path, f.Upstream, f.Exception, "")
}

func identityKey(rule, path string, span *Span, binding, subject string) string {
	upstream := ""
	if span != nil {
		upstream = fmt.Sprintf("%d:%d", span.Start, span.End)
	}
	return strings.Join([]string{rule, path, upstream, binding, subject}, "\x00")
}

// Subject digests the exact source a finding is about.
func Subject(parts ...string) string {
	return repository.Digest([]byte(strings.Join(parts, "\x00")))[:20]
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)

// Format is the policy file format this Concave reads.
const Format = 2

// Rename moves one field to its new name in the next format, or removes it
// when To is empty. In is the dotted path of the object holding it, "" for the
// policy itself, and "*" stands for every member of a map or list there, as in
// {In: "checks.*", From: "environment", To: "environments"}.
type Rename struct{ In, From, To string }

// NamingPolicy decodes only the selected captured policy; no repository or Git is opened.
func NamingPolicy(body []byte) (*naming.Policy, error) {
	c, err := decodePolicy(body)
	if err != nil {
		return nil, err
	}
	if c.Naming == nil {
		return nil, fmt.Errorf("repository declares no naming policy")
	}
	return c.Naming, c.Naming.Validate()
}

// migrations lists, per earlier format, the renames that produce the next one,
// so a baseline policy written before a rename decodes exactly, never
// leniently. A rename raises Format and adds its line here; the entry is
// removed once no compared baseline uses the earlier format.
var migrations = map[int][]Rename{}

// decodePolicy strictly decodes the current format, renaming earlier ones'
// fields first; any other difference is still refused by the strict decode.
func decodePolicy(data []byte) (*Config, error) {
	var header struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if header.Format != Format {
		var tree map[string]any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&tree); err != nil {
			return nil, fmt.Errorf("policy: %w", err)
		}
		for format := header.Format; format < Format; format++ {
			renames, ok := migrations[format]
			if !ok {
				break
			}
			for _, rename := range renames {
				if err := rename.apply(tree); err != nil {
					return nil, fmt.Errorf("policy format %d rename %s.%s: %w", format, rename.In, rename.From, err)
				}
			}
			header.Format = format + 1
		}
		if header.Format != Format {
			return nil, fmt.Errorf("policy format %d is not readable by this Concave, which reads format %d; a rename raises the format and lists itself in the policy migrations", header.Format, Format)
		}
		tree["format"] = Format
		var err error
		if data, err = json.Marshal(tree); err != nil {
			return nil, err
		}
	}
	c := new(Config)
	if err := artifact.Decode(data, c); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	return c, nil
}

func (rename Rename) apply(tree map[string]any) error {
	holders := []any{tree}
	if rename.In != "" {
		for _, step := range strings.Split(rename.In, ".") {
			next := []any{}
			for _, holder := range holders {
				switch value := holder.(type) {
				case map[string]any:
					if step == "*" {
						for _, member := range value {
							next = append(next, member)
						}
					} else if member, ok := value[step]; ok {
						next = append(next, member)
					}
				case []any:
					if step != "*" {
						return fmt.Errorf("a list needs * at %q", step)
					}
					next = append(next, value...)
				}
			}
			holders = next
		}
	}
	for _, holder := range holders {
		object, ok := holder.(map[string]any)
		if !ok {
			continue
		}
		value, ok := object[rename.From]
		if !ok {
			continue
		}
		delete(object, rename.From)
		if rename.To == "" {
			continue
		}
		if _, taken := object[rename.To]; taken {
			return fmt.Errorf("both %s and %s are present", rename.From, rename.To)
		}
		object[rename.To] = value
	}
	return nil
}

func Load(r *repository.Repo, ref string) (*Config, error) {
	defer process.Phase(r.Context, "policy", "")()
	var data []byte
	var e error
	if ref == "" {
		var state *repository.State
		state, data, e = r.Snapshot(File)
		if e == nil && (state == nil || state.Kind != "file") {
			return nil, fmt.Errorf("missing %s: a regular file of at most 4 MiB in the working files", File)
		}
	} else {
		t, err := r.Tree(ref)
		if err != nil {
			return nil, err
		}
		v, ok := t[File]
		if !ok {
			return nil, fmt.Errorf("%s has no %s; historical inspection requires --policy-from worktree", ref, File)
		}
		data, e = r.Blob(v.OID)
	}
	if e != nil {
		return nil, e
	}
	c, e := decodePolicy(data)
	if e != nil {
		return nil, e
	}
	c.Raw = data
	if c.Exceptions, c.Bindings, e = readBindings(r, ref); e != nil {
		return nil, e
	}
	if e = c.Validate(); e != nil {
		return nil, e
	}
	if c.TransformationManifest != "" {
		var m []byte
		if ref == "" {
			var state *repository.State
			state, m, e = r.Snapshot(c.TransformationManifest)
			if e == nil && (state == nil || state.Kind != "file") {
				return nil, fmt.Errorf("missing transformation manifest %s: a regular file of at most 4 MiB in the working files", c.TransformationManifest)
			}
		} else {
			t, err := r.Tree(ref)
			if err != nil {
				return nil, err
			}
			v, ok := t[c.TransformationManifest]
			if !ok {
				return nil, fmt.Errorf("missing transformation manifest")
			}
			m, e = r.Blob(v.OID)
		}
		if e != nil {
			return nil, e
		}
		if e = artifact.Decode(m, &c.Manifest); e != nil {
			return nil, fmt.Errorf("%s: %w", c.TransformationManifest, e)
		}
		for name, pairs := range c.Manifest.Rewrites {
			if name == "" {
				return nil, fmt.Errorf("empty module")
			}
			for _, p := range pairs {
				if p[0] == "" || p[0] == p[1] {
					return nil, fmt.Errorf("invalid transformation for %s", name)
				}
			}
		}
		c.ManifestSHA256 = repository.Digest(m)
	}
	c.SHA256 = c.ScopedSHA256(c.BindingSources())
	return c, nil
}
func (c *Config) Validate() error {
	if c.Naming != nil {
		if err := c.Naming.Validate(); err != nil {
			return err
		}
	}
	if err := c.validateDocuments(); err != nil {
		return err
	}
	if err := c.validateChecks(); err != nil {
		return err
	}
	checkContracts := func(refs []string) error {
		seen := map[string]bool{}
		for _, ref := range refs {
			_, _, err := contracts.Target("", ref)
			if err != nil || seen[ref] {
				return fmt.Errorf("invalid or duplicate obligation reference %q", ref)
			}
			seen[ref] = true
		}
		return nil
	}
	if c.Format != Format || len(c.Roots) == 0 || len(c.Families) == 0 {
		return fmt.Errorf("policy requires format 2, roots and families; recapture historical artifacts or inspect with --policy-from worktree")
	}
	if !commitPattern.MatchString(c.Upstream) {
		return fmt.Errorf("upstream must be a full commit hash")
	}
	if c.Contract != "" {
		if err := checkContracts([]string{c.Contract}); err != nil {
			return err
		}
	}
	paths := func(ps []string) error {
		for _, p := range ps {
			if !repository.ValidPath(p) {
				return fmt.Errorf("unsafe selector %q", p)
			}
		}
		return nil
	}
	if e := paths(append(append(append([]string{}, c.Roots...), c.Boundaries...), c.PreserveExactly...)); e != nil {
		return e
	}
	ids := map[string]bool{}
	unique := func(id string) error {
		if !idPattern.MatchString(id) || ids[id] {
			return fmt.Errorf("invalid or duplicate policy ID %q", id)
		}
		ids[id] = true
		return nil
	}
	selectors := map[string]bool{}
	for _, f := range c.Families {
		if err := checkContracts(f.Contracts); err != nil {
			return err
		}
		if e := unique(f.ID); e != nil {
			return e
		}
		if f.Title == "" || len(f.Paths) == 0 {
			return fmt.Errorf("family needs a title and paths")
		}
		if e := paths(f.Paths); e != nil {
			return e
		}
		for _, p := range f.Paths {
			if selectors[p] {
				return fmt.Errorf("ambiguous family selector %q", p)
			}
			selectors[p] = true
		}
	}
	exact := map[string]bool{}
	for _, p := range c.PreserveExactly {
		if exact[p] || strings.HasSuffix(p, "/") {
			return fmt.Errorf("invalid exact file %q", p)
		}
		exact[p] = true
	}
	for name, k := range c.Cases {
		for _, w := range k.Witnesses {
			if _, _, err := WitnessTarget("", w); err != nil {
				return err
			}
		}
		if err := checkContracts(k.Contracts); err != nil {
			return err
		}
		if !idPattern.MatchString(name) || len(k.Surfaces) == 0 {
			return fmt.Errorf("invalid case %q", name)
		}
		seen := map[string]bool{}
		for _, s := range k.Surfaces {
			if s.ID == "" || seen[s.ID] || s.Purpose == "" || len(s.Paths) == 0 {
				return fmt.Errorf("invalid surface in %s", name)
			}
			seen[s.ID] = true
			if e := paths(s.Paths); e != nil {
				return e
			}
		}
		for _, p := range k.Probes {
			if !repository.ValidPath(p.Path) || len(p.Needles) == 0 || p.ID == "" {
				return fmt.Errorf("invalid probe")
			}
		}
	}
	if c.TransformationManifest != "" && !repository.ValidPath(c.TransformationManifest) {
		return fmt.Errorf("unsafe manifest path")
	}
	transformed := map[string]bool{}
	for _, rule := range c.Restorations {
		if err := unique(rule.ID); err != nil {
			return err
		}
		if !commitPattern.MatchString(rule.RemovedBy) || len(rule.Paths) == 0 {
			return fmt.Errorf("restoration %s requires an immutable removal commit and paths", rule.ID)
		}
		if err := paths(rule.Paths); err != nil {
			return err
		}
		if err := rule.Explanation.Validate(); err != nil {
			return fmt.Errorf("restoration %s: %w", rule.ID, err)
		}
	}
	for _, t := range c.Transforms {
		if e := unique(t.ID); e != nil {
			return e
		}
		recipes := 0
		if t.Module != "" {
			recipes++
		}
		if len(t.Replacements) > 0 {
			recipes++
		}
		if t.Tokens != nil {
			recipes++
		}
		if t.ASTGrep != nil {
			recipes++
		}
		if len(t.Paths) == 0 || recipes != 1 {
			return fmt.Errorf("transform %s requires paths and exactly one of module, replacements, tokens or ast_grep", t.ID)
		}
		if t.ASTGrep != nil {
			if err := t.ASTGrep.Validate(); err != nil {
				return fmt.Errorf("transform %s: %w", t.ID, err)
			}
			for _, path := range t.Paths {
				if syntax.Language(path) == "" || syntax.Language(path) != t.ASTGrep.Language {
					return fmt.Errorf("transform %s language does not match %s", t.ID, path)
				}
			}
		}
		tests := map[string]bool{}
		for _, tc := range t.Tests {
			if tc.Name == "" || tests[tc.Name] || !t.Matches(tc.Path) || len(tc.Before) > repository.MaxBytes || len(tc.After) > repository.MaxBytes {
				return fmt.Errorf("invalid fixture for %s", t.ID)
			}
			tests[tc.Name] = true
		}
		if t.Module != "" && c.TransformationManifest == "" {
			return fmt.Errorf("transform %s requires a shared transformation manifest", t.ID)
		}
		if err := t.Explanation.Validate(); err != nil {
			return fmt.Errorf("transform %s: %w", t.ID, err)
		}
		from := map[string]bool{}
		selected := map[string]bool{}
		replacements := t.Replacements
		if t.Tokens != nil {
			replacements = t.Tokens.Rules
			if len(replacements) == 0 {
				return fmt.Errorf("transform %s requires token rules", t.ID)
			}
			except := map[string]bool{}
			for _, e := range t.Tokens.Except {
				if e == "" || except[e] {
					return fmt.Errorf("transform %s requires distinct, nonempty token exclusions", t.ID)
				}
				except[e] = true
			}
		}
		for _, p := range replacements {
			if p.From == "" || p.From == p.To || from[p.From] {
				return fmt.Errorf("transform %s requires distinct, nonempty source literals and changed replacements", t.ID)
			}
			from[p.From] = true
			scope := p.Paths
			if scope == nil {
				scope = t.Paths
			} else if len(scope) == 0 {
				return fmt.Errorf("transform %s replacement paths must be nonempty", t.ID)
			}
			seen := map[string]bool{}
			for _, path := range scope {
				if !t.Matches(path) || seen[path] {
					return fmt.Errorf("transform %s replacement names duplicate or unselected path %q", t.ID, path)
				}
				seen[path], selected[path] = true, true
			}
		}
		for _, path := range t.Paths {
			if t.Module == "" && t.ASTGrep == nil && !selected[path] {
				return fmt.Errorf("transform %s has no replacement for %q", t.ID, path)
			}
			if !repository.ValidPath(path) || strings.HasSuffix(path, "/") || exact[path] || transformed[path] {
				return fmt.Errorf("invalid or overlapping transform path %q", path)
			}
			transformed[path] = true
		}
	}
	derived := map[string]string{}
	for _, d := range c.Derived {
		if err := unique(d.ID); err != nil {
			return err
		}
		check, ok := c.Checks[d.Check]
		if len(d.Paths) == 0 || len(d.Sources) == 0 || strings.TrimSpace(d.Generator) == "" || strings.ContainsAny(d.Generator, "\x00\n\r") || !ok {
			return fmt.Errorf("derived %s requires paths, sources, a one-line generator and a declared freshness check", d.ID)
		}
		if err := d.Explanation.Validate(); err != nil {
			return fmt.Errorf("derived %s: %w", d.ID, err)
		}
		if err := paths(d.Sources); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, source := range d.Sources {
			if seen[source] {
				return fmt.Errorf("derived %s repeats source %q", d.ID, source)
			}
			seen[source] = true
		}
		for _, path := range d.Paths {
			if !repository.ValidPath(path) || strings.HasSuffix(path, "/") || exact[path] || transformed[path] || derived[path] != "" {
				return fmt.Errorf("invalid or overlapping derived path %q", path)
			}
			for _, rule := range c.Restorations {
				if repository.Matches(path, rule.Paths) {
					return fmt.Errorf("derived path %q overlaps restoration %s", path, rule.ID)
				}
			}
			// Freshness evidence must bind the output bytes it vouches for.
			if !repository.Matches(path, check.Inputs) {
				return fmt.Errorf("derived %s check %s does not bind %s in its inputs", d.ID, d.Check, path)
			}
			derived[path] = d.ID
		}
	}
	byPath := map[string][]Exception{}
	bindings := map[string]string{}
	for _, x := range c.Exceptions {
		if other, ok := bindings[x.ID]; ok {
			return fmt.Errorf("exception ID %q is bound in both %s and %s; rename one", x.ID, BindingPath(other), BindingPath(x.Path))
		}
		bindings[x.ID] = x.Path
		if e := unique(x.ID); e != nil {
			return e
		}
		if !repository.ValidPath(x.Path) || strings.HasSuffix(x.Path, "/") || exact[x.Path] {
			return fmt.Errorf("invalid exception %s", x.ID)
		}
		if derived[x.Path] != "" {
			return fmt.Errorf("exception %s overlaps derived %s; a generated file has one explanation", x.ID, derived[x.Path])
		}
		if x.File != nil {
			if transformed[x.Path] {
				return fmt.Errorf("file exception %s overlaps a source transformation", x.ID)
			}
			if x.Fence || x.Start != 0 || x.End != 0 || x.UpstreamSHA256 != "" || x.ReplacementSHA256 != "" {
				return fmt.Errorf("file exception %s cannot also bind a source span", x.ID)
			}
			if err := x.File.Validate(); err != nil {
				return fmt.Errorf("file exception %s: %w", x.ID, err)
			}
		} else if !shaPattern.MatchString(x.UpstreamSHA256) || !shaPattern.MatchString(x.ReplacementSHA256) || x.Start < 0 || x.End < x.Start {
			return fmt.Errorf("invalid exception span %s", x.ID)
		}
		if x.Fence {
			if !SupportsAnnotations(x.Path) || x.Reason != "" || len(x.Witnesses) > 0 || x.RemoveWhen != "" || len(x.Preserves) > 0 {
				return fmt.Errorf("fenced exception %s owns its explanation in source", x.ID)
			}
		} else if err := x.Explanation().Validate(); err != nil {
			return fmt.Errorf("exception %s: %w", x.ID, err)
		}
		byPath[x.Path] = append(byPath[x.Path], x)
	}
	for _, xs := range byPath {
		for _, x := range xs {
			if x.File != nil && len(xs) != 1 {
				return fmt.Errorf("file exception cannot overlap another exception")
			}
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].Start < xs[j].Start })
		for i := 1; i < len(xs); i++ {
			if xs[i].Start < xs[i-1].End || xs[i].Start == xs[i-1].Start {
				return fmt.Errorf("overlapping exceptions")
			}
		}
	}
	return nil
}
func (c *Config) Family(path string) (string, error) {
	best := ""
	n := -1
	for _, f := range c.Families {
		for _, s := range f.Paths {
			if repository.Matches(path, []string{s}) && len(s) > n {
				best = f.ID
				n = len(s)
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("unmapped inherited path %q", path)
	}
	return best, nil
}
func (c *Config) Exact(path string) bool {
	for _, p := range c.PreserveExactly {
		if p == path {
			return true
		}
	}
	return false
}

// DerivedFor returns the entry that declares path a generator output, if any.
func (c *Config) DerivedFor(path string) *Derived {
	for i := range c.Derived {
		if slices.Contains(c.Derived[i].Paths, path) {
			return &c.Derived[i]
		}
	}
	return nil
}
func (c *Config) RootSelectors() []string {
	out := []string{}
	for _, s := range c.Roots {
		// An exact family selector declares a file explicitly. Directory roots
		// retain their existing spelling and normalization; no extension guess.
		file := false
		for _, family := range c.Families {
			if !strings.HasSuffix(s, "/") && slices.Contains(family.Paths, s) {
				file = true
			}
		}
		for _, check := range c.Checks {
			if !strings.HasSuffix(s, "/") && slices.Contains(check.Inputs, s) {
				file = true
			}
		}
		if file {
			out = append(out, s)
		} else {
			out = append(out, strings.TrimSuffix(s, "/")+"/")
		}
	}
	return out
}
