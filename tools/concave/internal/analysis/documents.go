package analysis

import (
	"strings"

	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/repository"
)

// Document identity digests the bytes or absence of every input that can change
// this document's parse and resolution, and nothing else.
type Document struct {
	Path         string   `json:"path"`
	UpstreamPath string   `json:"upstream_path,omitempty"`
	Dependencies []string `json:"dependencies"`
	Identity     string   `json:"identity"`
}

// Document imports bind source-backed snippets and partials to the same snapshot
// as their consuming page. Inspection never evaluates MDX expressions or builds.
func inspectDocuments(repo *repository.Repo, session *policy.Session, report *Report, read func(string) ([]byte, error)) error {
	config := session.Config
	if config.Documents == nil {
		return nil
	}
	if config.Documents.UpstreamRoot != "" {
		originals, err := repo.Tree(config.Upstream)
		if err != nil {
			return err
		}
		mapped := map[string]bool{}
		for _, m := range config.Documents.Correspondences {
			mapped[m.UpstreamPath] = true
		}
		for _, row := range report.Files {
			p := row.Path
			if originals[p].OID != "" && strings.HasPrefix(p, config.Documents.UpstreamRoot) && strings.HasSuffix(p, ".mdx") && !mapped[p] {
				partial := false
				for _, part := range strings.Split(p, "/") {
					partial = partial || strings.HasPrefix(part, "_")
				}
				if !partial {
					report.Findings = append(report.Findings, policy.Finding{Kind: "missing_document_correspondence", Path: p, Message: "inherited guide has no explicit publishing correspondence"})
				}
			}
		}
	}
	engine := session.Documents
	sources := []document.Source{}
	for _, row := range report.Files {
		if row.Selected == nil || !document.Supports(row.Path) || !repository.Matches(row.Path, config.Documents.Paths) {
			continue
		}
		data, err := read(row.Path)
		if err != nil {
			return err
		}
		sources = append(sources, document.Source{Path: row.Path, Source: string(data)})
	}
	if len(sources) == 0 {
		return nil
	}
	graph, err := engine.Dependencies(sources, read)
	if err != nil {
		return err
	}
	report.DocumentEngine = document.Identity()
	identities := map[string]string{}
	identity := func(path string) (string, error) {
		if value, ok := identities[path]; ok {
			return value, nil
		}
		parts := []string{}
		for _, input := range graph.Inputs[path] {
			data, err := read(input)
			if repository.IsStale(err) {
				return "", err
			}
			state := repository.Digest(data)
			if err != nil {
				state = "unreadable: " + err.Error()
			}
			parts = append(parts, input, state)
		}
		identities[path] = policy.Subject(parts...)
		return identities[path], nil
	}
	for _, problem := range graph.Problems {
		basis, err := identity(problem.Path)
		if err != nil {
			return err
		}
		report.Findings = append(report.Findings, policy.Finding{Kind: problem.Kind, Path: problem.Path, Subject: policy.Subject(basis, problem.Import), Message: problem.Message})
	}
	for _, source := range sources {
		basis, err := identity(source.Path)
		if err != nil {
			return err
		}
		item := Document{Path: source.Path, Dependencies: graph.Dependencies[source.Path], Identity: basis}
		if original := config.UpstreamPath(source.Path); original != source.Path {
			item.UpstreamPath = original
		}
		report.Documents = append(report.Documents, item)
	}
	return nil
}
