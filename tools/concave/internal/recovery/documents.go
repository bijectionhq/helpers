package recovery

import (
	"fmt"
	"strings"

	"concave/internal/analysis"
	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/repository"
)

// PrepareDocument restores source imports in native Docusaurus MDX. Converted
// public pages first require an explicit renderer/source migration; inserting a
// raw-loader import into a Mintlify page would break its current publishing owner.
func PrepareDocument(r *repository.Repo, c *policy.Config, report *analysis.Report, raw []byte, file, reason string) (*Plan, error) {
	if !document.Native(file) {
		return nil, fmt.Errorf("snippet recovery requires native npm-packages/docs/docs/*.mdx; converted publishing pages require renderer migration first")
	}
	var row *analysis.Row
	for i := range report.Files {
		if report.Files[i].Path == file {
			row = &report.Files[i]
			break
		}
	}
	if row == nil || row.Upstream == nil {
		return nil, fmt.Errorf("inspect the inherited document first")
	}
	state, current, err := r.Snapshot(file)
	if err != nil {
		return nil, err
	}
	if !repository.Same(state, row.Selected) {
		return nil, stale("document source changed")
	}
	upstream, err := r.Blob(row.Upstream.OID)
	if err != nil {
		return nil, err
	}
	inputs := repository.Inputs{}
	sources := map[string]string{}
	read := func(file string) error {
		state, body, err := r.Snapshot(file)
		if err != nil {
			return err
		}
		if state == nil || state.Kind != "file" || !repository.Text(body) {
			return fmt.Errorf("missing regular recovery input: %s", file)
		}
		inputs[file] = state
		sources[file] = string(body)
		return nil
	}
	for _, file := range document.RecoveryInputs() {
		if err = read(file); err != nil {
			return nil, err
		}
	}
	engine := document.New(r.Context, r.Root)
	facts, err := engine.Parse(file, string(upstream))
	if err != nil {
		return nil, err
	}
	if facts.Error != "" {
		return nil, fmt.Errorf("invalid upstream MDX: %s", facts.Error)
	}
	for _, imported := range facts.Imports {
		spec := strings.TrimPrefix(imported.Specifier, "!!raw-loader!")
		if spec == imported.Specifier {
			continue
		}
		source, local, err := document.ImportPath(c.UpstreamPath(file), imported.Specifier)
		if err != nil {
			return nil, err
		}
		if !local {
			return nil, fmt.Errorf("snippet must resolve to captured repository source: %s", imported.Specifier)
		}
		if err = read(source); err != nil {
			return nil, err
		}
	}
	proposal, err := engine.Recover(file, c.UpstreamPath(file), string(upstream), string(current), sources)
	if err != nil {
		return nil, err
	}
	if err = inputs.Verify(r, repository.SourceInputs); err != nil {
		return nil, err
	}
	if proposal.Content == string(current) {
		return nil, fmt.Errorf("no unambiguous source-backed recovery: %+v", proposal.Records)
	}
	plan, err := Prepare(r, c, report, raw, nil, SinglePatch(file, current, []byte(proposal.Content)), reason)
	if err != nil {
		return nil, err
	}
	if plan.DocumentInputs == nil {
		plan.DocumentInputs = repository.Inputs{}
	}
	for file, state := range inputs {
		plan.DocumentInputs[file] = state
	}
	plan.DocumentEngine = document.Identity()
	plan.Examples = proposal.Records
	return plan, nil
}
