package subagentbatch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubjectIDFromObligation(t *testing.T) {
	require.Equal(t, "batch_1", SubjectIDFromObligation("batch_1"))
	require.Equal(t, "child-1", SubjectIDFromObligation(AgentSessionObligationID("child-1")))
	require.Equal(t, "team-1", SubjectIDFromObligation(TeamObligationID("team-1")))
	require.Equal(t, "job-1", SubjectIDFromObligation(BackgroundJobObligationID("job-1")))
	require.Equal(t, "unknown", SubjectIDFromObligation(" unknown "))
}
