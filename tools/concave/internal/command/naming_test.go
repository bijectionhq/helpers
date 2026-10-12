package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concave/internal/command"
	"concave/internal/naming"
)

func TestNamingCLIReadsCapturedSourceWithoutGit(t *testing.T) {
	root := t.TempDir()
	vocab := "| Word | Meaning | Not |\n| --- | --- | --- |\n| wait | x | attempt |\n"
	p := naming.Policy{Vocabulary: "wiki/vocabulary.md", Scopes: []naming.Scope{{ID: "docs", Paths: []string{"wiki/"}, Surface: "documentation"}}, Rules: []naming.Rule{{ID: "naming/terminology", Scopes: []string{"docs"}, Roles: []string{"term"}, Predicate: "canonical_term"}}}
	policyBody, _ := json.Marshal(map[string]any{"format": 2, "naming": p})
	os.Mkdir(filepath.Join(root, "wiki"), 0700)
	os.WriteFile(filepath.Join(root, "concave.json"), policyBody, 0600)
	os.WriteFile(filepath.Join(root, "wiki/vocabulary.md"), []byte(vocab), 0600)
	os.WriteFile(filepath.Join(root, "wiki/topic.md"), []byte("## Attempt\n"), 0600)
	input := naming.Input{Format: 1, Root: root, SourceID: strings.Repeat("a", 64), Target: "wiki", Rules: []string{"naming/terminology"}, PolicySHA256: naming.Digest(policyBody), Files: map[string]string{"concave.json": naming.Digest(policyBody), "wiki/vocabulary.md": naming.Digest([]byte(vocab)), "wiki/topic.md": naming.Digest([]byte("## Attempt\n"))}}
	body, _ := json.Marshal(input)
	path := filepath.Join(t.TempDir(), "input.json")
	os.WriteFile(path, body, 0600)
	var out, stderr bytes.Buffer
	code := command.Run(context.Background(), []string{"naming", "--input", path}, &out, &stderr)
	var report naming.Report
	if code != 1 || json.Unmarshal(out.Bytes(), &report) != nil || report.State != "failed" || len(report.Findings) != 1 {
		t.Fatal(code, out.String(), stderr.String())
	}
	input.Files["wiki/topic.md"] = strings.Repeat("f", 64)
	body, _ = json.Marshal(input)
	os.WriteFile(path, body, 0600)
	out.Reset()
	stderr.Reset()
	code = command.Run(context.Background(), []string{"naming", "--input", path}, &out, &stderr)
	if code != 2 || json.Unmarshal(out.Bytes(), &report) != nil || report.State != "unavailable" {
		t.Fatal(code, out.String(), stderr.String())
	}
}
