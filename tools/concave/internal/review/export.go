package review

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"concave/internal/artifact"
	"concave/internal/policy"
	"concave/internal/process"
	"concave/internal/repository"
)

// SelectContent materializes only explicitly selected hunks over land.py's
// contribution baseline. Baseline disagreement requires recapture, not relocation.
func SelectContent(repo *repository.Repo, config *policy.Config, r *Contribution, dir string) (*SelectedContent, error) {
	if err := r.Validate(repo, config); err != nil {
		return nil, err
	}
	if len(r.Selected) == 0 {
		return nil, fmt.Errorf("export requires explicit contribution --hunk selection; capture the selected IDs first")
	}
	selected := map[string]bool{}
	for _, id := range r.Selected {
		selected[id] = true
	}
	groups := map[string][]repository.Hunk{}
	for _, h := range r.Hunks {
		if selected[h.ID] {
			groups[h.Path] = append(groups[h.Path], h)
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("empty contribution")
	}
	baseSources := map[string]string{}
	for _, s := range r.Context.Sources {
		if s.Version == "baseline" && s.Unavailable == "" {
			baseSources[s.Path] = r.Context.Blobs[s.SHA256]
		}
	}
	paths := make([]string, 0, len(groups))
	result := &SelectedContent{}
	for path := range groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	args := append([]string{"python3", "scripts/land.py", "--baselines"}, paths...)
	finish := process.Phase(repo.Context, "baseline", fmt.Sprintf("read %d contribution baselines", len(paths)))
	out, err := process.Run(repo.Context, repo.Root, args, nil, repository.MaxOutput)
	finish()
	if err != nil {
		return nil, fmt.Errorf("read landing baselines: %w", err)
	}
	var baselines struct {
		Format int    `json:"format"`
		Kind   string `json:"kind"`
		Head   string `json:"head"`
		Files  map[string]struct {
			Identity string `json:"identity"`
			Data     []byte `json:"data"`
		} `json:"files"`
	}
	if err := artifact.Decode(out.Stdout, &baselines); err != nil {
		return nil, err
	}
	if baselines.Format != 1 || baselines.Kind != "land-baselines" || len(baselines.Files) != len(paths) {
		return nil, fmt.Errorf("invalid landing baseline snapshot")
	}
	data := map[string][]byte{}
	for _, path := range paths {
		baseline, ok := baselines.Files[path]
		if !ok {
			return nil, fmt.Errorf("missing landing baseline: %s", path)
		}
		identity := baseline.Identity
		mode, _, ok := strings.Cut(identity, ":")
		if identity == "absent" {
			if r.Inputs[path] == nil || r.Inputs[path].Kind != "file" {
				return nil, fmt.Errorf("new contribution must be a regular file: %s", path)
			}
			mode, ok = r.Inputs[path].Mode, true
		}
		if !ok || (mode != "100644" && mode != "100755") {
			return nil, fmt.Errorf("export requires a regular landing baseline or a new regular file: %s", path)
		}
		if string(baseline.Data) != baseSources[path] {
			return nil, &repository.Stale{Message: "landing baseline differs from review: " + path + "; synchronize and capture again"}
		}
		file := SelectedFile{Path: path, Baseline: identity, Change: WriteFile}
		fileMode := mode
		for _, hunk := range groups[path] {
			if hunk.BeforeMode == "" && hunk.AfterMode == "" {
				continue
			}
			beforeMode := mode
			if identity == "absent" {
				beforeMode = "absent"
			}
			if len(groups[path]) != 1 || hunk.BeforeMode != beforeMode || hunk.Start != 0 || hunk.End != len(repository.Lines(string(baseline.Data))) || hunk.Before != string(baseline.Data) {
				return nil, fmt.Errorf("file change requires the complete reviewed file: %s", path)
			}
			fileMode = hunk.AfterMode
		}
		if fileMode == "absent" {
			if r.Inputs[path] != nil || groups[path][0].After != "" {
				return nil, fmt.Errorf("invalid file deletion: %s", path)
			}
			file.Change = DeleteFile
			result.Files = append(result.Files, file)
			continue
		}
		lines := repository.Lines(string(baseline.Data))
		var content strings.Builder
		pos := 0
		for _, h := range groups[path] {
			if h.Start < pos || h.End > len(lines) || strings.Join(lines[h.Start:h.End], "") != h.Before {
				return nil, fmt.Errorf("invalid selected source span")
			}
			content.WriteString(strings.Join(lines[pos:h.Start], ""))
			content.WriteString(h.After)
			pos = h.End

		}
		content.WriteString(strings.Join(lines[pos:], ""))
		data[path] = []byte(content.String())
		file.Content = &FileContent{Mode: fileMode, SHA256: repository.Digest(data[path])}
		result.Files = append(result.Files, file)
	}
	if err := r.Validate(repo, config); err != nil {
		return nil, err
	}
	destination, err := repo.OutputDirectory(dir)
	if err != nil {
		return nil, err
	}
	for i := range result.Files {
		file := &result.Files[i]
		if file.Change == DeleteFile {
			continue
		}
		artifact := filepath.Join(destination, fmt.Sprintf("%04d.source", i))
		if err := os.WriteFile(artifact, data[file.Path], 0600); err != nil {
			return nil, err
		}
		file.Content.Artifact = artifact
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}
