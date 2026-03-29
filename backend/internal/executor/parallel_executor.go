package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// ParallelExecutor 并行执行器
// 支持并发执行工具调用，基于 DAG 进行依赖管理
type ParallelExecutor struct {
	maxConcurrency int
	sem            chan struct{}
	nodeExecutor   NodeExecutorFunction
}

// NodeExecutorFunction 节点执行函数类型
type NodeExecutorFunction func(ctx context.Context, nodeID, tool string, args map[string]interface{}, execCtx *NodeExecutorContext) (*types.Observation, error)

// DAG 依赖图（简化版，用于执行）
type DAG struct {
	Nodes map[string]*DAGNode
}

// DAGNode DAG 节点
type DAGNode struct {
	ID     string
	Tool   string
	Args   map[string]interface{}
	Deps   []string // 依赖的节点 ID
	Status NodeStatus
	Error  error
}

// NodeStatus 节点状态
type NodeStatus int

const (
	StatusPending NodeStatus = iota
	StatusRunning
	StatusCompleted
	StatusFailed
)

// ExecutionResult 执行结果
type ExecutionResult struct {
	Success      bool
	Observations []*types.Observation
	Errors       []error
	Duration     time.Duration
}

// NodeExecutorContext 节点执行上下文
type NodeExecutorContext struct {
	DAG              *DAG
	CompletedNodes   map[string]bool
	FailedNodes      map[string]bool
	NodeObservations map[string]*types.Observation
}

// NewParallelExecutor 创建并行执行器
func NewParallelExecutor(maxConcurrency int, nodeExecutor NodeExecutorFunction) *ParallelExecutor {
	return &ParallelExecutor{
		maxConcurrency: maxConcurrency,
		sem:            make(chan struct{}, maxConcurrency),
		nodeExecutor:   nodeExecutor,
	}
}

// ExecuteParallel 执行 DAG
func (e *ParallelExecutor) ExecuteParallel(ctx context.Context, dag *DAG) ([]*types.Observation, error) {
	if dag == nil {
		return nil, fmt.Errorf("dag cannot be nil")
	}

	if len(dag.Nodes) == 0 {
		return []*types.Observation{}, nil
	}

	// 检查循环依赖
	if err := e.detectCycles(dag); err != nil {
		return nil, err
	}

	startTime := time.Now()

	// 结果收集
	var results []*types.Observation
	var resultsMu sync.Mutex

	// 已完成节点
	completedNodes := make(map[string]bool)
	failedNodes := make(map[string]bool)
	nodeObservations := make(map[string]*types.Observation)

	var mu sync.Mutex
	var wg sync.WaitGroup
	executionErrors := make([]error, 0)

	// 处理节点
	for id := range dag.Nodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()

			// 获取信号量（控制并发）
			e.sem <- struct{}{}
			defer func() { <-e.sem }()

			// 等待依赖节点完成
			for {
				mu.Lock()
				depsCompleted := true
				depFailed := false
				for _, depID := range dag.Nodes[nodeID].Deps {
					if failedNodes[depID] {
						depFailed = true
						depsCompleted = false
						break
					}
					if !completedNodes[depID] {
						depsCompleted = false
						break
					}
				}
				if depFailed {
					node := dag.Nodes[nodeID]
					depErr := fmt.Errorf("dependency failed for node %s", nodeID)
					node.Status = StatusFailed
					node.Error = depErr
					failedNodes[nodeID] = true
					observation := types.NewObservation(nodeID, node.Tool)
					observation.WithInput(node.Args)
					observation.MarkFailure(depErr.Error())
					nodeObservations[nodeID] = observation
					resultsMu.Lock()
					results = append(results, observation)
					resultsMu.Unlock()
					executionErrors = append(executionErrors, depErr)
					mu.Unlock()
					return
				}
				mu.Unlock()

				if depsCompleted {
					break
				}

				// 检查是否被取消
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}

			// 获取节点信息
			mu.Lock()
			node := dag.Nodes[nodeID]
			node.Status = StatusRunning
			mu.Unlock()

			// 创建执行上下文
			mu.Lock()
			execCtx := &NodeExecutorContext{
				DAG:              dag,
				CompletedNodes:   completedNodes,
				FailedNodes:      failedNodes,
				NodeObservations: nodeObservations,
			}
			mu.Unlock()

			// 执行节点
			start := time.Now()
			obs, err := e.executeNode(ctx, node, execCtx, e.nodeExecutor)
			_ = time.Since(start) // 执行时间记录到观察结果

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				node.Status = StatusFailed
				node.Error = err
				failedNodes[nodeID] = true
				executionErrors = append(executionErrors, err)
			} else {
				node.Status = StatusCompleted
				completedNodes[nodeID] = true
			}

			if obs != nil {
				nodeObservations[nodeID] = obs
				resultsMu.Lock()
				results = append(results, obs)
				resultsMu.Unlock()

				// 记录执行时间
				if obs.Duration.IsZero() {
					obs.Duration.Start = start
					obs.Duration.StopTimer()
				}
				obs.WithMetric("duration_ms", time.Since(start).Milliseconds())
				obs.WithMetric("max_concurrency", e.maxConcurrency)
			}
		}(id)
	}

	// 等待所有任务完成
	wg.Wait()

	_ = time.Since(startTime) // 记录总执行时间

	if len(executionErrors) > 0 {
		if len(executionErrors) == 1 {
			return results, executionErrors[0]
		}
		return results, errors.Join(executionErrors...)
	}
	if err := ctx.Err(); err != nil {
		return results, err
	}

	return results, nil
}

// executeNode 执行单个节点
func (e *ParallelExecutor) executeNode(ctx context.Context, node *DAGNode, execCtx *NodeExecutorContext, executorFunc NodeExecutorFunction) (*types.Observation, error) {
	obs := types.NewObservation("execute_node", node.Tool)
	obs.WithInput(node.Args)

	// 调用节点执行函数
	obsResult, err := executorFunc(ctx, node.ID, node.Tool, node.Args, execCtx)
	if err != nil {
		obs.MarkFailure(err.Error())
		return obs, err
	}

	if obsResult != nil {
		// 复制观察结果
		obs.Step = obsResult.Step
		obs.Tool = obsResult.Tool
		obs.Output = obsResult.Output
		obs.Success = obsResult.Success
		obs.Error = obsResult.Error
		if obsResult.Metrics != nil {
			if obs.Metrics == nil {
				obs.Metrics = make(map[string]interface{})
			}
			for k, v := range obsResult.Metrics {
				obs.Metrics[k] = v
			}
		}
	}

	obs.MarkSuccess()
	return obs, nil
}

// detectCycles 检测循环依赖
func (e *ParallelExecutor) detectCycles(dag *DAG) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	for nodeID := range dag.Nodes {
		if !visited[nodeID] {
			if e.hasCycle(nodeID, dag, visited, recStack) {
				return fmt.Errorf("cycle detected in DAG starting from node %s", nodeID)
			}
		}
	}

	return nil
}

// hasCycle 使用 DFS 检测是否有循环
func (e *ParallelExecutor) hasCycle(nodeID string, dag *DAG, visited, recStack map[string]bool) bool {
	visited[nodeID] = true
	recStack[nodeID] = true

	node, exists := dag.Nodes[nodeID]
	if !exists {
		return false
	}

	for _, depID := range node.Deps {
		if !visited[depID] {
			if e.hasCycle(depID, dag, visited, recStack) {
				return true
			}
		} else if recStack[depID] {
			return true
		}
	}

	recStack[nodeID] = false
	return false
}

// GetMaxConcurrency 获取最大并发数
func (e *ParallelExecutor) GetMaxConcurrency() int {
	return e.maxConcurrency
}

// SetMaxConcurrency 设置最大并发数
func (e *ParallelExecutor) SetMaxConcurrency(max int) {
	e.maxConcurrency = max
	if len(e.sem) < max {
		newSem := make(chan struct{}, max)
		e.sem = newSem
	}
}
