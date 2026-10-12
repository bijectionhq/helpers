package analysis

import (
	"encoding/json"
	"fmt"
	"sort"

	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/repository"
	"concave/internal/surface"
	"concave/internal/syntax"
)

type ContractChange struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	Reference string `json:"reference,omitempty"`
	Before    any    `json:"before"`
	After     any    `json:"after"`
}
type ConsumerCandidate struct {
	Invariant    string                `json:"invariant"`
	Path         string                `json:"path"`
	Line         int                   `json:"line"`
	Text         string                `json:"text"`
	Obligations  []contracts.Invariant `json:"obligations"`
	Scopes       []Obligation          `json:"scopes"`
	Relationship string                `json:"relationship"`
	Declaration  string                `json:"declaration"`
}
type SearchCoverage struct {
	ScannedFiles      int      `json:"scanned_files"`
	UnscannedFiles    int      `json:"unscanned_files"`
	ScannedBytes      int      `json:"scanned_bytes"`
	OmittedReferences int      `json:"omitted_references"`
	Limits            []string `json:"limits"`
}
type ContractReview struct {
	CallerContext            *ContractContext      `json:"caller_context,omitempty"`
	CallerLocations          []CallerLocation      `json:"caller_locations,omitempty"`
	CallerCoverage           *CallerCoverage       `json:"caller_coverage,omitempty"`
	CallerFindings           []policy.Finding      `json:"caller_findings,omitempty"`
	ImportConsumers          *surface.Report       `json:"import_consumers,omitempty"`
	Format                   int                   `json:"format"`
	Kind                     string                `json:"kind"`
	Status                   string                `json:"status"`
	Against                  string                `json:"against"`
	Candidate                *Report               `json:"candidate"`
	Baseline                 *Report               `json:"baseline"`
	Changes                  []ContractChange      `json:"changes"`
	ExcludedExceptionChanges int                   `json:"excluded_exception_changes"`
	Sources                  []ReviewSource        `json:"sources"`
	Blobs                    map[string]string     `json:"blobs"`
	Consumers                []ConsumerCandidate   `json:"consumer_candidates"`
	ConsumerDefinitions      map[string]Obligation `json:"consumer_definitions"`
	Search                   SearchCoverage        `json:"consumer_search"`
	SnapshotSHA256           string                `json:"snapshot_sha256"`
	Instructions             []string              `json:"review_instructions"`
	BehavioralParity         string                `json:"behavioral_parity"`
}

// seal finalizes deterministic evidence after the owner has verified live inputs.
func (packet *ContractReview) seal(candidateDigest, baselineDigest string, config, previousConfig *policy.Config) error {
	sort.Slice(packet.Changes, func(i, j int) bool {
		a, b := packet.Changes[i], packet.Changes[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Reference != b.Reference {
			return a.Reference < b.Reference
		}
		return a.Kind < b.Kind
	})
	packet.SnapshotSHA256 = repository.Digest([]byte(candidateDigest + baselineDigest + packet.Candidate.SnapshotSHA256 + packet.Baseline.SnapshotSHA256 + packet.Candidate.Upstream + config.SHA256 + previousConfig.SHA256 + syntax.Version))
	if packet.ImportConsumers != nil {
		binding, err := repository.JSONDigest(struct {
			Import    string
			Context   string
			Locations []CallerLocation
			Coverage  *CallerCoverage
			Findings  []policy.Finding
		}{packet.ImportConsumers.SnapshotSHA256, packet.CallerContext.SnapshotSHA256, packet.CallerLocations, packet.CallerCoverage, packet.CallerFindings})
		if err != nil {
			return err
		}
		packet.SnapshotSHA256 = repository.Digest([]byte(packet.SnapshotSHA256 + binding))
	}
	serialized, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	if len(serialized) > repository.MaxOutput {
		return fmt.Errorf("review packet exceeds 128 MiB; narrow scope")
	}
	return nil
}
