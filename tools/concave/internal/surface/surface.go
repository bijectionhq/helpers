// Package surface bridges the installed TypeScript compiler and esbuild. Go owns
// process bounds, Git inventory, artifact integrity and stale-source validation.
package surface

import (
	"bytes"
	"concave/internal/process"
	"concave/internal/repository"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	Snapshot Mode = "snapshot"
	Callers  Mode = "callers"
)

type Status string

const (
	Captured   Status = "captured"
	Incomplete Status = "incomplete"
)

type Options struct {
	Mode    Mode              `json:"mode"`
	Project string            `json:"project"`
	Entries []string          `json:"entries"`
	Modules map[string]string `json:"modules"`
	Files   []string          `json:"files"`
}
type Signature struct {
	Text         string   `json:"text"`
	Calls        []string `json:"calls"`
	Constructors []string `json:"constructors"`
	Members      []Member `json:"members"`
}
type Member struct {
	Name      string `json:"name"`
	Optional  bool   `json:"optional"`
	Modifiers int    `json:"modifiers"`
	Constant  any    `json:"constant,omitempty"`
	Type      string `json:"type"`
}
type Export struct {
	Name         string     `json:"name"`
	Owner        string     `json:"owner"`
	Declarations []string   `json:"declarations"`
	Value        *Signature `json:"value"`
	Type         *Signature `json:"type"`
}
type Entry struct {
	Exports []Export `json:"exports"`
	Runtime []string `json:"runtime"`
}
type Toolchain struct {
	NodeSHA256 string `json:"node_sha256"`
	Node       string `json:"node"`
	TypeScript string `json:"typescript"`
	ESBuild    string `json:"esbuild"`
}
type Diagnostic struct {
	Code    int    `json:"code"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}
type Binding struct {
	Imported string `json:"imported"`
	Local    string `json:"local"`
}
type Reference struct {
	Column int    `json:"column"`
	Line   int    `json:"line"`
	Name   string `json:"name"`
}
type Consumer struct {
	Compiler           string      `json:"compiler"`
	Entry              string      `json:"entry"`
	Column             int         `json:"column"`
	ReferencesComplete bool        `json:"references_complete"`
	OmittedReferences  int         `json:"omitted_references"`
	Path               string      `json:"path"`
	Line               int         `json:"line"`
	Module             string      `json:"module"`
	Source             string      `json:"source"`
	Kind               string      `json:"kind"`
	Bindings           []Binding   `json:"bindings"`
	References         []Reference `json:"references"`
}
type CoverageIssue struct {
	Compiler string `json:"compiler,omitempty"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
}
type Coverage struct {
	Complete          bool            `json:"complete"`
	ParseErrors       int             `json:"parse_errors"`
	Issues            []CoverageIssue `json:"issues"`
	OmittedIssues     int             `json:"omitted_issues"`
	OmittedReferences int             `json:"omitted_references"`
	ScannedFiles      int             `json:"scanned_files"`
	UnscannedFiles    int             `json:"unscanned_files"`
	UnresolvedDynamic int             `json:"unresolved_dynamic"`
	ReexportLimit     bool            `json:"reexport_limit"`
	Limits            []string        `json:"limits"`
}

// Analysis is the compiler response. Git, status and artifact identity belong
// exclusively to Go; the adapter never manufactures them.
type CompilerScope struct {
	ResolvedCompiler string `json:"resolved_compiler,omitempty"`
	Error            string `json:"error,omitempty"`
	Owner            string `json:"owner"`
	Basis            string `json:"basis"`
	Compiler         string `json:"compiler"`
	TypeScript       string `json:"typescript"`
	Files            int    `json:"files"`
}

type Analysis struct {
	Compilers     []CompilerScope   `json:"compilers"`
	Entries       map[string]Entry  `json:"entries"`
	Consumers     []Consumer        `json:"consumers"`
	Coverage      Coverage          `json:"coverage"`
	Diagnostics   []Diagnostic      `json:"diagnostics"`
	Toolchain     Toolchain         `json:"toolchain"`
	OptionsSHA256 string            `json:"options_sha256"`
	Inputs        repository.Inputs `json:"inputs"`
	Limits        []string          `json:"limits"`
}
type adapterRequest struct {
	Protocol int     `json:"protocol"`
	Options  Options `json:"options"`
}
type adapterResponse struct {
	Protocol int                `json:"protocol"`
	Analysis *Analysis          `json:"analysis,omitempty"`
	Error    *adapterError      `json:"error,omitempty"`
	Timings  map[string]float64 `json:"timings,omitempty"`
}
type adapterError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}
type Report struct {
	// Timings are local observations, excluded from saved artifacts and their identity.
	Timings map[string]time.Duration `json:"-"`
	Format  int                      `json:"format"`
	Kind    string                   `json:"kind"`
	Status  Status                   `json:"status"`
	Head    string                   `json:"head"`
	Analysis
	InventorySHA256 string  `json:"inventory_sha256"`
	SnapshotSHA256  string  `json:"snapshot_sha256"`
	AdapterSHA256   string  `json:"adapter_sha256"`
	Scope           Options `json:"scope"`
}

func sourceFile(p string) bool {
	switch filepath.Ext(p) {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
		return true
	}
	return false
}
func inventory(repo *repository.Repo) ([]string, error) {
	paths, err := repo.Inventory()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(paths, func(path string) bool { return !sourceFile(path) || strings.Contains("/"+path, "/node_modules/") }), nil
}
func Capture(repo *repository.Repo, options Options) (*Report, error) {
	started := time.Now()
	timings := map[string]time.Duration{}
	if options.Mode != Snapshot && options.Mode != Callers || len(options.Entries) == 0 || len(options.Entries) > 16 {
		return nil, fmt.Errorf("surface capture needs 1..16 entries")
	}
	if len(options.Modules) == 0 {
		options.Modules = nil
	}
	options.Entries = slices.Clone(options.Entries)
	slices.Sort(options.Entries)
	if len(slices.Compact(slices.Clone(options.Entries))) != len(options.Entries) {
		return nil, fmt.Errorf("duplicate entry")
	}
	for module, entry := range options.Modules {
		if options.Mode != Callers || module == "" || strings.ContainsAny(module, "=\n\r\x00") || !slices.Contains(options.Entries, entry) {
			return nil, fmt.Errorf("module %q must map to a selected caller entry", module)
		}
	}
	for _, path := range append(append([]string{}, options.Entries...), options.Project) {
		if path != "" && (!repository.ValidPath(path) || strings.HasSuffix(path, "/")) {
			return nil, fmt.Errorf("unsafe source/project path %q", path)
		}
	}
	head, err := repo.Resolve("HEAD")
	if err != nil {
		return nil, err
	}
	if options.Mode == Callers {
		options.Files, err = inventory(repo)
		if err != nil {
			return nil, err
		}
	}
	request, _ := json.Marshal(adapterRequest{Protocol: 1, Options: options})
	_, nodeSHA, err := process.Executable(repo.Root, "node")
	if err != nil {
		return nil, err
	}
	entry, cleanup, err := materializeAdapter()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	timings["prepare"] = time.Since(started)
	started = time.Now()
	result, err := process.Run(repo.Context, repo.Root, []string{"node", "--max-old-space-size=768", entry}, request, 16<<20)
	if err != nil {
		return nil, fmt.Errorf("TypeScript adapter: %w: %s", err, result.Stderr)
	}
	timings["adapter"] = time.Since(started)
	started = time.Now()
	response, err := decodeResponse(result.Stdout)
	if err != nil {
		return nil, err
	}
	for phase, milliseconds := range response.Timings {
		timings["adapter."+phase] = time.Duration(milliseconds * float64(time.Millisecond))
	}
	report := Report{Format: 4, Kind: "concave-surface", Status: Captured, Analysis: *response.Analysis, Timings: timings}
	timings["decode"] = time.Since(started)
	started = time.Now()
	if options.Mode == Callers {
		report.Kind = "concave-callers"
		report.InventorySHA256, err = repository.JSONDigest(options.Files)
		if err != nil {
			return nil, err
		}
	}
	if len(report.Diagnostics) != 0 || report.Coverage.UnscannedFiles != 0 || report.Coverage.ReexportLimit || report.Coverage.ParseErrors != 0 {
		report.Status = Incomplete
	}
	report.Head = head
	_, currentNodeSHA, err := process.Executable(repo.Root, "node")
	if err != nil {
		return nil, err
	}
	if currentNodeSHA != nodeSHA {
		return nil, &repository.Stale{Message: "Node executable changed during analysis"}
	}
	report.Toolchain.NodeSHA256 = nodeSHA
	report.Scope = options
	report.Scope.Files = nil
	report.AdapterSHA256 = adapterSHA256
	if err = report.verifyInputs(repo); err != nil {
		return nil, err
	}
	if current, e := repo.Resolve("HEAD"); e != nil || current != head {
		return nil, &repository.Stale{Message: "HEAD changed during TypeScript analysis"}
	}
	if options.Mode == Callers {
		current, e := inventory(repo)
		if e != nil {
			return nil, e
		}
		if !slices.Equal(current, options.Files) {
			return nil, &repository.Stale{Message: "caller inventory changed"}
		}
	}
	timings["revalidate"] = time.Since(started)
	report.SnapshotSHA256, err = report.Digest()
	return &report, err
}

// Transport success is distinct from successful analysis. The narrow protocol
// never accepts a partially decoded response or confuses staleness with a finding.
func decodeResponse(data []byte) (*adapterResponse, error) {
	var response adapterResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("compiler protocol: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("compiler protocol: trailing response")
	}
	if response.Protocol != 1 || (response.Analysis == nil) == (response.Error == nil) {
		return nil, fmt.Errorf("invalid compiler response")
	}
	if response.Error != nil {
		if response.Error.Kind == "stale_inputs" {
			return nil, &repository.Stale{Message: response.Error.Message}
		}
		return nil, fmt.Errorf("TypeScript adapter: %s", response.Error.Message)
	}
	if response.Analysis.Entries == nil || response.Analysis.Inputs == nil {
		return nil, fmt.Errorf("incomplete compiler response")
	}
	for phase, milliseconds := range response.Timings {
		if phase == "" || math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds < 0 || milliseconds > float64(time.Hour/time.Millisecond) {
			return nil, fmt.Errorf("invalid compiler timing %q", phase)
		}
	}
	return &response, nil
}

func (report *Report) Digest() (string, error) {
	copy := *report
	copy.SnapshotSHA256 = ""
	return repository.JSONDigest(copy)
}

// ValidateIdentity checks the artifact before consuming it. Live inputs are
// validated once at the acceptance boundary, after observed source agreement.
func ValidateIdentity(report *Report) error {
	digest, err := report.Digest()
	if err != nil || report.Format != 4 || (report.Kind != "concave-callers" && report.Kind != "concave-surface") || report.SnapshotSHA256 != digest {
		return fmt.Errorf("invalid surface artifact")
	}
	if report.AdapterSHA256 != adapterSHA256 {
		return &repository.Stale{Message: "compiler adapter changed; recapture the surface"}
	}
	return nil
}

// ValidateObserved binds contract bytes to the caller evidence even if a source
// changes and changes back before the final live sweep. Unavailable source is
// excluded from context and remains an explicit coverage gap.
func ValidateObserved(report *Report, observed repository.Inputs) error {
	for path, state := range observed {
		if state == nil || state.Kind != "file" {
			continue
		}
		expected, known := report.Inputs[path]
		if known && (expected == nil || expected.Kind != "file" || expected.SHA256 != state.SHA256 || expected.Size != state.Size) {
			return &repository.Stale{Message: "caller and contract sources differ: " + path}
		}
	}
	return nil
}

func Validate(repo *repository.Repo, report *Report) error {
	if err := ValidateIdentity(report); err != nil {
		return err
	}
	_, nodeSHA, err := process.Executable(repo.Root, "node")
	if err != nil {
		return err
	}
	if nodeSHA != report.Toolchain.NodeSHA256 {
		return &repository.Stale{Message: "surface Node toolchain changed"}
	}
	if report.Kind == "concave-callers" {
		current, e := inventory(repo)
		if e != nil {
			return e
		}
		digest, e := repository.JSONDigest(current)
		if e != nil {
			return e
		}
		if digest != report.InventorySHA256 {
			return &repository.Stale{Message: "caller inventory changed"}
		}

	}
	return report.verifyInputs(repo)
}

func (report *Report) verifyInputs(repo *repository.Repo) error {
	if err := report.Inputs.Verify(repo, repository.CompilerInputs); err != nil {
		return err
	}
	for _, scope := range report.Compilers {
		if scope.Error != "" {
			continue
		}
		state := report.Inputs[scope.ResolvedCompiler]
		if state == nil || state.Kind != "file" || scope.Compiler == "" || filepath.IsAbs(scope.Compiler) || !strings.Contains("/"+scope.Compiler, "/node_modules/") || filepath.ToSlash(filepath.Clean(scope.Compiler)) != scope.Compiler {
			return fmt.Errorf("invalid compiler ownership for %s", scope.Owner)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(repo.Root, filepath.FromSlash(scope.Compiler)))
		if err != nil || resolved != filepath.Join(repo.Root, filepath.FromSlash(scope.ResolvedCompiler)) {
			return &repository.Stale{Message: "compiler dependency changed: " + scope.Compiler}
		}
	}
	return nil
}

type Change struct {
	Entry  string `json:"entry"`
	Export string `json:"export"`
	Kind   string `json:"kind"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}
type CallerChange struct {
	Entry  string    `json:"entry"`
	Path   string    `json:"path"`
	Kind   string    `json:"kind"`
	Before *Consumer `json:"before,omitempty"`
	After  *Consumer `json:"after,omitempty"`
}
type CoverageChange struct {
	Before Coverage `json:"before"`
	After  Coverage `json:"after"`
}
type Comparison struct {
	BeforeHead   string          `json:"before_head"`
	AfterHead    string          `json:"after_head"`
	Callers      []CallerChange  `json:"callers,omitempty"`
	Coverage     *CoverageChange `json:"coverage,omitempty"`
	Format       int             `json:"format"`
	Kind         string          `json:"kind"`
	Comparable   bool            `json:"comparable"`
	BasisChanges []string        `json:"basis_changes"`
	Changes      []Change        `json:"changes"`
	Limits       []string        `json:"limits"`
}

func Compare(before, after *Report) (*Comparison, error) {
	for _, report := range []*Report{before, after} {
		digest, err := report.Digest()
		if err != nil || report.Format != 4 || (report.Kind != "concave-surface" && report.Kind != "concave-callers") || report.SnapshotSHA256 != digest {
			return nil, fmt.Errorf("expected intact format-4 surface or caller artifacts")
		}
	}
	if before.Kind != after.Kind {
		return nil, fmt.Errorf("compare artifacts of the same kind")
	}
	comparison := &Comparison{BeforeHead: before.Head, AfterHead: after.Head, Format: 1, Kind: "concave-surface-comparison", Comparable: before.Status == Captured && after.Status == Captured, BasisChanges: []string{}, Changes: []Change{}, Limits: before.Limits}
	for _, p := range []struct {
		name          string
		before, after any
	}{{"toolchain", before.Toolchain, after.Toolchain}, {"compiler_options", before.OptionsSHA256, after.OptionsSHA256}, {"adapter", before.AdapterSHA256, after.AdapterSHA256}, {"entries", before.Scope.Entries, after.Scope.Entries}, {"module_aliases", before.Scope.Modules, after.Scope.Modules}, {"project", before.Scope.Project, after.Scope.Project}} {
		if !reflect.DeepEqual(p.before, p.after) {
			comparison.Comparable = false
			comparison.BasisChanges = append(comparison.BasisChanges, p.name)
		}
	}
	compilerBasis := func(scopes []CompilerScope) []CompilerScope {
		result := slices.Clone(scopes)
		for i := range result {
			result[i].Files = 0
		}
		return result
	}
	if !reflect.DeepEqual(compilerBasis(before.Compilers), compilerBasis(after.Compilers)) {
		comparison.Comparable = false
		comparison.BasisChanges = append(comparison.BasisChanges, "compiler_ownership")
	}
	if before.Status != Captured || after.Status != Captured {
		comparison.BasisChanges = append(comparison.BasisChanges, "incomplete_snapshot")
	}
	if before.Kind == "concave-callers" {
		comparison.Kind = "concave-callers-comparison"
		if !before.Coverage.Complete || !after.Coverage.Complete {
			comparison.Comparable = false
			comparison.BasisChanges = append(comparison.BasisChanges, "coverage_gaps")
		}
		compareCallers(comparison, before, after)
	}
	entries := map[string]bool{}
	for p := range before.Entries {
		entries[p] = true
	}
	for p := range after.Entries {
		entries[p] = true
	}
	for p := range entries {
		old, next := map[string]Export{}, map[string]Export{}
		for _, e := range before.Entries[p].Exports {
			old[e.Name] = e
		}
		for _, e := range after.Entries[p].Exports {
			next[e.Name] = e
		}
		for name, x := range old {
			y, ok := next[name]
			if !ok {
				comparison.Changes = append(comparison.Changes, Change{p, name, "removed", x, nil})
				continue
			}
			if !reflect.DeepEqual(x.Value, y.Value) || !reflect.DeepEqual(x.Type, y.Type) {
				comparison.Changes = append(comparison.Changes, Change{p, name, "signature_changed", x, y})
			}
			if x.Owner != y.Owner || !slices.Equal(x.Declarations, y.Declarations) {
				comparison.Changes = append(comparison.Changes, Change{p, name, "owner_changed", x.Declarations, y.Declarations})
			}
		}
		for name, y := range next {
			if _, ok := old[name]; !ok {
				comparison.Changes = append(comparison.Changes, Change{p, name, "added", nil, y})
			}
		}
		if !slices.Equal(before.Entries[p].Runtime, after.Entries[p].Runtime) {
			comparison.Changes = append(comparison.Changes, Change{p, "", "runtime_exports_changed", before.Entries[p].Runtime, after.Entries[p].Runtime})
		}
	}
	sort.Slice(comparison.Changes, func(i, j int) bool {
		x, y := comparison.Changes[i], comparison.Changes[j]
		if x.Entry != y.Entry {
			return x.Entry < y.Entry
		}
		if x.Export != y.Export {
			return x.Export < y.Export
		}
		return x.Kind < y.Kind
	})
	return comparison, nil
}
