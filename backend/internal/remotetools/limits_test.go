package remotetools_test

// One limits knob must reach the read tools.
//
// Limits has two ceilings that mean the same thing to an operator: MaxBytesPerCall
// (documented, and the one aicli expands from a target block) and CaptureLimit (the
// pre-fold ceiling read tools actually consult, and which wins when both are set).
// Deriving CaptureLimit from MaxBytesPerCall is what keeps the documented knob
// honest. These tests pin that derivation, because a default that pre-fills
// CaptureLimit silently defeats every value the operator sets.

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestNormalizeDerivesCaptureLimitFromMaxBytesPerCall(t *testing.T) {
	cfg := remotetools.DefaultConfig()
	// Normalize validates that the workspace exists, because a local backend would
	// otherwise accept a path it can never serve.
	cfg.Workspace = t.TempDir()
	cfg.Limits.MaxBytesPerCall = 4096

	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if cfg.Limits.CaptureLimit != 4096 {
		t.Fatalf("capture limit = %d, want the operator's maxBytesPerCall to reach read tools", cfg.Limits.CaptureLimit)
	}
}

func TestNormalizeKeepsExplicitCaptureLimit(t *testing.T) {
	cfg := remotetools.DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Limits.MaxBytesPerCall = 4096
	cfg.Limits.CaptureLimit = 8192

	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// CaptureLimit supersedes for read tools when it is set deliberately; deriving
	// must not overwrite an explicit choice.
	if cfg.Limits.CaptureLimit != 8192 {
		t.Fatalf("capture limit = %d, want the explicit value preserved", cfg.Limits.CaptureLimit)
	}
}

func TestDefaultConfigLeavesCaptureLimitUnset(t *testing.T) {
	cfg := remotetools.DefaultConfig()
	if cfg.Limits.CaptureLimit != 0 {
		t.Fatalf("capture limit = %d, want 0 in the defaults so the derivation in Normalize can fire", cfg.Limits.CaptureLimit)
	}
	if cfg.Limits.MaxBytesPerCall <= 0 {
		t.Fatalf("max bytes per call = %d, want a working default", cfg.Limits.MaxBytesPerCall)
	}
}
