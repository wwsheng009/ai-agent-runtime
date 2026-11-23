package memorystore

import (
	"math"
	"strings"
)

const (
	InjectionCategoryCore      = "core"
	InjectionCategoryTask      = "task"
	InjectionCategoryRetrieved = "retrieved"
	InjectionCategoryProcedure = "procedure"
	InjectionCategoryReference = "reference"
)

// InjectionCategoryBudgets contains fractional hard caps for each memory
// category. When every value is zero, the legacy total-budget allocator is
// used. The unallocated fraction is added to retrieved.
type InjectionCategoryBudgets struct {
	Core      float64
	Task      float64
	Retrieved float64
	Procedure float64
	Reference float64
}

func (b InjectionCategoryBudgets) enabled() bool {
	return b.Core > 0 || b.Task > 0 || b.Retrieved > 0 ||
		b.Procedure > 0 || b.Reference > 0
}

func (b InjectionCategoryBudgets) normalized() InjectionCategoryBudgets {
	values := []*float64{&b.Core, &b.Task, &b.Retrieved, &b.Procedure, &b.Reference}
	sum := 0.0
	for _, value := range values {
		if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
			*value = 0
		}
		if *value > 1 {
			*value = 1
		}
		sum += *value
	}
	if sum > 1 {
		for _, value := range values {
			*value /= sum
		}
	}
	return b
}

func (b InjectionCategoryBudgets) allocation(total int) map[string]int {
	if total < 0 {
		total = 0
	}
	b = b.normalized()
	core := int(float64(total) * b.Core)
	task := int(float64(total) * b.Task)
	procedure := int(float64(total) * b.Procedure)
	reference := int(float64(total) * b.Reference)
	// Rounding residue and any unconfigured ratio go to retrieved. This keeps
	// the allocations exact and guarantees that category caps sum to total.
	retrieved := total - core - task - procedure - reference
	return map[string]int{
		InjectionCategoryCore:      core,
		InjectionCategoryTask:      task,
		InjectionCategoryRetrieved: retrieved,
		InjectionCategoryProcedure: procedure,
		InjectionCategoryReference: reference,
	}
}

func injectionCategoryForRecord(record Record) string {
	switch strings.ToLower(strings.TrimSpace(record.Kind)) {
	case "task", "task_trace", "task_state", "task_outcome":
		return InjectionCategoryTask
	case "procedure":
		return InjectionCategoryProcedure
	case "reference":
		return InjectionCategoryReference
	default:
		return InjectionCategoryRetrieved
	}
}

// prioritizeRecallCandidates keeps the inverse pruning order explicit:
// task is protected first, then retrieved, procedure, and reference. Core is
// already selected before recall. Consequently, when the total budget or
// max-item limit is exhausted, lower-priority categories are dropped first.
func prioritizeRecallCandidates(candidates []recallCandidate, records []Record) []recallCandidate {
	if len(candidates) < 2 {
		return candidates
	}
	order := map[string]int{
		InjectionCategoryTask:      0,
		InjectionCategoryRetrieved: 1,
		InjectionCategoryProcedure: 2,
		InjectionCategoryReference: 3,
	}
	grouped := make([][]recallCandidate, 4)
	unknown := make([]recallCandidate, 0)
	for _, candidate := range candidates {
		if candidate.recordIndex < 0 || candidate.recordIndex >= len(records) {
			unknown = append(unknown, candidate)
			continue
		}
		category := injectionCategoryForRecord(records[candidate.recordIndex])
		index, ok := order[category]
		if !ok {
			unknown = append(unknown, candidate)
			continue
		}
		grouped[index] = append(grouped[index], candidate)
	}
	ordered := make([]recallCandidate, 0, len(candidates))
	for _, group := range grouped {
		ordered = append(ordered, group...)
	}
	return append(ordered, unknown...)
}
