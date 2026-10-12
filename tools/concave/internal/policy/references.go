package policy

import (
	"concave/internal/contracts"
	"fmt"
)

// CheckReferences resolves witnesses and preserved source invariants in the
// selected snapshot. A reference is not proof of an executed test.
func CheckReferences(source string, m Explanation, refs *contracts.Resolver) error {
	read := refs.Read
	if err := m.Validate(); err != nil {
		return err
	}
	for _, w := range m.Witnesses {
		path, _, err := WitnessTarget(source, w)
		if err != nil {
			return err
		}
		if _, err = read(path); err != nil {
			return fmt.Errorf("witness %s: %w", w, err)
		}
	}
	for _, ref := range m.Preserves {
		if _, err := refs.Resolve(source, ref); err != nil {
			return err
		}
	}
	return nil
}
