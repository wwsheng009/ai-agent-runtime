package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTaskOutcomeContractParsesJSONBlock(t *testing.T) {
	outcome, err := ParseTaskOutcomeContract("notes\n```json\n{\"task_status\":\"handoff\",\"summary\":\"pass to reviewer\",\"blocker\":\"need review\",\"handoff_to\":\"mate-2\"}\n```")
	require.NoError(t, err)
	assert.Equal(t, TaskOutcomeHandoff, outcome.Status)
	assert.Equal(t, "pass to reviewer", outcome.Summary)
	assert.Equal(t, "need review", outcome.Blocker)
	assert.Equal(t, "mate-2", outcome.HandoffTo)
}

func TestParseTaskOutcomeContractParsesTaskLines(t *testing.T) {
	outcome, err := ParseTaskOutcomeContract("TASK_STATUS: blocked\nTASK_SUMMARY: waiting on architecture\nTASK_BLOCKER: api review")
	require.NoError(t, err)
	assert.Equal(t, TaskOutcomeBlocked, outcome.Status)
	assert.Equal(t, "waiting on architecture", outcome.Summary)
	assert.Equal(t, "api review", outcome.Blocker)
}

func TestValidateTaskOutcomeContractRejectsInvalidDonePayload(t *testing.T) {
	_, err := ValidateTaskOutcomeContract(TaskOutcomeContract{
		Status:  TaskOutcomeDone,
		Summary: "finished work",
		Blocker: "should not be here",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocker is only allowed")
}

func TestNormalizeTaskOutcomeContractUsesLegacySummaryCompatibility(t *testing.T) {
	outcome, structured, err := NormalizeTaskOutcomeContract(TaskOutcomeBlocked, TaskOutcomeContract{
		Summary: "waiting on review",
	})
	require.NoError(t, err)
	assert.False(t, structured)
	assert.Equal(t, TaskOutcomeBlocked, outcome.Status)
	assert.Equal(t, "waiting on review", outcome.Summary)
	assert.Empty(t, outcome.Blocker)
}

func TestNormalizeTaskOutcomeContractValidatesStructuredPayload(t *testing.T) {
	outcome, structured, err := NormalizeTaskOutcomeContract(TaskOutcomeBlocked, TaskOutcomeContract{
		Status:    TaskOutcomeHandoff,
		Summary:   "pass to reviewer",
		Blocker:   "need review",
		HandoffTo: "mate-2",
	})
	require.NoError(t, err)
	assert.True(t, structured)
	assert.Equal(t, TaskOutcomeHandoff, outcome.Status)
	assert.Equal(t, "mate-2", outcome.HandoffTo)
}

func TestValidateAllowedTaskOutcomeStatusRejectsUnexpectedStatus(t *testing.T) {
	err := ValidateAllowedTaskOutcomeStatus(TaskOutcomeContract{Status: TaskOutcomeFailed}, TaskOutcomeDone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected done")
}

func TestTaskOutcomeContractSchemaIncludesStrictFields(t *testing.T) {
	schema := TaskOutcomeContractSchema()
	require.NotNil(t, schema)

	required, ok := schema["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"task_status", "summary"}, required)

	additionalProperties, ok := schema["additionalProperties"].(bool)
	require.True(t, ok)
	assert.False(t, additionalProperties)

	properties, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, properties, "task_status")
	assert.Contains(t, properties, "summary")
	assert.Contains(t, properties, "blocker")
	assert.Contains(t, properties, "handoff_to")
}

func TestTaskOutcomeContractSchemaForRestrictsAllowedStatuses(t *testing.T) {
	schema := TaskOutcomeContractSchemaFor(TaskOutcomeBlocked, TaskOutcomeHandoff)
	require.NotNil(t, schema)

	properties, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok)
	taskStatus, ok := properties["task_status"].(map[string]interface{})
	require.True(t, ok)
	enumValues, ok := taskStatus["enum"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{string(TaskOutcomeBlocked), string(TaskOutcomeHandoff)}, enumValues)

	description, ok := taskStatus["description"].(string)
	require.True(t, ok)
	assert.Contains(t, description, "blocked or handoff")
}
