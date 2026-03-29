package executor

import (
	"context"
	stderrors "errors"
	"sync"
	"testing"
	"time"

	runtimeerrors "github.com/ai-gateway/ai-agent-runtime/internal/errors"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

func TestNewParallelExecutor(t *testing.T) {
	executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		obs := types.NewObservation("execute_node", tool)
		obs.WithOutput("success")
		obs.MarkSuccess()
		return obs, nil
	}

	executor := NewParallelExecutor(3, executorFunc)
	if executor == nil {
		t.Fatal("expected executor, got nil")
	}

	if executor.GetMaxConcurrency() != 3 {
		t.Errorf("expected max concurrency 3, got %d", executor.GetMaxConcurrency())
	}
}

func TestExecuteParallel_Simple(t *testing.T) {
	// 测试简单的并行执行
	ctx := context.Background()

	var calls []string
	var mu sync.Mutex
	executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		mu.Lock()
		calls = append(calls, nodeID)
		mu.Unlock()

		// 模拟执行时间
		time.Sleep(50 * time.Millisecond)

		obs := types.NewObservation("execute_node", tool)
		obs.WithOutput(nodeID)
		obs.MarkSuccess()
		return obs, nil
	}

	executor := NewParallelExecutor(5, executorFunc)

	// 创建简单的 DAG（3个独立节点）
	dag := &DAG{
		Nodes: map[string]*DAGNode{
			"node1": {
				ID:     "node1",
				Tool:   "tool1",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
			"node2": {
				ID:     "node2",
				Tool:   "tool2",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
			"node3": {
				ID:     "node3",
				Tool:   "tool3",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
		},
	}

	startTime := time.Now()
	result, err := executor.ExecuteParallel(ctx, dag)
	elapsed := time.Since(startTime)

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 所有节点应该成功执行
	for _, nodeID := range []string{"node1", "node2", "node3"} {
		if dag.Nodes[nodeID].Status != StatusCompleted {
			t.Errorf("expected node %s to be completed, got status %d", nodeID, dag.Nodes[nodeID].Status)
		}
	}

	// 所有节点应该被调用
	if len(calls) != 3 {
		t.Errorf("expected 3 calls, got %d", len(calls))
	}

	// 并行执行应该比串行快（每个节点50ms，3个节点）
	// 串行: 150ms, 并行: 约50ms
	if elapsed > 100*time.Millisecond {
		t.Logf("warning: parallel execution took %v, expected < 100ms", elapsed)
	}

	// 检查结果
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}

	for _, obs := range result {
		if !obs.Success {
			t.Errorf("expected success, got failed: %s", obs.Error)
		}
	}
}

func TestExecuteParallel_WithDeps(t *testing.T) {
	// 测试依赖关系
	ctx := context.Background()

	var calls []string
	var mu sync.Mutex
	executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		mu.Lock()
		calls = append(calls, nodeID)
		mu.Unlock()

		time.Sleep(50 * time.Millisecond)

		obs := types.NewObservation("execute_node", tool)
		obs.WithOutput(nodeID)
		obs.MarkSuccess()
		return obs, nil
	}

	executor := NewParallelExecutor(5, executorFunc)

	// 创建有依赖关系的 DAG
	// node3 依赖 node1 和 node2
	dag := &DAG{
		Nodes: map[string]*DAGNode{
			"node1": {
				ID:     "node1",
				Tool:   "tool1",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
			"node2": {
				ID:     "node2",
				Tool:   "tool2",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
			"node3": {
				ID:     "node3",
				Tool:   "tool3",
				Args:   map[string]interface{}{},
				Deps:   []string{"node1", "node2"},
				Status: StatusPending,
			},
		},
	}

	result, err := executor.ExecuteParallel(ctx, dag)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// 检查调用顺序：node3 应该在 node1 和 node2 之后
	node3Index := -1
	for i, call := range calls {
		if call == "node3" {
			node3Index = i
		}
	}

	if node3Index == -1 {
		t.Fatal("node3 was not called")
	}

	// node1 和 node2 应该在 node3 之前被调用
	var node1Index, node2Index int
	for i, call := range calls {
		if call == "node1" {
			node1Index = i
		}
		if call == "node2" {
			node2Index = i
		}
	}

	if node1Index > node3Index || node2Index > node3Index {
		t.Error("node3 should be called after node1 and node2")
	}

	// 所有节点应该完成
	for _, nodeID := range []string{"node1", "node2", "node3"} {
		if dag.Nodes[nodeID].Status != StatusCompleted {
			t.Errorf("expected node %s to be completed", nodeID)
		}
	}

	// 应该有3个结果
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
}

func TestExecuteParallel_ContextCancellation(t *testing.T) {
	// 测试上下文取消
	ctx, cancel := context.WithCancel(context.Background())

	executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			obs := types.NewObservation("execute_node", tool)
			obs.MarkFailure(ctx.Err().Error())
			return obs, ctx.Err()
		default:
		}

		// 模拟长时间运行的任务
		time.Sleep(200 * time.Millisecond)

		obs := types.NewObservation("execute_node", tool)
		obs.WithOutput(nodeID)
		obs.MarkSuccess()
		return obs, nil
	}

	executor := NewParallelExecutor(2, executorFunc)

	dag := &DAG{
		Nodes: map[string]*DAGNode{
			"node1": {
				ID:     "node1",
				Tool:   "tool1",
				Args:   map[string]interface{}{},
				Deps:   []string{},
				Status: StatusPending,
			},
		},
	}

	// 短暂等待后取消上下文
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := executor.ExecuteParallel(ctx, dag)
	if err != nil && err != context.Canceled {
		t.Errorf("expected context.Canceled or nil, got %v", err)
	}
}

func TestExecuteParallel_EmptyDAG(t *testing.T) {
	// 测试空 DAG
	ctx := context.Background()
	executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		obs := types.NewObservation("execute_node", tool)
		obs.WithOutput(nodeID)
		obs.MarkSuccess()
		return obs, nil
	}

	executor := NewParallelExecutor(5, executorFunc)

	dag := &DAG{
		Nodes: map[string]*DAGNode{},
	}

	result, err := executor.ExecuteParallel(ctx, dag)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}

func TestExecuteParallel_PreservesFailedObservationAndOriginalError(t *testing.T) {
	expectedErr := runtimeerrors.WrapWithContext(
		runtimeerrors.ErrAgentPermission,
		"sandbox denied workflow step",
		nil,
		map[string]interface{}{"policy": "sandbox"},
	)

	executor := NewParallelExecutor(1, func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
		obs := types.NewObservation(nodeID, tool)
		obs.WithInput(args)
		obs.MarkFailure(expectedErr.Error())
		return obs, expectedErr
	})

	results, err := executor.ExecuteParallel(context.Background(), &DAG{
		Nodes: map[string]*DAGNode{
			"node1": {
				ID:     "node1",
				Tool:   "tool1",
				Args:   map[string]interface{}{"foo": "bar"},
				Deps:   []string{},
				Status: StatusPending,
			},
		},
	})

	if err == nil {
		t.Fatal("expected execution error, got nil")
	}
	var runtimeErr *runtimeerrors.RuntimeError
	if !stderrors.As(err, &runtimeErr) {
		t.Fatalf("expected runtime error, got %T %v", err, err)
	}
	if runtimeErr.Code != runtimeerrors.ErrAgentPermission {
		t.Fatalf("expected AGENT_PERMISSION, got %s", runtimeErr.Code)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(results))
	}
	if results[0].Success {
		t.Fatal("expected failed observation")
	}
}

func TestBuildDAG(t *testing.T) {
	// 测试 DAG 构建
	tests := []struct {
		name        string
		nodes       map[string]*DAGNode
		expectError bool
	}{
		{
			name: "valid DAG",
			nodes: map[string]*DAGNode{
				"A": {ID: "A", Tool: "toolA", Args: map[string]interface{}{}, Deps: []string{}},
				"B": {ID: "B", Tool: "toolB", Args: map[string]interface{}{}, Deps: []string{"A"}},
				"C": {ID: "C", Tool: "toolC", Args: map[string]interface{}{}, Deps: []string{"A", "B"}},
			},
			expectError: false,
		},
		{
			name: "cyclic dependency",
			nodes: map[string]*DAGNode{
				"A": {ID: "A", Tool: "toolA", Args: map[string]interface{}{}, Deps: []string{"B"}},
				"B": {ID: "B", Tool: "toolB", Args: map[string]interface{}{}, Deps: []string{"A"}},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			executorFunc := func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error) {
				obs := types.NewObservation("execute_node", tool)
				obs.WithOutput(nodeID)
				obs.MarkSuccess()
				return obs, nil
			}

			executor := NewParallelExecutor(5, executorFunc)

			dag := &DAG{Nodes: tt.nodes}
			_, err := executor.ExecuteParallel(ctx, dag)

			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("expected no error but got: %v", err)
			}
		})
	}
}
