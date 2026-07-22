package background

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestDefaultStartupAcceptanceUsesProcessGate(t *testing.T) {
	startup := normalizeStartupAcceptance(nil)
	require.Equal(t, StartupProbeProcess, startup.Probe)
	require.Equal(t, int(defaultStartupGracePeriod.Milliseconds()), startup.GracePeriodMs)
	require.NoError(t, validateStartupAcceptance(startup))
}

func TestStartupAcceptanceValidationIsGenericAndStrict(t *testing.T) {
	require.Error(t, validateStartupAcceptance(StartupAcceptance{Probe: StartupProbeTCP}))
	require.Error(t, validateStartupAcceptance(StartupAcceptance{Probe: StartupProbeHTTP, URL: "file:///tmp/health"}))
	require.NoError(t, validateStartupAcceptance(StartupAcceptance{Probe: StartupProbeTCP, Address: "127.0.0.1:8080"}))
	require.NoError(t, validateStartupAcceptance(StartupAcceptance{Probe: StartupProbeHTTP, URL: "https://example.test/health"}))
}

func TestStartupAcceptanceRejectsProcessThatExitsDuringGrace(t *testing.T) {
	checks := 0
	err := runStartupProbe(context.Background(), StartupAcceptance{
		Probe:         StartupProbeProcess,
		GracePeriodMs: 10,
		TimeoutMs:     100,
	}, func() bool {
		checks++
		return checks == 1
	})
	require.ErrorContains(t, err, "process exited before startup acceptance")
}

func TestStartupAcceptanceFailureSetsStableStateAndCode(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:       "job-startup-failed",
			Status:   StatusPending,
			Metadata: metadataFromRequest(BackgroundTaskArgs{}, 0),
		},
		request: BackgroundTaskArgs{Startup: &StartupAcceptance{Probe: StartupProbeProcess}},
		output:  newOutputBuffer(1024),
	}

	manager.failStartupAcceptance(managed, errors.New("process exited during grace period"))
	result := decorateTaskOutputResult(TaskOutputResult{}, *managed.snapshot())
	require.Equal(t, StatusFailed, managed.snapshot().Status)
	require.Equal(t, string(runtimeerrors.ErrProcessHealthcheck), result.ErrorCode)
	require.Equal(t, launchStateFailed, result.LaunchState)
	require.Equal(t, healthcheckStateFailed, result.HealthcheckState)
	require.Contains(t, result.HealthcheckError, "grace period")
}

func TestStartupAcceptanceSuccessExposesAcceptedTelemetry(t *testing.T) {
	original := executeStartupProbe
	defer func() { executeStartupProbe = original }()
	executeStartupProbe = func(context.Context, StartupAcceptance, func() bool) error { return nil }

	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		ctx: context.Background(),
		info: Job{
			ID:       "job-startup-accepted",
			Status:   StatusPending,
			Metadata: metadataFromRequest(BackgroundTaskArgs{}, 0),
		},
		request: BackgroundTaskArgs{Startup: &StartupAcceptance{Probe: StartupProbeProcess}},
		output:  newOutputBuffer(1024),
	}

	require.True(t, manager.acceptStartedProcess(context.Background(), managed, time.Now().UTC(), 42, func() bool { return true }))
	result := decorateTaskOutputResult(TaskOutputResult{}, *managed.snapshot())
	require.Equal(t, StatusRunning, managed.snapshot().Status)
	require.True(t, result.ProcessStarted)
	require.Equal(t, launchStateAccepted, result.LaunchState)
	require.Equal(t, healthcheckStatePassed, result.HealthcheckState)
	require.NotEmpty(t, result.StartupAcceptedAt)
}

func TestOrphanJobPersistsHealthcheckErrorCode(t *testing.T) {
	manager := NewManager(Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	managed := &managedJob{
		info:   Job{ID: "job-orphaned", Status: StatusRunning, Metadata: map[string]interface{}{}},
		output: newOutputBuffer(1024),
	}
	manager.orphanJob(managed, "process outcome unavailable")
	result := decorateTaskOutputResult(TaskOutputResult{}, *managed.snapshot())
	require.Equal(t, string(runtimeerrors.ErrProcessHealthcheck), result.ErrorCode)
}
