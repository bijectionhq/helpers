// Package continuation defines the result of a next-step decision. Domain owners
// choose actions; humans and automation consume the same explicit arguments.
package continuation

type Kind string

const (
	None              Kind = "none"
	Recapture         Kind = "recapture"
	RepairSource      Kind = "repair_source"
	MapWitnesses      Kind = "map_witnesses"
	RepairEnvironment Kind = "repair_environment"
	RepairCheck       Kind = "repair_check"
	Verify            Kind = "verify"
	Publish           Kind = "publish"
	Inspect           Kind = "inspect"
	ExplainRule       Kind = "explain_rule"
)

// Action describes one continuation. Missing arguments stay explicit, so neither
// terminal rendering nor an agent can mistake a placeholder for authorization.
type Action struct {
	Kind     Kind     `json:"kind"`
	Message  string   `json:"message"`
	Argv     []string `json:"argv,omitempty"`
	Requires []string `json:"requires,omitempty"`
}

func (action Action) String() string { return action.Message }

// InRepository binds a relative Concave action without mutating saved evidence.
func (action Action) InRepository(root string) Action {
	if len(action.Argv) > 0 {
		action.Argv = append([]string{"concave", "--repo", root}, action.Argv[1:]...)
	}
	return action
}
