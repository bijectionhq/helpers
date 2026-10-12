package surface

import (
	"encoding/json"
	"reflect"
	"sort"
)

// Pair repeated imports in source order. Positions are evidence, not edge identity;
// moving an unchanged import must not manufacture a removed dependency.
func compareCallers(result *Comparison, before, after *Report) {
	group := func(report *Report) map[string][]Consumer {
		groups := map[string][]Consumer{}
		for _, consumer := range report.Consumers {
			raw, _ := json.Marshal([]string{consumer.Entry, consumer.Path, consumer.Module, consumer.Source, consumer.Kind})
			key := string(raw)
			groups[key] = append(groups[key], consumer)
		}
		for _, consumers := range groups {
			sort.SliceStable(consumers, func(i, j int) bool {
				if consumers[i].Line != consumers[j].Line {
					return consumers[i].Line < consumers[j].Line
				}
				return consumers[i].Column < consumers[j].Column
			})
		}
		return groups
	}
	old, next := group(before), group(after)
	keys := map[string]bool{}
	for key := range old {
		keys[key] = true
	}
	for key := range next {
		keys[key] = true
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		a, b := old[key], next[key]
		for i := 0; i < max(len(a), len(b)); i++ {
			var x, y *Consumer
			if i < len(a) {
				x = &a[i]
			}
			if i < len(b) {
				y = &b[i]
			}
			current := y
			if current == nil {
				current = x
			}
			appendChange := func(kind string) {
				result.Callers = append(result.Callers, CallerChange{current.Entry, current.Path, kind, x, y})
			}
			switch {
			case x == nil:
				appendChange("edge_added")
			case y == nil:
				appendChange("edge_removed")
			default:
				if !reflect.DeepEqual(x.Bindings, y.Bindings) {
					appendChange("bindings_changed")
				}
				if x.Line != y.Line || x.Column != y.Column {
					appendChange("import_location_changed")
				}
				if !reflect.DeepEqual(x.References, y.References) {
					appendChange("references_changed")
				}
				if x.ReferencesComplete != y.ReferencesComplete || x.OmittedReferences != y.OmittedReferences {
					appendChange("reference_coverage_changed")
				}
			}
		}
	}
	if !reflect.DeepEqual(before.Coverage, after.Coverage) {
		result.Coverage = &CoverageChange{before.Coverage, after.Coverage}
	}
}
