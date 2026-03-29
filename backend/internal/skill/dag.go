package skill

import "fmt"

// DAG 依赖图
type DAG struct {
	Nodes map[string]*Node
}

// Node DAG 节点
type Node struct {
	ID          string      `json:"id"`
	Data        interface{} `json:"data"`
	Deps        []string    `json:"deps"`        // 依赖列表
	Dependents  []string    `json:"dependents"` // 依赖此节点的其他节点
}

// DAGBuilder DAG 构建器
type DAGBuilder struct{}

// NewDAGBuilder 创建 DAG 构建器
func NewDAGBuilder() *DAGBuilder {
	return &DAGBuilder{}
}

// BuildFromWorkflow 从 Workflow 构建 DAG
func (b *DAGBuilder) BuildFromWorkflow(workflow *Workflow) (*DAG, error) {
	dag := &DAG{
		Nodes: make(map[string]*Node),
	}

	// 创建所有节点
	for _, step := range workflow.Steps {
		dag.Nodes[step.ID] = &Node{
			ID:         step.ID,
			Data:       step,
			Deps:       step.DependsOn,
			Dependents: []string{},
		}
	}

	// 验证所有依赖节点都存在
	for _, node := range dag.Nodes {
		for _, dep := range node.Deps {
			if _, exists := dag.Nodes[dep]; !exists {
				return nil, fmt.Errorf("workflow step %s depends on non-existent step %s",
					node.ID, dep)
			}
		}
	}

	// 建立反向依赖关系
	for _, step := range workflow.Steps {
		for _, dep := range step.DependsOn {
			if depNode, ok := dag.Nodes[dep]; ok {
				depNode.Dependents = append(depNode.Dependents, step.ID)
			}
		}
	}

	// 验证无循环
	if _, err := dag.TopologicalSort(); err != nil {
		return nil, err
	}

	return dag, nil
}

// TopologicalSort 拓扑排序
func (dag *DAG) TopologicalSort() ([]string, error) {
	var order []string
	visited := make(map[string]bool)
	temporary := make(map[string]bool)

	var visit func(id string) error
	visit = func(id string) error {
		if temporary[id] {
			// 发现循环依赖
			return fmt.Errorf("cycle detected in workflow, step %s", id)
		}

		if visited[id] {
			return nil
		}

		temporary[id] = true

		if node, ok := dag.Nodes[id]; ok {
			// 先访问所有依赖
			for _, dep := range node.Deps {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}

		delete(temporary, id)
		visited[id] = true
		order = append(order, id)

		return nil
	}

	// 访问所有节点
	for id := range dag.Nodes {
		if err := visit(id); err != nil {
			return nil, err
		}
	}

	return order, nil
}

// GetRoots 获取根节点（无依赖的节点）
func (dag *DAG) GetRoots() []string {
	var roots []string

	for id, node := range dag.Nodes {
		if len(node.Deps) == 0 {
			roots = append(roots, id)
		}
	}

	return roots
}

// GetLeaves 获取叶子节点（无依赖者的节点）
func (dag *DAG) GetLeaves() []string {
	var leaves []string

	for id, node := range dag.Nodes {
		if len(node.Dependents) == 0 {
			leaves = append(leaves, id)
		}
	}

	return leaves
}

// GetExecutableNodes 获取可执行节点（所有依赖已满足）
func (dag *DAG) GetExecutableNodes(completed map[string]bool) []string {
	var executable []string

	for id, node := range dag.Nodes {
		// 跳过已完成的节点
		if completed[id] {
			continue
		}

		// 检查所有依赖是否完成
		allDepsCompleted := true
		for _, dep := range node.Deps {
			if !completed[dep] {
				allDepsCompleted = false
				break
			}
		}

		if allDepsCompleted {
			executable = append(executable, id)
		}
	}

	return executable
}

// Validate 验证 DAG
func (dag *DAG) Validate() error {
	if len(dag.Nodes) == 0 {
		return fmt.Errorf("empty DAG")
	}

	// 检查所有依赖节点都存在
	for id, node := range dag.Nodes {
		for _, dep := range node.Deps {
			if _, exists := dag.Nodes[dep]; !exists {
				return fmt.Errorf("node %s has invalid dependency %s", id, dep)
			}
		}
	}

	// 检查无循环
	_, err := dag.TopologicalSort()
	if err != nil {
		return err
	}

	return nil
}

// GetNode 获取节点
func (dag *DAG) GetNode(id string) (*Node, bool) {
	node, ok := dag.Nodes[id]
	return node, ok
}

// Count 获取节点数量
func (dag *DAG) Count() int {
	return len(dag.Nodes)
}

// Clone 克隆 DAG
func (dag *DAG) Clone() *DAG {
	if dag == nil {
		return nil
	}

	clone := &DAG{
		Nodes: make(map[string]*Node, len(dag.Nodes)),
	}

	for id, node := range dag.Nodes {
		clone.Nodes[id] = &Node{
			ID:         node.ID,
			Data:       node.Data,
			Deps:       append([]string{}, node.Deps...),
			Dependents: append([]string{}, node.Dependents...),
		}
	}

	return clone
}

// GetDependencyChain 获取节点的依赖链
func (dag *DAG) GetDependencyChain(id string) []string {
	if _, ok := dag.Nodes[id]; !ok {
		return nil
	}

	var chain []string
	visited := make(map[string]bool)

	var collect func(nodeID string)
	collect = func(nodeID string) {
		if visited[nodeID] {
			return
		}

		visited[nodeID] = true
		chain = append(chain, nodeID)

		node := dag.Nodes[nodeID]
		for _, dep := range node.Deps {
			collect(dep)
		}
	}

	collect(id)

	return chain
}

// GetDependents 获取依赖此节点的所有节点（传递闭包）
func (dag *DAG) GetDependents(id string) []string {
	var dependents []string
	visited := make(map[string]bool)

	var collect func(nodeID string)
	collect = func(nodeID string) {
		if visited[nodeID] {
			return
		}

		visited[nodeID] = true

		node := dag.Nodes[nodeID]
		for _, depID := range node.Dependents {
			dependents = append(dependents, depID)
			collect(depID)
		}
	}

	collect(id)

	return dependents
}
