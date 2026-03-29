package team

import (
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// SessionObservation captures a tool observation surfaced from a teammate session run.
type SessionObservation struct {
	Tool    string                 `json:"tool"`
	Output  interface{}            `json:"output,omitempty"`
	Success bool                   `json:"success"`
	Error   string                 `json:"error,omitempty"`
	Metrics map[string]interface{} `json:"metrics,omitempty"`
}

// SessionObservationsFromRuntime converts runtime observations into the teammate-facing subset.
func SessionObservationsFromRuntime(observations []runtimetypes.Observation) []SessionObservation {
	if len(observations) == 0 {
		return nil
	}
	out := make([]SessionObservation, 0, len(observations))
	for _, observation := range observations {
		cloned := SessionObservation{
			Tool:    strings.TrimSpace(observation.Tool),
			Output:  observation.Output,
			Success: observation.Success,
			Error:   strings.TrimSpace(observation.Error),
		}
		if len(observation.Metrics) > 0 {
			cloned.Metrics = make(map[string]interface{}, len(observation.Metrics))
			for key, value := range observation.Metrics {
				cloned.Metrics[key] = value
			}
		}
		out = append(out, cloned)
	}
	return out
}
