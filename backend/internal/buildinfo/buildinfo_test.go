package buildinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveBackendProvenanceUsesLinkerOverrides(t *testing.T) {
	actual := resolveBackendProvenance(
		"v1.4.2",
		"0123456789abcdef",
		"true",
		"2026-07-12T08:30:00Z",
		vcsProvenance{
			version:   "v0.9.0",
			gitCommit: "old-commit",
			gitDirty:  "false",
		},
	)

	assert.Equal(t, BackendProvenance{
		Version:   "v1.4.2",
		GitCommit: "0123456789abcdef",
		GitDirty:  true,
		BuildTime: "2026-07-12T08:30:00Z",
	}, actual)
}

func TestResolveBackendProvenanceFallsBackToVCSMetadata(t *testing.T) {
	actual := resolveBackendProvenance("", "", "", "", vcsProvenance{
		version:   "v1.2.0",
		gitCommit: "abcdef",
		gitDirty:  "true",
	})

	assert.Equal(t, BackendProvenance{
		Version:   "v1.2.0",
		GitCommit: "abcdef",
		GitDirty:  true,
		BuildTime: "unknown",
	}, actual)
}

func TestResolveBackendProvenanceUsesStableDevelopmentDefaults(t *testing.T) {
	actual := resolveBackendProvenance("", "", "", "", vcsProvenance{
		version: "(devel)",
	})

	assert.Equal(t, BackendProvenance{
		Version:   "dev",
		GitCommit: "unknown",
		GitDirty:  false,
		BuildTime: "unknown",
	}, actual)
}
