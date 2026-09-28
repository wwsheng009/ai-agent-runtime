package chat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSessionActorLoopConfigPropagatesOnProgress pins the actor passthrough for
// P0-1: a configured OnProgress callback must be injected into every run's
// ReAct config, and a nil callback must leave the loop config untouched.
func TestSessionActorLoopConfigPropagatesOnProgress(t *testing.T) {
	var got []string
	actor := &SessionActor{onProgress: func(kind string) {
		got = append(got, kind)
	}}
	cfg := actor.historyCheckpointLoopConfig(nil, nil, nil)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.OnProgress, "OnProgress 必须透传到 run 级 ReAct 配置")
	cfg.OnProgress(context.Background(), "llm_response")
	cfg.OnProgress(context.Background(), "iteration_end")
	require.Equal(t, []string{"llm_response", "iteration_end"}, got)

	plain := &SessionActor{}
	plainCfg := plain.historyCheckpointLoopConfig(nil, nil, nil)
	require.NotNil(t, plainCfg)
	require.Nil(t, plainCfg.OnProgress, "未配置回调时不得注入进度钩子")
}
