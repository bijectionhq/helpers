package analysis

import (
	"encoding/json"

	"concave/internal/policy"
)

// ExceptionContext retains the binding fields and explains their captured source.
// Rationale availability is independent of binding validity and witness execution.
type ExceptionContext struct {
	policy.Exception
	Explanation            *policy.Explanation `json:"explanation,omitempty"`
	ExplanationUnavailable string              `json:"explanation_unavailable,omitempty"`
}

// Preserve the policy serializer's distinction between span and file bindings.
// Embedding Exception otherwise promotes its marshaler and drops the rationale.
func (c ExceptionContext) MarshalJSON() ([]byte, error) {
	binding, err := json.Marshal(c.Exception)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(binding, &fields); err != nil {
		return nil, err
	}
	if c.Explanation != nil {
		fields["explanation"], err = json.Marshal(c.Explanation)
	} else if c.ExplanationUnavailable != "" {
		fields["explanation_unavailable"], err = json.Marshal(c.ExplanationUnavailable)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// Resolve only from the review's captured versions. A consumer is candidate
// source; upstream and working files outside the packet cannot supply rationale.
func attachExceptionContext(changes []ContractChange, sources []ReviewSource, blobs map[string]string, sessions ...*policy.Session) {
	parse := policy.Annotations
	if len(sessions) > 0 {
		parse = sessions[0].Annotations
	}
	versions := map[sourceVersion]ReviewSource{}
	for _, source := range sources {
		version := source.Version
		if version == "consumer" {
			version = "candidate"
		}
		if version == "baseline" || version == "candidate" {
			versions[sourceVersion{version, source.Path}] = source
		}
	}
	type sourceBody struct{ path, digest string }
	type parsedSource struct {
		annotations map[string]policy.Annotation
		invalid     bool
	}
	parsed := map[sourceBody]parsedSource{}
	context := func(version string, binding policy.Exception) any {
		if binding.ID == "" {
			return nil
		}
		result := ExceptionContext{Exception: binding}
		if !binding.Fence {
			explanation := binding.Explanation()
			result.Explanation = &explanation
			return result
		}
		source, ok := versions[sourceVersion{version, binding.Path}]
		if !ok {
			result.ExplanationUnavailable = "source not captured in this review"
			return result
		}
		if source.Unavailable != "" {
			result.ExplanationUnavailable = source.Unavailable
			return result
		}
		body, ok := blobs[source.SHA256]
		if !ok {
			result.ExplanationUnavailable = "captured source body unavailable"
			return result
		}
		key := sourceBody{binding.Path, source.SHA256}
		annotations, ok := parsed[key]
		if !ok {
			found, findings := parse(binding.Path, body)
			annotations = parsedSource{found, len(findings) != 0}
			parsed[key] = annotations
		}
		if annotations.invalid {
			result.ExplanationUnavailable = "captured source annotations are invalid"
		} else if annotation, ok := annotations.annotations[binding.ID]; ok {
			result.Explanation = &annotation.Explanation
		} else {
			result.ExplanationUnavailable = "named fence absent from captured source"
		}
		return result
	}
	for i := range changes {
		if binding, ok := changes[i].Before.(policy.Exception); ok {
			changes[i].Before = context("baseline", binding)
		}
		if binding, ok := changes[i].After.(policy.Exception); ok {
			changes[i].After = context("candidate", binding)
		}
	}
}
