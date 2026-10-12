package analysis

import (
	"fmt"
	"sort"

	"concave/internal/policy"
	"concave/internal/repository"
)

// HistoricalBlock exposes the same reviewed deletion history used by enforcement.
// Comparisons are diagnostic: even exact bytes require the ordinary classification
// checks, including source location, references, exclusive ownership and policy.
// Source is a range in SourceRevision; Changes use offsets within the two blocks.
type HistoricalBlock struct {
	ID             string               `json:"id"`
	Pairing        string               `json:"pairing,omitempty"`
	Rule           string               `json:"rule"`
	Path           string               `json:"path"`
	RemovedBy      string               `json:"removed_by"`
	SourceRevision string               `json:"source_revision"`
	Source         policy.Span          `json:"source"`
	Upstream       *policy.Span         `json:"upstream,omitempty"`
	HistoricalText string               `json:"historical_text"`
	Status         string               `json:"status"`
	Selected       *HistoricalSelection `json:"selected,omitempty"`
	Changes        []repository.Hunk    `json:"changes,omitempty"`
}

type HistoricalSelection struct {
	Hunk string      `json:"hunk"`
	Span policy.Span `json:"span"`
	Text string      `json:"text"`
}

type HistoryPair struct {
	Block string `json:"block"`
	Hunk  string `json:"hunk"`
}

func normalizeHistoryPairs(o *Options) error {
	if len(o.HistoryPairs) == 0 {
		return nil
	}
	if !o.History {
		return fmt.Errorf("historical pairs require --history")
	}
	if len(o.HistoryPairs) > 128 {
		return fmt.Errorf("at most 128 historical pairs are supported")
	}
	o.HistoryPairs = append([]HistoryPair{}, o.HistoryPairs...)
	sort.Slice(o.HistoryPairs, func(i, j int) bool { return o.HistoryPairs[i].Block < o.HistoryPairs[j].Block })
	blocks, hunks := map[string]bool{}, map[string]bool{}
	for _, pair := range o.HistoryPairs {
		if pair.Block == "" || pair.Hunk == "" {
			return fmt.Errorf("historical pairs require block-id=hunk-id")
		}
		if blocks[pair.Block] || hunks[pair.Hunk] {
			return fmt.Errorf("historical pairs must select each block and hunk at most once")
		}
		blocks[pair.Block], hunks[pair.Hunk] = true, true
	}
	return nil
}

// Automatic pairing requires the exact projected upstream span. Explicit pairs
// allow inspection of moved or adapted blocks without changing enforcement.
// Neither diagnostic path authorizes a restoration or a recovery write.
func compareHistory(r *repository.Repo, blocks []HistoricalBlock, contents map[string]sourcePair, diffs map[string][]repository.Hunk, explicit []HistoryPair) error {
	sort.SliceStable(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Source.Start < b.Source.Start
	})
	requested := map[string]string{}
	for _, pair := range explicit {
		requested[pair.Block] = pair.Hunk
	}
	pairs := map[string]repository.SourcePair{}
	indices := map[string]int{}
	identified := map[string][]repository.Hunk{}
	for i := range blocks {
		b := &blocks[i]
		id, err := repository.JSONDigest(struct {
			Rule, Path, RemovedBy, SourceRevision string
			Source                                policy.Span
			Text                                  string
		}{b.Rule, b.Path, b.RemovedBy, b.SourceRevision, b.Source, b.HistoricalText})
		if err != nil {
			return err
		}
		b.ID = id[:20]
		wanted := requested[b.ID]
		delete(requested, b.ID)
		if b.Upstream == nil && wanted == "" {
			continue
		}
		d := contents[b.Path]
		if !repository.Text(d.a) || !repository.Text(d.b) {
			if wanted != "" {
				return fmt.Errorf("historical pair %s requires current text sources", b.ID)
			}
			continue
		}
		hs, ok := identified[b.Path]
		if !ok {
			hs = repository.Identify(b.Path, d.a, d.b, diffs[b.Path])
			identified[b.Path] = hs
		}
		var selected *repository.Hunk
		for j := range hs {
			h := &hs[j]
			if wanted != "" && h.ID == wanted || wanted == "" && b.Upstream != nil && h.Start == b.Upstream.Start && h.End == b.Upstream.End {
				if selected != nil {
					selected = nil
					break
				}
				selected = h
			}
		}
		if selected == nil {
			if wanted != "" {
				return fmt.Errorf("historical pair %s: hunk %s is not in the current inspection of %s", b.ID, wanted, b.Path)
			}
			continue
		}
		b.Pairing = "projected_span"
		if wanted != "" {
			b.Pairing = "explicit_selection"
		}
		b.Selected = &HistoricalSelection{Hunk: selected.ID, Span: policy.Span{Start: selected.CurrentStart, End: selected.CurrentEnd}, Text: selected.After}
		b.Status = "adapted"
		if b.HistoricalText == selected.After {
			b.Status = "exact_bytes"
		}
		key := fmt.Sprintf("history/%08d", i)
		pairs[key] = repository.SourcePair{Before: []byte(b.HistoricalText), After: []byte(selected.After)}
		indices[key] = i
	}
	if len(requested) != 0 {
		ids := make([]string, 0, len(requested))
		for id := range requested {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return fmt.Errorf("historical block %s is not in the current inspection", ids[0])
	}
	changes, err := r.DiffSources(pairs)
	if err != nil {
		return err
	}
	for key, hs := range changes {
		b := &blocks[indices[key]]
		for i := range hs {
			hs[i].Path = b.Path
		}
		b.Changes = hs
	}
	return nil
}
