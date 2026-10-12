package analysis

import (
	"bytes"
	"concave/internal/coverage"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"concave/internal/contracts"
	"concave/internal/document"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/syntax"
)

// contractResolver owns obligation discovery for one snapshot and policy.
// Inspection, lexical candidates and typed callers share these semantics.
// Its parser cache is invocation-local. Resolve is provisional: inspection and
// review verify their snapshot once after all context has been collected.
type contractResolver struct {
	snapshot   *repository.Snapshot
	session    *policy.Session
	refs       *contracts.Resolver
	inputs     repository.Inputs
	inherited  map[string]repository.Entry
	exceptions map[string]map[string]policy.Explanation
}

type contractSource struct {
	Path         string
	State        *repository.State
	Inherited    bool
	Explanations map[string]policy.Explanation
}

type contractOptions struct {
	Case     string
	Context  bool
	Location *Location
}

func newContractResolver(snapshot *repository.Snapshot, session *policy.Session) (*contractResolver, error) {
	config := session.Config
	inherited, err := config.UpstreamTree(snapshot.Repo)
	if err != nil {
		return nil, err
	}
	resolver := &contractResolver{snapshot: snapshot, session: session, inherited: inherited,
		inputs:     repository.Inputs{},
		exceptions: map[string]map[string]policy.Explanation{}}
	resolver.refs = contracts.New(func(path string) ([]byte, error) {
		data, err := snapshot.Read(path)
		if state, observed := snapshot.Inputs[path]; observed {
			resolver.inputs[path] = state
		}
		return data, err
	}, session.Syntax)
	for _, exception := range config.Exceptions {
		if resolver.exceptions[exception.Path] == nil {
			resolver.exceptions[exception.Path] = map[string]policy.Explanation{}
		}
		resolver.exceptions[exception.Path][exception.ID] = exception.Explanation()
	}
	return resolver, nil
}

// Source refers to bytes already observed in this snapshot, never a synthetic inspection.
func (resolver *contractResolver) Source(path string) contractSource {
	_, inherited := resolver.inherited[path]
	return contractSource{path, resolver.snapshot.Inputs[path], inherited, resolver.exceptions[path]}
}

func (resolver *contractResolver) Resolve(sources []contractSource, options contractOptions) (*ContractContext, []policy.Finding, error) {
	snapshot, config, refs := resolver.snapshot, resolver.session.Config, resolver.refs
	findings := []policy.Finding{}
	var err error
	context := &ContractContext{Engine: "ast-grep " + syntax.Version, Inputs: resolver.inputs, Location: options.Location, Files: []FileContracts{}, Limits: []string{
		"Obligations apply by syntax containment and explicit policy references; no inferred complete call graph or cross-file type inheritance.",
		"References and syntax resolution are not executed tests. Behavioral compliance requires review.",
	}}
	addFinding := func(path string, err error) {
		finding := policy.Finding{Kind: "invalid_contract", Rule: "contract/valid", Path: path, Message: err.Error()}
		var located *syntax.SourceError
		if errors.As(err, &located) {
			finding.Path, finding.Line, finding.EndLine = located.Path, located.Line, located.Line
			// The located declaration, not the error wording, names the obligation.
			if data, readErr := refs.Read(located.Path); readErr == nil {
				if lines := repository.Lines(string(data)); located.Line > 0 && located.Line <= len(lines) {
					finding.Subject = policy.Subject(strings.TrimSpace(lines[located.Line-1]))
				}
			}
			if err == located {
				finding.Message = located.Message
			}
		}
		findings = append(findings, finding)
	}
	total := 0
	if options.Case != "" {
		for _, w := range config.Cases[options.Case].Witnesses {
			path, symbol, e := policy.WitnessTarget("", w)
			if e != nil {
				return nil, nil, e
			}
			resolution := "file_found"
			if options.Context {
				resolution, e = refs.Witness(path, symbol)
			} else {
				_, e = refs.Read(path)
			}
			if e != nil {
				if syntax.IsUnavailable(e) || repository.IsStale(e) {
					return nil, nil, e
				}
				addFinding(path, e)
			}
			context.Witnesses = append(context.Witnesses, Witness{Reference: w, Path: path, Symbol: symbol, Resolution: resolution})
		}
	}
	for _, row := range sources {
		if row.State == nil || row.State.Kind != "file" && row.State.Kind != "blob" {
			continue
		}
		path := row.Path
		relevant := syntax.Language(path) != "" || policy.SupportsAnnotations(path)
		var data []byte
		if relevant || options.Location != nil {
			data, err = refs.Read(path)
			if err != nil {
				if repository.IsStale(err) {
					return nil, nil, err
				}
				if options.Context || options.Location != nil {
					addFinding(path, err)
				}
				continue
			}
			if row.State.OID != "" && snapshot.Inputs[path].OID != row.State.OID {
				return nil, nil, &repository.Stale{Message: "input changed between parity and contract inspection: " + path}
			}
		}
		// Native files have no upstream reconstruction to validate their fences.
		// Still reject misspelled directives and misleading exception annotations.
		if !row.Inherited && relevant && bytes.Contains(data, []byte("concave:")) {
			annotations, annotationFindings := resolver.session.Annotations(path, string(data))
			findings = append(findings, annotationFindings...)
			for _, annotation := range annotations {
				findings = append(findings, policy.Finding{
					Kind: "invalid_fence", Rule: "exception/valid", Path: path,
					Line: annotation.Start + 1, EndLine: annotation.End, Exception: annotation.ID,
					Message: "fork-owned source has no upstream span; use concave:invariant for behavioral obligations",
				})
			}
		}
		doc := &contracts.Document{Invariants: []contracts.Invariant{}}
		if relevant && !document.Supports(path) && bytes.Contains(data, []byte("concave:invariant")) {
			doc, err = refs.Document(path)
			if err != nil {
				if syntax.IsUnavailable(err) || repository.IsStale(err) {
					return nil, nil, err
				}
				addFinding(path, err)
				continue
			}
		}
		if !options.Context && options.Location == nil {
			continue
		}
		file := FileContracts{Path: path, Invariants: doc.Invariants, Obligations: []Obligation{}, Exceptions: []ScopedException{}, Witnesses: []Witness{}}
		if options.Location != nil && path == options.Location.Path {
			lines := repository.Lines(string(data))
			loc := options.Location
			if loc.Line > len(lines) || loc.Column > len(strings.TrimRight(lines[loc.Line-1], "\r\n"))+1 {
				return nil, nil, fmt.Errorf("location outside %s", path)
			}
			file.Invariants = doc.Applicable(loc.Line, loc.Column)
		}
		for _, inv := range file.Invariants {
			i := inv
			file.Obligations = append(file.Obligations, Obligation{i.Reference(), "declaration", path, i.Line, i.Prose, &i})
		}
		links := scopeLinks(config, path, options.Case)
		annotations, _ := resolver.session.Annotations(path, string(data))
		explanations := map[string]policy.Explanation{}
		for id, ex := range row.Explanations {
			explanations[id] = ex
		}
		// Unrecorded fences also need useful discovery, without gaining permission.
		for id, a := range annotations {
			explanations[id] = a.Explanation
		}
		ids := make([]string, 0, len(explanations))
		for id := range explanations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ex := explanations[id]
			a, fenced := annotations[id]
			if options.Location != nil && fenced && (options.Location.Line < a.Start+1 || options.Location.Line > a.End) {
				continue
			}
			start, end := 0, 0
			if fenced {
				start, end = a.Start+1, a.End
			}
			file.Exceptions = append(file.Exceptions, ScopedException{id, start, end, ex})
			for _, ref := range ex.Preserves {
				links = append(links, obligationLink{ref, "preserves:" + id})
			}
			for _, w := range ex.Witnesses {
				p, s, err := policy.WitnessTarget(path, w)
				if err != nil {
					addFinding(path, err)
					continue
				}
				resolution, err := refs.Witness(p, s)
				if err != nil {
					if syntax.IsUnavailable(err) || repository.IsStale(err) {
						return nil, nil, err
					}
					addFinding(path, err)
				}
				witness := Witness{Reference: w, Path: p, Symbol: s, Resolution: resolution}
				if fenced {
					witness.Coverage = "not_run"
					if source, err := refs.Read(p); err == nil {
						request := coverage.Request{Fence: id, Path: path, Source: data, CodeStart: a.CodeStart, CodeEnd: a.CodeEnd, Witness: w, WitnessPath: p, Symbol: s, WitnessSource: source}
						if receipt := coverage.Load(snapshot.Repo.Root, request); receipt != nil {
							witness.Coverage, witness.ObservedExecution = receipt.Outcome, receipt.Outcome == coverage.Executed
							witness.Evidence = "historical"
						}
						witness.Mutation = "not_run"
						for _, binding := range config.Exceptions {
							if binding.ID == id && binding.Path == path && binding.Fence {
								if receipt := coverage.LoadMutation(snapshot.Repo.Root, request, coverage.BindingAnchor(binding.UpstreamSHA256, binding.Start, binding.End)); receipt != nil {
									witness.Mutation = receipt.Outcome
									// A witness that fails without the fenced code ran it.
									witness.ObservedExecution = witness.ObservedExecution || receipt.Outcome == coverage.Witnessed
									witness.Evidence = "historical"
								}
							}
						}
					}
				}
				file.Witnesses = append(file.Witnesses, witness)
			}
		}
		seen := map[string]bool{}
		for _, link := range links {
			key := link.scope + "\x00" + link.ref
			if seen[key] {
				continue
			}
			seen[key] = true
			obligation, err := ResolveObligation(link.ref, link.scope, path, refs)
			if err != nil {
				if syntax.IsUnavailable(err) || repository.IsStale(err) {
					return nil, nil, err
				}
				addFinding(path, err)
				continue
			}
			total += len(obligation.Text)
			if total > repository.MaxOutput {
				return nil, nil, fmt.Errorf("contract context exceeds 128 MiB; narrow scope")
			}
			file.Obligations = append(file.Obligations, obligation)
		}
		context.Files = append(context.Files, file)
	}
	// The same repository, family and case requirements apply in enforcement.
	if !options.Context && options.Location == nil {
		seen := map[string]bool{}
		for _, row := range sources {
			for _, link := range scopeLinks(config, row.Path, options.Case) {
				if seen[link.ref] {
					continue
				}
				seen[link.ref] = true
				if _, err := ResolveObligation(link.ref, link.scope, row.Path, refs); err != nil {
					if syntax.IsUnavailable(err) || repository.IsStale(err) {
						return nil, nil, err
					}
					addFinding(row.Path, err)
				}
			}
		}
	}
	context.Inputs = maps.Clone(resolver.inputs)
	context.SnapshotSHA256, err = repository.JSONDigest(struct {
		Revision string
		Inputs   repository.Inputs
	}{snapshot.Revision, context.Inputs})
	if err != nil {
		return nil, nil, err
	}
	context.Definitions = map[string]Obligation{}
	for i := range context.Files {
		for j := range context.Files[i].Obligations {
			ob := &context.Files[i].Obligations[j]
			if ob.Invariant != nil {
				ob.Reference = ob.Invariant.Reference()
			}
			definition := *ob
			definition.Scope = ""
			context.Definitions[ob.Reference] = definition
			ob.Text = ""
			ob.Invariant = nil
		}
	}
	if options.Context || options.Location != nil {
		paths, references := []string{}, []string{}
		addWitness := func(witness Witness) {
			reference := witness.Path
			if witness.Symbol != "" {
				reference += "::" + witness.Symbol
			}
			references = append(references, reference)
		}
		for _, witness := range context.Witnesses {
			addWitness(witness)
		}
		for _, file := range context.Files {
			paths = append(paths, file.Path)
			for _, witness := range file.Witnesses {
				addWitness(witness)
			}
		}
		requirements := config.RequiredChecks(paths, references)
		context.Verification = &requirements
		unmapped := map[string]bool{}
		for _, reference := range requirements.Unmapped {
			unmapped[reference] = true
		}
		// One line per explanation and reference: a rule that explains many
		// files cites its witnesses once.
		summary, counted := &WitnessSummary{}, map[string]bool{}
		count := func(owner, path, symbol string) {
			reference := path
			if symbol != "" {
				reference += "::" + symbol
			}
			if counted[owner+"\x00"+reference] {
				return
			}
			counted[owner+"\x00"+reference] = true
			summary.Total++
			switch {
			case !unmapped[reference]:
				summary.Mapped++
			case symbol != "":
				summary.UnmappedWitnesses++
			default:
				summary.SourceReferences++
			}
		}
		for _, witness := range context.Witnesses {
			count("case:"+options.Case, witness.Path, witness.Symbol)
		}
		for _, file := range context.Files {
			for _, exception := range file.Exceptions {
				for _, w := range exception.Explanation.Witnesses {
					if path, symbol, err := policy.WitnessTarget(file.Path, w); err == nil {
						count(exception.ID, path, symbol)
					}
				}
			}
		}
		context.WitnessSummary = summary
	}
	return context, findings, nil
}
