package repository

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Hunk struct {
	// File changes are atomic: existence and mode changes include all source bytes.
	BeforeMode   string `json:"before_mode,omitempty"`
	AfterMode    string `json:"after_mode,omitempty"`
	ID           string `json:"id"`
	Path         string `json:"path"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	CurrentStart int    `json:"current_start"`
	CurrentEnd   int    `json:"current_end"`
	Before       string `json:"before"`
	After        string `json:"after"`
	// Approval is the policy entry inspection found covering this hunk.
	Approval string `json:"approval,omitempty"`
}

var header = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func Lines(s string) []string {
	if s == "" {
		return nil
	}
	v := strings.SplitAfter(s, "\n")
	if v[len(v)-1] == "" {
		v = v[:len(v)-1]
	}
	return v
}
func number(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, _ := strconv.Atoi(s)
	return v
}
func gitPath(s string) (string, error) {
	s = strings.TrimSuffix(s, "\t")
	if strings.HasPrefix(s, `"`) {
		p, e := strconv.Unquote(s)
		return p, e
	}
	return strings.TrimSuffix(s, "\t"), nil
}

// concave:invariant exact-diff-bytes
// Preserve every hunk span and byte, including CRLF and missing final newlines.
// Reusing a buffer must never mutate a previously parsed hunk or file.
func ParseDiff(data []byte) (map[string][]Hunk, error) {
	out := map[string][]Hunk{}
	path := ""
	old := ""
	var h *Hunk
	var before, after bytes.Buffer
	last := byte(0)
	flush := func() {
		if h != nil {
			h.Path = path
			h.Before, h.After = before.String(), after.String()
			out[path] = append(out[path], *h)
			before.Reset()
			after.Reset()
			h = nil
		}
	}
	for _, line := range Lines(string(data)) {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			path = ""
			old = ""
			continue
		}
		if h == nil && strings.HasPrefix(line, "--- ") {
			p, e := gitPath(strings.TrimSuffix(line[4:], "\n"))
			if e != nil {
				return nil, e
			}
			old = strings.TrimPrefix(p, "a/")
			continue
		}
		if h == nil && strings.HasPrefix(line, "+++ ") {
			p, e := gitPath(strings.TrimSuffix(line[4:], "\n"))
			if e != nil {
				return nil, e
			}
			path = strings.TrimPrefix(p, "b/")
			if p == "/dev/null" {
				path = old
			}
			continue
		}
		if m := header.FindStringSubmatch(line); m != nil {
			flush()
			a, b := number(m[1], 0), number(m[2], 1)
			c, d := number(m[3], 0), number(m[4], 1)
			if b > 0 {
				a--
			}
			if d > 0 {
				c--
			}
			h = &Hunk{Start: a, End: a + b, CurrentStart: c, CurrentEnd: c + d}
			continue
		}
		if h != nil && len(line) > 0 {
			switch line[0] {
			case '-':
				before.WriteString(line[1:])
				last = '-'
			case '+':
				after.WriteString(line[1:])
				last = '+'
			case '\\':
				if last == '-' && bytes.HasSuffix(before.Bytes(), []byte("\n")) {
					before.Truncate(before.Len() - 1)
				}
				if last == '+' && bytes.HasSuffix(after.Bytes(), []byte("\n")) {
					after.Truncate(after.Len() - 1)
				}
			}
		}
	}
	flush()
	for p, hs := range out {
		if !ValidPath(p) {
			return nil, fmt.Errorf("unsafe diff path %q", p)
		}
		for _, h := range hs {
			if len(Lines(h.Before)) != h.End-h.Start || len(Lines(h.After)) != h.CurrentEnd-h.CurrentStart {
				return nil, fmt.Errorf("malformed diff for %q", p)
			}
		}
	}
	return out, nil
}
func sourceDiffArgs() []string {
	return []string{"diff", "--no-color", "--src-prefix=a/", "--dst-prefix=b/", "--no-ext-diff", "--no-textconv", "--no-renames", "--diff-algorithm=myers", "--no-indent-heuristic", "--text", "--unified=0"}
}

func (r *Repo) DiffPatch(base, rev string, paths []string) ([]byte, error) {
	if rev == "" {
		return r.workingPatch(base, paths)
	}
	args := append(sourceDiffArgs(), base)
	if rev != "" {
		args = append(args, rev)
	}
	args = append(args, "--")
	args = append(args, paths...)
	return r.Git(nil, args...)
}
func (r *Repo) Diff(base, rev string, paths []string) (map[string][]Hunk, error) {
	b, e := r.DiffPatch(base, rev, paths)
	if e != nil {
		return nil, e
	}
	return ParseDiff(b)
}

// concave:invariant stable-hunk-identity
// Bind each hunk ID to its path, complete before/after bytes and both spans.
// Hashing the complete source must not repeat for each hunk in the same file.
func Identify(path string, before, after []byte, hunks []Hunk) []Hunk {
	if len(hunks) == 0 {
		return hunks
	}
	// Every hunk shares these exact source identities. Hash each input once,
	// retaining the existing ID encoding and preimage binding.
	beforeSHA, afterSHA := Digest(before), Digest(after)
	for i := range hunks {
		h := &hunks[i]
		h.Path = path
		b, _ := json.Marshal([]any{path, beforeSHA, afterSHA, h.Start, h.End, h.CurrentStart, h.CurrentEnd})
		if h.BeforeMode != "" || h.AfterMode != "" {
			b, _ = json.Marshal([]any{string(b), h.BeforeMode, h.AfterMode})
		}
		h.ID = Digest(b)[:20]
	}
	return hunks
}

// Patch serializes selected replacement spans without restoring whole files.
func Patch(path string, hs []Hunk) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "diff --git %s %s\n--- %s\n+++ %s\n", strconv.Quote("a/"+path), strconv.Quote("b/"+path), strconv.Quote("a/"+path), strconv.Quote("b/"+path))
	for _, h := range hs {
		if h.BeforeMode != "" || h.AfterMode != "" {
			fmt.Fprintf(&b, "file mode %s -> %s (complete file)\n", h.BeforeMode, h.AfterMode)
		}
		a, c := h.Start, h.CurrentStart
		if h.End > h.Start {
			a++
		}
		if h.CurrentEnd > h.CurrentStart {
			c++
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", a, h.End-h.Start, c, h.CurrentEnd-h.CurrentStart)
		for _, side := range []struct {
			prefix string
			text   string
		}{{"-", h.Before}, {"+", h.After}} {
			for _, line := range Lines(side.text) {
				b.WriteString(side.prefix + line)
				if !strings.HasSuffix(line, "\n") {
					b.WriteString("\n\\ No newline at end of file\n")
				}
			}
		}
	}
	return b.Bytes()
}
