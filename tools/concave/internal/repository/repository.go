// Package repository owns bounded Git and filesystem reads. It never stages files.
package repository

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxBytes = 4 << 20
const MaxOutput = 128 << 20

type Entry struct {
	Mode string `json:"mode"`
	Kind string `json:"kind"`
	OID  string `json:"oid"`
}
type State struct {
	Kind   string `json:"kind"`
	Mode   string `json:"mode,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	OID    string `json:"oid,omitempty"`
	Size   int64  `json:"size,omitempty"`
}
type Repo struct {
	Root      string
	Revision  string
	Context   context.Context
	Processes int
	Reads     int
	BlobReads int
	blobs     map[string][]byte
	blobBytes int
	// sizes records each oversized blob's size, drained without retention.
	sizes        map[string]int64
	objectFormat string
	trees        map[string]map[string]Entry
	treeBytes    int
	resolved     map[string]string
	batch        *exec.Cmd
	input        io.WriteCloser
	output       *bufio.Reader
	mu           sync.Mutex
	parent       *Repo
	sourceTree   map[string]Entry
}

// At supplies an immutable source view through the same bounded repository read
// API. It creates no checkout and never resolves source from working files.
func (r *Repo) At(revision string) (*Repo, error) {
	revision, err := r.Resolve(revision)
	if err != nil {
		return nil, err
	}
	tree, err := r.Tree(revision)
	if err != nil {
		return nil, err
	}
	return &Repo{Root: r.Root, Context: r.Context, parent: r, Revision: revision, objectFormat: r.objectFormat, sourceTree: tree}, nil
}

// WithContext opens an independent read handle on the same source basis. Scoped
// execution settings and cancellation never mutate the caller's handle. Close the
// child to release its own Git process and aggregate its observations.
func (r *Repo) WithContext(ctx context.Context) *Repo {
	return &Repo{Root: r.Root, Revision: r.Revision, Context: ctx, objectFormat: r.objectFormat,
		sourceTree: r.sourceTree, trees: maps.Clone(r.trees), treeBytes: r.treeBytes,
		resolved: maps.Clone(r.resolved), parent: r}
}

func Digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func Text(b []byte) bool     { return b != nil && utf8.Valid(b) && !bytes.ContainsRune(b, 0) }
func ValidPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, s := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if s == "" || s == "." || s == ".." || s == ".git" {
			return false
		}
	}
	return true
}
func Matches(p string, selectors []string) bool {
	for _, s := range selectors {
		if p == s || strings.HasSuffix(s, "/") && strings.HasPrefix(p, s) {
			return true
		}
	}
	return false
}
func Open(ctx context.Context, dir string) (*Repo, error) {
	r := &Repo{Root: dir, Context: ctx}
	b, e := r.Git(nil, "rev-parse", "--show-toplevel")
	if e != nil {
		return nil, e
	}
	r.Root = strings.TrimSpace(string(b))
	b, e = r.Git(nil, "rev-parse", "--show-object-format")
	r.objectFormat = strings.TrimSpace(string(b))
	return r, e
}

type bounded struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *bounded) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.overflow = true
		return 0, fmt.Errorf("Git output exceeds %d bytes", b.max)
	}
	return b.Buffer.Write(p)
}
func (r *Repo) Git(input []byte, args ...string) ([]byte, error) {
	return r.gitAt(r.Root, input, false, args...)
}

// Difference status 1 is accepted only for the private no-index source comparison.
func (r *Repo) gitAt(dir string, input []byte, differenceStatus bool, args ...string) ([]byte, error) {
	r.Processes++
	prefix := []string{"-c", "core.quotePath=true"}
	// check-ignore consumes literal names and rejects Git pathspec magic.
	if len(args) > 0 && args[0] != "check-ignore" {
		prefix = append(prefix, "--literal-pathspecs")
	}
	c := exec.CommandContext(r.Context, "git", append(prefix, args...)...)
	c.Dir = dir
	c.Stdin = bytes.NewReader(input)
	out := &bounded{max: MaxOutput}
	errout := &bounded{max: 1 << 20}
	c.Stdout = out
	c.Stderr = errout
	e := c.Run()
	if out.overflow || errout.overflow {
		return nil, fmt.Errorf("git %s output exceeds bound", args[0])
	}
	if e != nil {
		var status *exec.ExitError
		if !differenceStatus || !errors.As(e, &status) || status.ExitCode() != 1 {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], e, errout.String())
		}
	}
	return out.Bytes(), nil
}

// Only immutable full object IDs are memoized. HEAD and named refs stay live.
func (r *Repo) Resolve(ref string) (string, error) {
	if r.Revision != "" && (ref == "HEAD" || strings.HasPrefix(ref, "HEAD^")) {
		ref = r.Revision + strings.TrimPrefix(ref, "HEAD")
	}
	if r.objectID(ref) && r.resolved[ref] != "" {
		return r.resolved[ref], nil
	}
	b, err := r.Git(nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	resolved := strings.TrimSpace(string(b))
	if err == nil && r.objectID(ref) && len(r.resolved) < 256 {
		if r.resolved == nil {
			r.resolved = map[string]string{}
		}
		r.resolved[ref] = resolved
	}
	return resolved, err
}
func (r *Repo) objectID(ref string) bool {
	length := 40
	if r.objectFormat == "sha256" {
		length = 64
	}
	if len(ref) != length {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}
func (r *Repo) Ancestor(a, b string) error {
	_, e := r.Git(nil, "merge-base", "--is-ancestor", a, b)
	return e
}
func (r *Repo) Tree(ref string) (map[string]Entry, error) {
	if !r.objectID(ref) {
		var err error
		ref, err = r.Resolve(ref)
		if err != nil {
			return nil, err
		}
	}
	if tree, ok := r.trees[ref]; ok {
		return maps.Clone(tree), nil
	}
	b, e := r.Git(nil, "ls-tree", "-rz", "--full-tree", ref)
	if e != nil {
		return nil, e
	}
	v := map[string]Entry{}
	for _, line := range bytes.Split(b, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		m, p, ok := strings.Cut(string(line), "\t")
		f := strings.Fields(m)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("invalid Git tree")
		}
		v[p] = Entry{f[0], f[1], f[2]}
	}
	// Bounded per invocation; callers receive independent maps.
	if len(r.trees) < 16 && len(b) <= (32<<20)-r.treeBytes {
		if r.trees == nil {
			r.trees = map[string]map[string]Entry{}
		}
		r.trees[ref] = maps.Clone(v)
		r.treeBytes += len(b)
	}
	return v, nil
}

// Blob keeps one cat-file process alive for the entire invocation. Large blobs
// are drained without retaining their bytes; callers must report missing text.
func (r *Repo) Blob(oid string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.blobs[oid]; ok {
		return bytes.Clone(b), nil
	}
	r.BlobReads++
	if r.batch == nil {
		r.Processes++
		r.batch = exec.CommandContext(r.Context, "git", "cat-file", "--batch")
		r.batch.Dir = r.Root
		var e error
		r.input, e = r.batch.StdinPipe()
		if e != nil {
			return nil, e
		}
		out, e := r.batch.StdoutPipe()
		if e != nil {
			return nil, e
		}
		r.output = bufio.NewReader(out)
		if e = r.batch.Start(); e != nil {
			return nil, e
		}
	}
	if _, e := fmt.Fprintln(r.input, oid); e != nil {
		return nil, e
	}
	line, e := r.output.ReadString('\n')
	if e != nil {
		return nil, e
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[1] != "blob" {
		return nil, fmt.Errorf("unavailable blob %s", oid)
	}
	n, e := strconv.ParseInt(f[2], 10, 64)
	if e != nil || n < 0 {
		return nil, fmt.Errorf("invalid blob size")
	}
	if n > MaxBytes {
		if r.sizes == nil {
			r.sizes = map[string]int64{}
		}
		if len(r.sizes) < 40000 {
			r.sizes[oid] = n
		}
		_, e = io.CopyN(io.Discard, r.output, n+1)
		return nil, e
	}
	b := make([]byte, n+1)
	_, e = io.ReadFull(r.output, b)
	if e != nil {
		return nil, e
	}
	if r.objectID(oid) && len(r.blobs) < 40000 && int(n) <= (32<<20)-r.blobBytes {
		if r.blobs == nil {
			r.blobs = map[string][]byte{}
		}
		r.blobs[oid] = bytes.Clone(b[:n])
		r.blobBytes += int(n)
	}
	return b[:n], nil
}
func (r *Repo) Close() {
	if r.batch != nil {
		r.input.Close()
		r.batch.Wait()
		r.batch = nil
	}
	if r.parent != nil {
		r.parent.Processes += r.Processes
		r.parent.Reads += r.Reads
		r.parent.BlobReads += r.BlobReads
		r.parent = nil
	}
}
func (r *Repo) OID(data []byte) string {
	oid, _ := r.streamOID(bytes.NewReader(data), int64(len(data)), nil, nil)
	return oid
}

// streamOID hashes exactly size bytes as a Git blob, and into also when given,
// in bounded memory through buffer (allocated when nil).
func (r *Repo) streamOID(f io.Reader, size int64, also io.Writer, buffer []byte) (string, error) {
	var h hash.Hash = sha1.New()
	if r.objectFormat == "sha256" {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", size)
	var w io.Writer = h
	if also != nil {
		w = io.MultiWriter(h, also)
	}
	n, err := io.CopyBuffer(w, io.LimitReader(f, size+1), buffer)
	if err != nil {
		return "", err
	}
	if n != size {
		return "", &Stale{Message: "input changed while reading"}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (r *Repo) Snapshot(p string) (*State, []byte, error) {
	reader := r.Reader()
	defer reader.Close()
	return reader.Snapshot(p)
}

// Reader reads paths as Snapshot does, keeping the preceding path's open
// directory chain, so consecutive paths beneath one directory check and open it
// once. Sorted paths share the most; any order stays exact. Close releases it.
type Reader struct {
	repo   *Repo
	chain  *directories
	buffer []byte
}

func (r *Repo) Reader() *Reader { return &Reader{repo: r} }

func (reader *Reader) Snapshot(p string) (*State, []byte, error) { return reader.read(p, true) }

// Identity is Snapshot without the bytes: a working file streams through its
// digests in one reused buffer and is never retained.
func (reader *Reader) Identity(p string) (*State, error) {
	state, _, err := reader.read(p, false)
	return state, err
}

func (reader *Reader) read(p string, retain bool) (*State, []byte, error) {
	r := reader.repo
	r.Reads++
	if !ValidPath(p) || strings.HasSuffix(p, "/") {
		return nil, nil, fmt.Errorf("unsafe path %q", p)
	}
	if r.Revision != "" {
		entry, ok := r.sourceTree[p]
		if !ok {
			return nil, nil, nil
		}
		var data []byte
		var err error
		if entry.Mode == "100644" || entry.Mode == "100755" {
			data, err = r.Blob(entry.OID)
		}
		if err != nil {
			return nil, nil, err
		}
		state, err := r.BlobState(entry, data)
		return state, data, err
	}
	if r.Context != nil && r.Context.Err() != nil {
		return nil, nil, r.Context.Err()
	}
	if reader.chain == nil {
		base, err := os.OpenRoot(r.Root)
		if err != nil {
			return nil, nil, err
		}
		reader.chain = &directories{base: base}
	}
	dir, name := path.Split(p)
	var parts []string
	if dir != "" {
		parts = strings.Split(strings.TrimSuffix(dir, "/"), "/")
	}
	root, state, err := reader.chain.enter(parts)
	if err != nil || root == nil {
		return state, nil, err
	}
	return reader.snapshotIn(root, name, p, retain)
}

func (reader *Reader) Close() {
	if reader.chain != nil {
		reader.chain.close()
		reader.chain = nil
	}
}

// directories is the open directory chain of one reader: open[i] is
// the directory names[:i+1] below base, each component checked not to be a
// link before it is opened. An absent or linked component ends the chain as
// its last name; every path beneath it shares that outcome.
type directories struct {
	base   *os.Root
	names  []string
	open   []*os.Root
	ended  bool
	linked bool
}

// enter returns the directory for parts, or a nil root with the state of a path
// beneath an absent (nil) or linked ancestor.
func (d *directories) enter(parts []string) (*os.Root, *State, error) {
	k := 0
	for k < len(d.names) && k < len(parts) && d.names[k] == parts[k] {
		k++
	}
	if d.ended && k == len(d.names) {
		return nil, d.blocked(), nil
	}
	for _, root := range d.open[k:] {
		root.Close()
	}
	d.names, d.open, d.ended, d.linked = d.names[:k], d.open[:k], false, false
	root := d.base
	if k > 0 {
		root = d.open[k-1]
	}
	for _, part := range parts[k:] {
		info, err := root.Lstat(part)
		if os.IsNotExist(err) || err == nil && info.Mode()&os.ModeSymlink != 0 {
			d.names, d.ended, d.linked = append(d.names, part), true, err == nil
			return nil, d.blocked(), nil
		}
		if err != nil {
			return nil, nil, err
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return nil, nil, err
		}
		d.names, d.open = append(d.names, part), append(d.open, next)
		root = next
	}
	return root, nil, nil
}

func (d *directories) blocked() *State {
	if d.linked {
		return &State{Kind: "symlink_parent"}
	}
	return nil
}

func (d *directories) close() {
	for _, root := range d.open {
		root.Close()
	}
	d.base.Close()
}

// snapshotIn reads name in its already opened, link-checked directory.
func (reader *Reader) snapshotIn(root *os.Root, name, p string, retain bool) (*State, []byte, error) {
	r := reader.repo
	pre, e := root.Lstat(name)
	if os.IsNotExist(e) {
		return nil, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	if pre.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(name)
		if err != nil {
			return nil, nil, err
		}
		after, err := root.Lstat(name)
		if err != nil || !os.SameFile(pre, after) || pre.ModTime() != after.ModTime() || pre.Size() != after.Size() {
			return nil, nil, &Stale{Message: "link changed while reading: " + p}
		}
		data := []byte(target)
		return &State{Kind: "symlink", Mode: "120000", SHA256: Digest(data), OID: r.OID(data), Size: int64(len(data))}, nil, nil
	}
	if !pre.Mode().IsRegular() {
		return &State{Kind: "nonregular"}, nil, nil
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, nil, e
	}
	mode := "100644"
	if st.Mode()&0100 != 0 {
		mode = "100755"
	}
	if !st.Mode().IsRegular() {
		return &State{Kind: "nonregular", Mode: mode}, nil, nil
	}
	if reader.buffer == nil {
		reader.buffer = make([]byte, 64<<10)
	}
	if !retain && st.Size() <= MaxBytes {
		sum := sha256.New()
		oid, err := r.streamOID(f, st.Size(), sum, reader.buffer)
		if err != nil {
			return nil, nil, err
		}
		after, err := root.Lstat(name)
		if err != nil || !os.SameFile(st, after) || after.Size() != st.Size() || after.ModTime() != st.ModTime() {
			return nil, nil, &Stale{Message: "input changed while reading: " + p}
		}
		return &State{Kind: "file", Mode: mode, SHA256: hex.EncodeToString(sum.Sum(nil)), OID: oid, Size: st.Size()}, nil, nil
	}
	if st.Size() > MaxBytes {
		// Identity without retention: an oversized file equal to its Git blob
		// is exact. Its bytes are streamed through the object hash only.
		state := &State{Kind: "oversize", Mode: mode, Size: st.Size()}
		if st.Size() <= MaxOutput {
			oid, err := r.streamOID(f, st.Size(), nil, reader.buffer)
			if err != nil {
				return nil, nil, err
			}
			after, err := root.Lstat(name)
			if err != nil || !os.SameFile(st, after) || after.Size() != st.Size() || after.ModTime() != st.ModTime() {
				return nil, nil, &Stale{Message: "input changed while reading: " + p}
			}
			state.OID = oid
		}
		return state, nil, nil
	}
	b, e := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if e != nil {
		return nil, nil, e
	}
	after, e := root.Lstat(name)
	if e != nil || !os.SameFile(st, after) || after.Size() != st.Size() || after.ModTime() != st.ModTime() || len(b) > MaxBytes {
		return nil, nil, &Stale{Message: "input changed while reading: " + p}
	}
	return &State{Kind: "file", Mode: mode, SHA256: Digest(b), OID: r.OID(b), Size: int64(len(b))}, b, nil
}

// BlobState is a Git entry's state in the terms a working read gives: an
// oversized blob, never retained, still carries its size, so a revision input
// and the same bytes materialized in a checkout compare equal.
func (r *Repo) BlobState(e Entry, b []byte) (*State, error) {
	s := BlobState(e, b)
	if s == nil || s.Kind != "oversize" {
		return s, nil
	}
	r.mu.Lock()
	size, ok := r.sizes[e.OID]
	r.mu.Unlock()
	if !ok {
		out, err := r.Git(nil, "cat-file", "-s", e.OID)
		if err != nil {
			return nil, err
		}
		if size, err = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err != nil {
			return nil, fmt.Errorf("invalid blob size")
		}
	}
	s.Size = size
	return s, nil
}

func BlobState(e Entry, b []byte) *State {
	if e.OID == "" {
		return nil
	}
	s := &State{Kind: e.Kind, Mode: e.Mode, OID: e.OID}
	if e.Mode == "100644" || e.Mode == "100755" {
		s.Kind = "file"
		if b == nil {
			s.Kind = "oversize"
		} else {
			s.SHA256 = Digest(b)
			s.Size = int64(len(b))
		}
	}
	return s
}
func Same(a, b *State) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func (r *Repo) Untracked() ([]string, error) {
	if r.Revision != "" {
		return []string{}, nil
	}
	b, e := r.Git(nil, "ls-files", "--others", "--exclude-standard", "-z")
	v := []string{}
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" {
			v = append(v, p)
		}
	}
	sort.Strings(v)
	return v, e
}
func (r *Repo) OutputDirectory(p string) (string, error) {
	abs, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	if _, e = os.Lstat(abs); !os.IsNotExist(e) {
		return "", fmt.Errorf("output must be a new directory: %s", abs)
	}
	// Canonicalize the nearest existing parent before deciding whether output
	// is inside the checkout (macOS /var itself is a symlink).
	parent := filepath.Dir(abs)
	tail := []string{filepath.Base(abs)}
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			abs = filepath.Join(append([]string{resolved}, tail...)...)
			break
		}
		if !os.IsNotExist(err) || parent == filepath.Dir(parent) {
			return "", err
		}
		tail = append([]string{filepath.Base(parent)}, tail...)
		parent = filepath.Dir(parent)
	}
	rel, e := filepath.Rel(r.Root, abs)
	if e != nil {
		return "", e
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if _, e = r.Git(nil, "check-ignore", "-q", "--", filepath.ToSlash(rel)+"/"); e != nil {
			return "", fmt.Errorf("output must be ignored or outside checkout")
		}
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return "", e
	}
	return abs, nil
}
