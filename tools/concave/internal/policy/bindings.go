package policy

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"concave/internal/artifact"
	"concave/internal/repository"
)

// BindingsDirectory holds generated approval bindings, one file per source
// path at a mirrored location, so unrelated paths never share a file.
const BindingsDirectory = "concave.exceptions"

// BindingsFormat versions one binding file.
const BindingsFormat = 1

type bindingFile struct {
	Format     int         `json:"format"`
	Exceptions []Exception `json:"exceptions"`
}

// BindingPath is the file holding the bindings of one source path.
func BindingPath(source string) string { return BindingsDirectory + "/" + source + ".json" }

// BindingSource is the source path a binding file describes.
func BindingSource(file string) (string, bool) {
	source, inside := strings.CutPrefix(file, BindingsDirectory+"/")
	source, named := strings.CutSuffix(source, ".json")
	return source, inside && named && repository.ValidPath(source) && !strings.HasSuffix(source, "/")
}

// readBindings decodes every binding file of a revision, or of the working
// files when ref is empty, keyed by source path with its exact bytes.
func readBindings(repo *repository.Repo, ref string) ([]Exception, map[string][]byte, error) {
	var files map[string][]byte
	if ref == "" {
		var err error
		if files, err = repo.ReadTree(BindingsDirectory + "/"); err != nil {
			return nil, nil, err
		}
	} else {
		tree, err := repo.Tree(ref)
		if err != nil {
			return nil, nil, err
		}
		files = map[string][]byte{}
		for path, entry := range tree {
			if !strings.HasPrefix(path, BindingsDirectory+"/") {
				continue
			}
			var data []byte
			if entry.Mode == "100644" {
				if data, err = repo.Blob(entry.OID); err != nil {
					return nil, nil, err
				}
			}
			if data == nil {
				return nil, nil, fmt.Errorf("%s must be a regular non-executable file of at most 4 MiB", path)
			}
			files[path] = data
		}
	}
	exceptions := []Exception{}
	raw := map[string][]byte{}
	for _, file := range slices.Sorted(maps.Keys(files)) {
		source, ok := BindingSource(file)
		if !ok {
			return nil, nil, fmt.Errorf("%s is not a binding file; %s holds only <source path>.json", file, BindingsDirectory)
		}
		data := files[file]
		decoded := new(bindingFile)
		if err := artifact.Decode(data, decoded); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		if decoded.Format != BindingsFormat {
			return nil, nil, fmt.Errorf("%s: unsupported approval bindings format", file)
		}
		if len(decoded.Exceptions) == 0 {
			return nil, nil, fmt.Errorf("%s holds no bindings; delete it", file)
		}
		for _, binding := range decoded.Exceptions {
			if binding.Path != source {
				return nil, nil, fmt.Errorf("%s binds %s to %q; a binding lives in its own source path's file", file, binding.ID, binding.Path)
			}
		}
		exceptions = append(exceptions, decoded.Exceptions...)
		raw[source] = data
	}
	return exceptions, raw, nil
}

// EncodeBindings is the one deterministic encoding of a source path's
// bindings: ordered by ID, or nil when none remain and the file is absent.
func EncodeBindings(source string, exceptions []Exception) ([]byte, error) {
	ordered := []Exception{}
	for _, binding := range exceptions {
		if binding.Path != source {
			return nil, fmt.Errorf("binding %s belongs to %s, not %s", binding.ID, binding.Path, source)
		}
		ordered = append(ordered, binding)
	}
	if len(ordered) == 0 {
		return nil, nil
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	b, err := json.MarshalIndent(bindingFile{Format: BindingsFormat, Exceptions: ordered}, "", "  ")
	return append(b, '\n'), err
}

// Inputs are the policy files that work confined to paths reads: the policy,
// the transformation manifest and the existing binding files of those paths.
func (c *Config) Inputs(paths []string) []string {
	inputs := []string{File}
	if c.TransformationManifest != "" {
		inputs = append(inputs, c.TransformationManifest)
	}
	for _, path := range paths {
		if _, ok := c.Bindings[path]; ok {
			inputs = append(inputs, BindingPath(path))
		}
	}
	slices.Sort(inputs)
	return slices.Compact(inputs)
}

// BindingSources lists every source path with a binding file, sorted.
func (c *Config) BindingSources() []string {
	paths := make([]string, 0, len(c.Bindings))
	for path := range c.Bindings {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// ScopedSHA256 is the policy identity seen by work confined to paths: the
// policy file, the transformation manifest, and the presence and bytes of
// exactly those paths' binding files. Bindings of other paths never enter it,
// so concurrent approvals of unrelated paths cannot invalidate each other.
// SHA256 is the same identity over every binding file.
func (c *Config) ScopedSHA256(paths []string) string {
	paths = slices.Clone(paths)
	slices.Sort(paths)
	var b strings.Builder
	b.WriteString(repository.Digest(c.Raw))
	b.WriteString("\x00")
	b.WriteString(c.ManifestSHA256)
	for _, path := range slices.Compact(paths) {
		b.WriteString("\x00")
		b.WriteString(path)
		b.WriteString("\x00")
		if data, ok := c.Bindings[path]; ok {
			b.WriteString(repository.Digest(data))
		}
	}
	return repository.Digest([]byte(b.String()))
}
