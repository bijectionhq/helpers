package analysis

import (
	"reflect"

	"concave/internal/contracts"
	"concave/internal/policy"
	"concave/internal/repository"
)

func invariantMap(report *Report) map[string]contracts.Invariant {
	result := map[string]contracts.Invariant{}
	if report.Contracts != nil {
		for _, obligation := range report.Contracts.Definitions {
			if obligation.Invariant != nil {
				inv := *obligation.Invariant
				result[inv.Reference()] = inv
			}
		}
		for _, file := range report.Contracts.Files {
			for _, inv := range file.Invariants {
				result[inv.Reference()] = inv
			}
			for _, obligation := range file.Obligations {
				if obligation.Invariant != nil {
					result[obligation.Invariant.Reference()] = *obligation.Invariant
				}
			}
		}
	}
	return result
}
func contractChanges(before, after *Report) []ContractChange {
	old, next := invariantMap(before), invariantMap(after)
	changes := []ContractChange{}
	documents := func(report *Report) map[string]Obligation {
		refs := map[string]Obligation{}
		if report.Contracts != nil {
			for ref, obligation := range report.Contracts.Definitions {
				if obligation.Invariant == nil {
					refs[ref] = obligation
				}
			}
			for _, file := range report.Contracts.Files {
				for _, obligation := range file.Obligations {
					if obligation.Invariant == nil && obligation.Text != "" {
						refs[obligation.Reference] = obligation
					}
				}
			}
		}
		return refs
	}
	oldDocs, newDocs := documents(before), documents(after)
	for ref, a := range oldDocs {
		if b, ok := newDocs[ref]; !ok {
			changes = append(changes, ContractChange{"obligation_removed", a.Path, ref, a, nil})
		} else if a.Text != b.Text {
			changes = append(changes, ContractChange{"obligation_changed", b.Path, ref, a, b})
		}
	}
	for ref, b := range newDocs {
		if _, ok := oldDocs[ref]; !ok {
			changes = append(changes, ContractChange{"obligation_added", b.Path, ref, nil, b})
		}
	}
	witnesses := func(report *Report) map[string]*repository.State {
		result := map[string]*repository.State{}
		if report.Contracts == nil {
			return result
		}
		all := append([]Witness{}, report.Contracts.Witnesses...)
		for _, file := range report.Contracts.Files {
			all = append(all, file.Witnesses...)
		}
		for _, witness := range all {
			result[witness.Path] = report.Contracts.Inputs[witness.Path]
		}
		return result
	}
	oldWitnesses, newWitnesses := witnesses(before), witnesses(after)
	for path, a := range oldWitnesses {
		if b, ok := newWitnesses[path]; ok && !repository.Same(a, b) {
			changes = append(changes, ContractChange{"witness_source_changed", path, "", a, b})
		}
	}
	removed, added := map[string]contracts.Invariant{}, map[string]contracts.Invariant{}
	for ref, a := range old {
		b, ok := next[ref]
		if !ok {
			removed[ref] = a
			continue
		}
		if a.Prose != b.Prose {
			changes = append(changes, ContractChange{"obligation_changed", b.Path, ref, a, b})
		}
		if a.Declaration.Identity != b.Declaration.Identity {
			changes = append(changes, ContractChange{"obligation_reattached", b.Path, ref, a, b})
		}
	}
	for ref, b := range next {
		if _, ok := old[ref]; !ok {
			added[ref] = b
		}
	}
	// A move is recognized only when its stable ID and prose identify one unique
	// old/new pair. Ambiguous cases remain explicit removals and additions.
	for ref, a := range removed {
		candidates := []string{}
		oldCount := 0
		for _, v := range removed {
			if a.ID == v.ID && a.Prose == v.Prose {
				oldCount++
			}
		}
		for target, b := range added {
			if a.ID == b.ID && a.Prose == b.Prose {
				candidates = append(candidates, target)
			}
		}
		if len(candidates) == 1 && oldCount == 1 {
			b := added[candidates[0]]
			changes = append(changes, ContractChange{"obligation_moved", b.Path, b.Reference(), a, b})
			delete(added, candidates[0])
			delete(removed, ref)
		}
	}
	for ref, a := range removed {
		changes = append(changes, ContractChange{"obligation_removed", a.Path, ref, a, nil})
	}
	for ref, b := range added {
		changes = append(changes, ContractChange{"obligation_added", b.Path, ref, nil, b})
	}
	oldRows := map[string]Row{}
	for _, row := range before.Files {
		oldRows[row.Path] = row
	}
	newPaths := map[string]bool{}
	for _, row := range after.Files {
		newPaths[row.Path] = true
		old := oldRows[row.Path]
		a, b := "", ""
		if old.Selected != nil {
			a = old.Selected.OID
		}
		if row.Selected != nil {
			b = row.Selected.OID
		}
		if a != b {
			changes = append(changes, ContractChange{"source_changed", row.Path, "", old.Selected, row.Selected})
		}
	}
	for _, row := range before.Files {
		if !newPaths[row.Path] && row.Selected != nil {
			changes = append(changes, ContractChange{"source_changed", row.Path, "", row.Selected, nil})
		}
	}
	return changes
}
func policyContractChanges(old, next *policy.Config, sources []ReviewSource) ([]ContractChange, int) {
	changes := []ContractChange{}
	paths := map[string]bool{}
	for _, source := range sources {
		paths[source.Path] = true
	}
	excluded := 0
	// Either side may bring an exception into scope, including a move away
	// from a selected file. Policy bytes and hashes still bind every exception.
	appendException := func(change ContractChange, previousPath string) {
		if paths[change.Path] || paths[previousPath] {
			changes = append(changes, change)
		} else {
			excluded++
		}
	}
	cases := map[string]bool{}
	for name := range old.Cases {
		cases[name] = true
	}
	for name := range next.Cases {
		cases[name] = true
	}
	for name := range cases {
		a, b := old.Cases[name].Witnesses, next.Cases[name].Witnesses
		if !reflect.DeepEqual(a, b) {
			changes = append(changes, ContractChange{"witnesses_changed", policy.File, "case:" + name, a, b})
		}
	}
	a, b := map[string]policy.Exception{}, map[string]policy.Exception{}
	for _, x := range old.Exceptions {
		a[x.ID] = x
	}
	for _, x := range next.Exceptions {
		b[x.ID] = x
	}
	for id, x := range b {
		previous, ok := a[id]
		if !ok || x.Path != previous.Path || x.Start < previous.Start || x.End > previous.End || x.File != nil && previous.File == nil {
			appendException(ContractChange{"exception_coverage_expanded", x.Path, id, previous, x}, previous.Path)
		}
		if ok && !reflect.DeepEqual(previous, x) {
			appendException(ContractChange{"exception_binding_changed", x.Path, id, previous, x}, previous.Path)
		}
	}
	for id, x := range a {
		if _, ok := b[id]; !ok {
			appendException(ContractChange{"exception_removed", x.Path, id, x, nil}, x.Path)
		}
	}
	if !reflect.DeepEqual(old.Transforms, next.Transforms) || old.ManifestSHA256 != next.ManifestSHA256 {
		changes = append(changes, ContractChange{"transformations_changed", policy.File, "", old.Transforms, next.Transforms})
	}
	if !reflect.DeepEqual(old.Documents, next.Documents) {
		changes = append(changes, ContractChange{"document_correspondence_changed", policy.File, "", old.Documents, next.Documents})
	}
	broad := func(config *policy.Config) any {
		refs := map[string]any{"repository": config.Contract}
		for _, family := range config.Families {
			refs["family:"+family.ID] = struct{ Paths, Contracts []string }{family.Paths, family.Contracts}
		}
		for name, k := range config.Cases {
			refs["case:"+name] = struct {
				Surfaces  []policy.Surface
				Contracts []string
			}{k.Surfaces, k.Contracts}
		}
		return refs
	}
	if !reflect.DeepEqual(broad(old), broad(next)) {
		changes = append(changes, ContractChange{"obligation_scope_changed", policy.File, "", broad(old), broad(next)})
	}
	return changes, excluded
}

// Discovery includes unrecorded fences as review data without approving them.
// Compare that same context instead of the subset with policy bindings.
func explanationChanges(before, after *Report) []ContractChange {
	type location struct{ path, id string }
	explanations := func(report *Report) map[location]policy.Explanation {
		result := map[location]policy.Explanation{}
		if report.Contracts != nil {
			for _, file := range report.Contracts.Files {
				for _, exception := range file.Exceptions {
					result[location{file.Path, exception.ID}] = exception.Explanation
				}
			}
		}
		return result
	}
	old, next := explanations(before), explanations(after)
	changes := []ContractChange{}
	for location, explanation := range next {
		reference := location.path + "#" + location.id
		previous, ok := old[location]
		if !ok {
			changes = append(changes, ContractChange{"exception_explanation_added", location.path, reference, nil, explanation})
			continue
		}
		if !reflect.DeepEqual(previous.Witnesses, explanation.Witnesses) {
			changes = append(changes, ContractChange{"witnesses_changed", location.path, reference, previous.Witnesses, explanation.Witnesses})
		}
		if !reflect.DeepEqual(previous.Preserves, explanation.Preserves) {
			changes = append(changes, ContractChange{"preserved_obligations_changed", location.path, reference, previous.Preserves, explanation.Preserves})
		}
		if previous.Why != explanation.Why || previous.RemoveWhen != explanation.RemoveWhen {
			changes = append(changes, ContractChange{"exception_explanation_changed", location.path, reference, previous, explanation})
		}
	}
	for location, explanation := range old {
		if _, ok := next[location]; !ok {
			changes = append(changes, ContractChange{"exception_explanation_removed", location.path, location.path + "#" + location.id, explanation, nil})
		}
	}
	return changes
}
