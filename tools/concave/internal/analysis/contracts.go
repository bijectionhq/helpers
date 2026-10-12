package analysis

import (
	"fmt"
	"strconv"
	"strings"

	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/repository"
)

type Location struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column,omitempty"`
}

func ParseLocation(arg string) (*Location, error) {
	path, line, column := arg, "", ""
	parts := strings.Split(arg, ":")
	if len(parts) == 1 {
		return nil, nil
	}
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("expected file:line[:column]")
	}
	path, line = parts[0], parts[1]
	if len(parts) == 3 {
		column = parts[2]
	}
	n, err := strconv.Atoi(line)
	if !repository.ValidPath(path) || err != nil || n < 1 {
		return nil, fmt.Errorf("invalid source location %q", arg)
	}
	c := 0
	if len(parts) == 3 {
		c, err = strconv.Atoi(column)
		if err != nil || c < 1 {
			return nil, fmt.Errorf("invalid source column")
		}
	}
	return &Location{path, n, c}, nil
}

type Obligation struct {
	Reference string               `json:"reference"`
	Scope     string               `json:"scope"`
	Path      string               `json:"path"`
	Line      int                  `json:"line"`
	Text      string               `json:"text,omitempty"`
	Invariant *contracts.Invariant `json:"invariant,omitempty"`
}
type Witness struct {
	Reference  string `json:"reference"`
	Path       string `json:"path"`
	Symbol     string `json:"symbol,omitempty"`
	Resolution string `json:"resolution"`
	// Inspection never establishes current execution evidence. Witness receipts
	// describe a historical run of matching fence and witness bytes; dependencies
	// and toolchains may since have changed. Candidate verification owns freshness.
	Executed          bool   `json:"executed"`
	ObservedExecution bool   `json:"observed_execution,omitempty"`
	Evidence          string `json:"evidence,omitempty"`
	// Coverage is that receipt's outcome, or not_run.
	Coverage string `json:"coverage,omitempty"`
	// Mutation is the outcome of running the witness with the fence replaced
	// by what it explains away (concave verify witness --mutate), or not_run.
	// Only witnessed shows the witness detects the fenced divergence.
	Mutation string `json:"mutation,omitempty"`
}
type ScopedException struct {
	ID          string             `json:"id"`
	Start       int                `json:"line"`
	End         int                `json:"end_line"`
	Explanation policy.Explanation `json:"explanation"`
}
type FileContracts struct {
	Path        string                `json:"path"`
	Invariants  []contracts.Invariant `json:"invariants"`
	Obligations []Obligation          `json:"obligations"`
	Exceptions  []ScopedException     `json:"exceptions"`
	Witnesses   []Witness             `json:"witnesses"`
}

// WitnessSummary counts the witness lines of this scope's divergence
// explanations by what stands behind each one. Mapped lines name cases a
// declared check runs; an unmapped witness names a symbol or test no check
// runs; a source reference only points at a file. None is an execution result.
type WitnessSummary struct {
	Total             int `json:"total"`
	Mapped            int `json:"mapped"`
	UnmappedWitnesses int `json:"unmapped_witnesses"`
	SourceReferences  int `json:"source_references"`
}

type ContractContext struct {
	Verification   *policy.Requirements         `json:"verification,omitempty"`
	WitnessSummary *WitnessSummary              `json:"witness_summary,omitempty"`
	Engine         string                       `json:"engine"`
	SnapshotSHA256 string                       `json:"snapshot_sha256"`
	Inputs         map[string]*repository.State `json:"inputs"`
	Location       *Location                    `json:"location,omitempty"`
	Files          []FileContracts              `json:"files"`
	Definitions    map[string]Obligation        `json:"definitions"`
	Witnesses      []Witness                    `json:"witnesses"`
	Limits         []string                     `json:"limits"`
}

type obligationLink struct{ ref, scope string }

func scopeLinks(config *policy.Config, path, caseName string) []obligationLink {
	links := []obligationLink{}
	if config.Contract != "" {
		links = append(links, obligationLink{config.Contract, "repository"})
	}
	// Family obligations are simultaneous even where inventory chooses one label.
	for _, family := range config.Families {
		if repository.Matches(path, family.Paths) {
			for _, ref := range family.Contracts {
				links = append(links, obligationLink{ref, "family:" + family.ID})
			}
		}
	}
	if caseName != "" {
		for _, ref := range config.Cases[caseName].Contracts {
			links = append(links, obligationLink{ref, "case:" + caseName})
		}
	}
	return links
}

// ResolveObligation resolves only colocated source invariants. Wiki prose is
// independently maintained documentation, never a lint obligation or input.
func ResolveObligation(ref, scope, source string, refs *contracts.Resolver) (Obligation, error) {
	inv, err := refs.Resolve(source, ref)
	if err != nil {
		return Obligation{}, err
	}
	return Obligation{ref, scope, inv.Path, inv.Line, inv.Prose, &inv}, nil
}
func enrichContracts(repo *repository.Repo, session *policy.Session, report *Report, options Options) error {
	snapshot, err := repository.OpenSnapshot(repo, options.Rev)
	if err != nil {
		return err
	}
	resolver, err := newContractResolver(snapshot, session)
	if err != nil {
		return err
	}
	sources := make([]contractSource, 0, len(report.Files))
	for _, row := range report.Files {
		sources = append(sources, contractSource{row.Path, row.Selected, row.Upstream != nil, row.Explanations})
	}
	context, findings, err := resolver.Resolve(sources, contractOptions{Case: options.Case, Context: options.Contracts, Location: options.Location})
	if err != nil {
		return err
	}
	if _, err = snapshot.Verify(); err != nil {
		return err
	}
	report.Findings = append(report.Findings, findings...)
	report.ContractInputsSHA256 = context.SnapshotSHA256
	if options.Contracts || options.Location != nil {
		report.Contracts = context
	}
	return nil
}

// Applicable preserves declaration containment and exception-fence scope at a location.
func (file FileContracts) Applicable(location Location) []Obligation {
	declarations := map[string]bool{}
	doc := contracts.Document{Invariants: file.Invariants}
	for _, invariant := range doc.Applicable(location.Line, location.Column) {
		declarations[invariant.Reference()] = true
	}
	result := []Obligation{}
	for _, obligation := range file.Obligations {
		if obligation.Scope == "declaration" && !declarations[obligation.Reference] {
			continue
		}
		if strings.HasPrefix(obligation.Scope, "preserves:") {
			id := strings.TrimPrefix(obligation.Scope, "preserves:")
			inside := false
			for _, exception := range file.Exceptions {
				if exception.ID == id && (exception.Start == 0 || location.Line >= exception.Start && location.Line <= exception.End) {
					inside = true
				}
			}
			if !inside {
				continue
			}
		}
		result = append(result, obligation)
	}
	return result
}
