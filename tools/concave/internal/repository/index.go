package repository

import (
	"bytes"
	"fmt"
	"strings"
)

// UnchangedIndex returns, for working files, the index entries whose bytes
// Git vouches for without a read: Git's stat comparison finds the file clean
// (it compares content where a stat cannot decide), the entry is an ordinary
// stage-0 file or link with no assume-unchanged or skip-worktree flag, and no
// conversion (autocrlf, text, eol, filter, ident, working-tree-encoding) can
// make stored bytes differ from working bytes. Every other path is absent and
// must be read. The shared index is only read, never refreshed.
func (r *Repo) UnchangedIndex() (map[string]Entry, error) {
	if r.Revision != "" {
		return map[string]Entry{}, nil
	}
	for key, trusted := range map[string]func(string) bool{
		"core.autocrlf": func(v string) bool { return v == "" || v == "false" },
		"core.filemode": func(v string) bool { return v == "" || v == "true" },
	} {
		value, err := r.Git(nil, "--no-optional-locks", "config", "--default", "", "--get", key)
		if err != nil {
			return nil, err
		}
		if !trusted(strings.ToLower(strings.TrimSpace(string(value)))) {
			return map[string]Entry{}, nil
		}
	}
	staged, err := r.Git(nil, "--no-optional-locks", "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	entries := map[string]Entry{}
	for _, record := range bytes.Split(staged, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, path, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("invalid Git index listing")
		}
		mode, oid, stage := fields[0], fields[1], fields[2]
		if stage == "0" && (mode == "100644" || mode == "100755" || mode == "120000") {
			entries[path] = Entry{Mode: mode, Kind: "blob", OID: oid}
		}
	}
	tags, err := r.Git(nil, "--no-optional-locks", "ls-files", "-v", "-z")
	if err != nil {
		return nil, err
	}
	for _, record := range bytes.Split(tags, []byte{0}) {
		if tag, path, ok := strings.Cut(string(record), " "); ok && tag != "H" {
			delete(entries, path)
		}
	}
	dirty, err := r.Git(nil, "--no-optional-locks", "-c", "core.fsmonitor=false", "diff-files", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range bytes.Split(dirty, []byte{0}) {
		delete(entries, string(path))
	}
	var paths bytes.Buffer
	for path := range entries {
		paths.WriteString(path)
		paths.WriteByte(0)
	}
	attributes, err := r.Git(paths.Bytes(), "--no-optional-locks", "check-attr", "--stdin", "-z", "text", "eol", "filter", "ident", "working-tree-encoding")
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(attributes, []byte{0})
	for i := 0; i+2 < len(fields); i += 3 {
		path, attribute, value := string(fields[i]), string(fields[i+1]), string(fields[i+2])
		// A binary (text unset) path is stored as is; any other setting converts.
		if value != "unspecified" && !(attribute == "text" && value == "unset") {
			delete(entries, path)
		}
	}
	return entries, nil
}
