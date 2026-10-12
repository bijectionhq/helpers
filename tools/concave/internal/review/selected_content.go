package review

import (
	"fmt"
	"path/filepath"
	"strings"

	"concave/internal/repository"
)

type FileChange string

const (
	WriteFile  FileChange = "write"
	DeleteFile FileChange = "delete"
)

type FileContent struct {
	Artifact string `json:"artifact"`
	Mode     string `json:"mode"`
	SHA256   string `json:"sha256"`
}

type SelectedFile struct {
	Path     string       `json:"path"`
	Baseline string       `json:"baseline"`
	Change   FileChange   `json:"change"`
	Content  *FileContent `json:"content,omitempty"`
}

// SelectedContent has one canonical, path-ordered entry per selected file.
// A write of empty bytes remains distinct from an explicit deletion.
type SelectedContent struct {
	Files []SelectedFile `json:"files"`
}

func (c *SelectedContent) Paths() []string {
	paths := make([]string, len(c.Files))
	for i, file := range c.Files {
		paths[i] = file.Path
	}
	return paths
}

func (c *SelectedContent) Validate() error {
	if c == nil || len(c.Files) == 0 {
		return fmt.Errorf("selected content requires files")
	}
	previous := ""
	for _, file := range c.Files {
		if !repository.ValidPath(file.Path) || file.Path <= previous {
			return fmt.Errorf("selected paths must be valid, unique and sorted: %q", file.Path)
		}
		previous = file.Path
		if file.Baseline != "absent" {
			mode, oid, ok := strings.Cut(file.Baseline, ":")
			if !ok || (mode != "100644" && mode != "100755") || (len(oid) != 40 && len(oid) != 64) || strings.Trim(oid, "0123456789abcdef") != "" {
				return fmt.Errorf("invalid selected baseline: %s", file.Path)
			}
		}
		switch file.Change {
		case DeleteFile:
			if file.Content != nil || file.Baseline == "absent" {
				return fmt.Errorf("deletion requires an existing baseline and no content: %s", file.Path)
			}
		case WriteFile:
			content := file.Content
			if content == nil || !filepath.IsAbs(content.Artifact) || (content.Mode != "100644" && content.Mode != "100755") || len(content.SHA256) != 64 || strings.Trim(content.SHA256, "0123456789abcdef") != "" {
				return fmt.Errorf("invalid selected write content: %s", file.Path)
			}
		default:
			return fmt.Errorf("unknown selected file change %q: %s", file.Change, file.Path)
		}
	}
	return nil
}
