package agentresult

import "strings"

// FromLegacy upgrades a legacy success/output/error tuple into the shared contract.
func FromLegacy(success bool, summary, errorCode, errorMessage string, usage Usage) *Result {
	status := StatusSucceeded
	if !success {
		status = StatusFailed
	}
	summary = strings.TrimSpace(summary)
	errorMessage = strings.TrimSpace(errorMessage)
	if summary == "" {
		summary = errorMessage
	}
	if summary == "" {
		if success {
			summary = "Agent run completed."
		} else {
			summary = "Agent run failed."
		}
	}
	result := &Result{Status: status, Summary: summary, Usage: usage}
	if errorMessage != "" {
		result.Errors = []Error{{Code: strings.TrimSpace(errorCode), Message: errorMessage}}
	}
	return result
}

func (r *Result) Clone() *Result {
	if r == nil {
		return nil
	}
	clone := *r
	clone.Findings = append([]Finding(nil), r.Findings...)
	for index := range clone.Findings {
		clone.Findings[index].EvidenceRefs = append([]string(nil), r.Findings[index].EvidenceRefs...)
	}
	clone.Changes = append([]Change(nil), r.Changes...)
	for index := range clone.Changes {
		clone.Changes[index].ArtifactRefs = append([]string(nil), r.Changes[index].ArtifactRefs...)
		clone.Changes[index].EvidenceRefs = append([]string(nil), r.Changes[index].EvidenceRefs...)
	}
	clone.Artifacts = append([]Artifact(nil), r.Artifacts...)
	clone.Evidence = append([]Evidence(nil), r.Evidence...)
	clone.RemainingWork = append([]string(nil), r.RemainingWork...)
	clone.Errors = append([]Error(nil), r.Errors...)
	for index := range clone.Errors {
		clone.Errors[index].EvidenceRefs = append([]string(nil), r.Errors[index].EvidenceRefs...)
	}
	clone.ExecutionEventRefs = append([]string(nil), r.ExecutionEventRefs...)
	return &clone
}

func MergeEvidence(result *Result, refs ...string) {
	if result == nil {
		return
	}
	seen := make(map[string]bool, len(result.Evidence)+len(refs))
	for _, evidence := range result.Evidence {
		if ref := strings.TrimSpace(evidence.Ref); ref != "" {
			seen[ref] = true
		}
	}
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		result.Evidence = append(result.Evidence, Evidence{Ref: ref, Kind: evidenceKind(ref)})
	}
}

func evidenceKind(ref string) string {
	switch {
	case strings.HasPrefix(ref, "artifact:") || strings.HasPrefix(ref, "art_"):
		return "artifact"
	case strings.HasPrefix(ref, "event:") || strings.HasPrefix(ref, "evt_"):
		return "execution_event"
	case strings.HasPrefix(ref, "file:"):
		return "file"
	case strings.HasPrefix(ref, "command:"):
		return "command"
	case strings.HasPrefix(ref, "test:"):
		return "test"
	default:
		return "reference"
	}
}
