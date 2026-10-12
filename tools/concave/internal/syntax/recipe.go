package syntax

import (
	"encoding/json"
	"fmt"
)

// Validate closes the native subset before handing it to ast-grep, whose serde
// parser can ignore unknown fields. Concave alone owns files, IDs and severity.
func (r Recipe) Validate() error {
	if r.Fix == nil {
		return fmt.Errorf("structural recipe requires fix")
	}
	if err := validateRule(r.Rule, false, 0); err != nil {
		return err
	}
	if len(r.Constraints) > 0 {
		constraints, err := object(r.Constraints)
		if err != nil {
			return err
		}
		for _, rule := range constraints {
			if err := validateRule(rule, false, 0); err != nil {
				return err
			}
		}
	}
	if len(r.Transform) > 0 {
		transforms, err := object(r.Transform)
		if err != nil {
			return err
		}
		for _, transform := range transforms {
			ops, err := object(transform)
			if err != nil {
				return err
			}
			if len(ops) != 1 || ops["replace"] == nil {
				return fmt.Errorf("structural transform supports only replace")
			}
			fields, err := object(ops["replace"])
			if err != nil {
				return err
			}
			if len(fields) != 3 {
				return fmt.Errorf("replace requires source, replace and by")
			}
			for _, key := range []string{"source", "replace", "by"} {
				if !jsonString(fields[key]) {
					return fmt.Errorf("replace requires string %s", key)
				}
			}
		}
	}
	return nil
}
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("expected structural object: %w", err)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("expected nonempty structural object")
	}
	return fields, nil
}
func jsonString(raw json.RawMessage) bool {
	var s string
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil
}
func validateRule(raw json.RawMessage, relational bool, depth int) error {
	if depth > 32 {
		return fmt.Errorf("structural rule nesting exceeds 32")
	}
	fields, err := object(raw)
	if err != nil {
		return err
	}
	matcher := false
	for key, value := range fields {
		switch key {
		case "kind", "regex":
			if !jsonString(value) {
				return fmt.Errorf("%s requires a string", key)
			}
			matcher = true
		case "pattern":
			if !jsonString(value) {
				pattern, err := object(value)
				if err != nil {
					return err
				}
				for k, v := range pattern {
					if (k != "context" && k != "selector" && k != "strictness") || !jsonString(v) {
						return fmt.Errorf("unsupported pattern field %s", k)
					}
				}
				if pattern["context"] == nil {
					return fmt.Errorf("pattern requires context")
				}
			}
			matcher = true
		case "inside", "has", "precedes", "follows":
			if err := validateRule(value, true, depth+1); err != nil {
				return err
			}
			matcher = true
		case "not":
			if err := validateRule(value, false, depth+1); err != nil {
				return err
			}
			matcher = true
		case "all", "any":
			var rules []json.RawMessage
			if json.Unmarshal(value, &rules) != nil || len(rules) == 0 {
				return fmt.Errorf("%s requires nonempty rules", key)
			}
			for _, rule := range rules {
				if err := validateRule(rule, false, depth+1); err != nil {
					return err
				}
			}
			matcher = true
		case "field":
			if !relational || !jsonString(value) {
				return fmt.Errorf("field requires a relational rule and string")
			}
		case "stopBy":
			if !relational {
				return fmt.Errorf("stopBy requires a relational rule")
			}
			if jsonString(value) {
				var stop string
				_ = json.Unmarshal(value, &stop)
				if stop != "neighbor" && stop != "end" {
					return fmt.Errorf("unsupported stopBy %s", stop)
				}
			} else if err := validateRule(value, false, depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported structural rule field %s", key)
		}
	}
	if !matcher {
		return fmt.Errorf("structural rule requires a matcher")
	}
	return nil
}
