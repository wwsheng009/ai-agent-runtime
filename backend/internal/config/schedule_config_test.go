package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDefaultScheduleConfigEnablesAutoExecution(t *testing.T) {
	cfg := DefaultScheduleConfig()
	require.True(t, cfg.Enabled, "schedule control plane is enabled by default")
	require.Equal(t, ScheduleModeAuto, cfg.Execution.Mode,
		"default execution must allow embedded delivery; delegate with an empty URL cannot work out of the box")
}

func TestScheduleConfigOmittedFieldsKeepAutomaticMode(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("schedule:\n  enabled: true\n"), cfg))
	require.Equal(t, ScheduleModeAuto, cfg.Schedule.Execution.Mode)
}
