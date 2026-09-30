package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 探索记忆自动采集器（06 §4 Phase 2 W2）。
//
// 采集通道复用 Phase 1 的 OnToolObserved 只读钩子（agent.LoopReActConfig）：
// 把 grep / view 的最终结果映射成 exploration_sessions / nodes / edges 落库。
// 与 ShadowObserver 的关系：
//   - shadow 写 exploration_attribution（对比口径，Phase 1 观测面）；
//   - 本采集器写探索记忆（任务工作集，供 W3/W4 复用判定消费）。
//   两者共享同一条 hook 链与同一套 query_hash 口径，互不替代。
//
// 不变量（与 shadow 同契约）：
//   - 尽力而为：Record 不阻塞、不返回错误；写失败经 OnError 上报或静默，
//     绝不冒泡为 turn 失败，也绝不修改工具结果；
//   - 只读语义：hook 在工具执行完成后触发，采集器不触碰工具返回值；
//   - 隐私：query 节点只落 query_hash（复用 shadow 的 queryHash 口径），
//     不落 pattern / 查询明文；
//   - mode=off / 未接线：Recorder 为 nil，Record 为 nil-safe no-op，零写入；
//   - 写入门禁（W6）：只读子代理 / 只读会话 / 未标注来源零写入（fail closed）；
//     低于跨任务下限（0.90）的节点在任务作用域未知时丢弃；
//   - 异步 + 有界：入队满即丢弃（丢观察可接受、卡 turn 不可接受）；每任务
//     节点/边数量有上限，单次观察的文件目标数有上限。
//
// task_id / turn_id：OnToolObserved 签名不携带任务/轮次上下文，本版本以
// 宿主经 ObservedCall.TaskID 注入任务作用域（W6）；宿主没有任务语义时回退
// session 级工作集（task_id 为空，W1 的 DTO 明确允许该回退），不编造 task id。

// ObservationSource 标记一次工具观察的来源（06 §4 Phase 2 W6 写入门禁）。
//
// 零值 = 未标注：**默认不写**（fail closed）——判定错误的方向必须是"不写"
// （漏记可接受、脏写不可接受，06 §4 W6 风险项）。
type ObservationSource string

const (
	// ObservationSourceUnspecified 表示宿主未标注来源：不写 exploration memory。
	ObservationSourceUnspecified ObservationSource = ""
	// ObservationSourceMainSession 是主会话（非子代理）：允许写入。
	ObservationSourceMainSession ObservationSource = "main_session"
	// ObservationSourceSubagent 是可写子代理：允许写入（per-task 作用域）。
	ObservationSourceSubagent ObservationSource = "subagent"
	// ObservationSourceSubagentReadOnly 是只读子代理：零写入（04 §4.5 / §6 R10）。
	ObservationSourceSubagentReadOnly ObservationSource = "subagent_read_only"
	// ObservationSourceReadOnlySession 是只读会话（非子代理标记）：零写入。
	ObservationSourceReadOnlySession ObservationSource = "read_only_session"
)

// WriteAllowed 报告该来源的观察是否允许写入探索记忆。
//
// 只读子代理 / 只读会话 / 未标注 → false；主会话与可写子代理 → true。
// 只读子代理**不写索引、不写 exploration memory**（04 §4.5 交付 6 / 04 §6 R10）；
// 索引写入本就不在观察链上（owner + index_jobs 串行），此处门禁覆盖探索记忆。
func (s ObservationSource) WriteAllowed() bool {
	return s == ObservationSourceMainSession || s == ObservationSourceSubagent
}

// ObservationSourceFor 把宿主侧的子代理 / 只读标记折叠为来源枚举。
//
// 只读标记优先：只读子代理取最保守一侧；两个标记都为 false 时按主会话
// （可写）处理——调用方必须先完成"是否子代理"的判定，未判定时不得调用本函数，
// 而应保留零值（不写）。
func ObservationSourceFor(subagent, readOnly bool) ObservationSource {
	switch {
	case readOnly && subagent:
		return ObservationSourceSubagentReadOnly
	case readOnly:
		return ObservationSourceReadOnlySession
	case subagent:
		return ObservationSourceSubagent
	default:
		return ObservationSourceMainSession
	}
}

const (
	// DefaultExplorationRecorderQueueSize 是异步采集队列的默认容量。
	DefaultExplorationRecorderQueueSize = 128
	// 单次观察最多落盘的 file 目标数（超出部分丢弃，防止一次 grep 命中上千文件）。
	defaultMaxRecorderTargetsPerCall = 20
	// 单个 exploration（任务工作集）的节点/边上限（进程内计数，尽力而为）。
	defaultMaxRecorderNodesPerTask = 200
	defaultMaxRecorderEdgesPerTask = 400
	// WorkspaceVersion 的缓存 TTL：版本计算要扫全量文件行，不能每次工具调用都算。
	defaultRecorderVersionTTL = 30 * time.Second
	// 直接观察到的文件节点置信度（工具输出里出现了该文件）；
	// query 节点略低（查询→结果集的映射是启发式解析）。
	observedFileNodeConfidence  = 1.0
	observedQueryNodeConfidence = 0.9
)

// ExplorationRecorderConfig 组装一个 ExplorationRecorder。
type ExplorationRecorderConfig struct {
	// Store 是知识层持久化句柄；nil 时构造器返回 nil（整体 no-op）。
	Store Store
	// Workspace 是工作区根目录，用于把绝对路径折叠成 workspace 相对路径。
	Workspace string
	// WorkspaceID 显式指定 workspaces 行 id；空时按 Workspace 惰性登记。
	WorkspaceID string
	// Mode 是产生本批数据的知识层模式（shadow | on）；off 时整体 no-op。
	Mode Mode
	// Now 便于测试注入时钟；nil 时用 time.Now。
	Now func() time.Time
	// QueueSize 是异步队列容量；<=0 时用 DefaultExplorationRecorderQueueSize。
	QueueSize int
	// MaxTargetsPerCall 限制单次观察落盘的 file 目标数；<=0 时用默认值。
	MaxTargetsPerCall int
	// MaxNodesPerTask / MaxEdgesPerTask 是任务工作集上限；<=0 时用默认值。
	MaxNodesPerTask int
	MaxEdgesPerTask int
	// VersionTTL 是 WorkspaceVersion 的缓存时长；<=0 时用默认值。
	VersionTTL time.Duration
	// OnError 观察写失败（尽力而为路径的显式出口）；nil 时静默。
	OnError func(error)

	// workspaceIDFor 由 Activation 注入，复用 Layer 的惰性登记缓存；
	// 为空时回退到 Store.EnsureWorkspace(Workspace)。
	workspaceIDFor func(context.Context) (string, error)
}

func (c ExplorationRecorderConfig) normalize() ExplorationRecorderConfig {
	if strings.TrimSpace(string(c.Mode)) == "" {
		c.Mode = ModeOff
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.QueueSize <= 0 {
		c.QueueSize = DefaultExplorationRecorderQueueSize
	}
	if c.MaxTargetsPerCall <= 0 {
		c.MaxTargetsPerCall = defaultMaxRecorderTargetsPerCall
	}
	if c.MaxNodesPerTask <= 0 {
		c.MaxNodesPerTask = defaultMaxRecorderNodesPerTask
	}
	if c.MaxEdgesPerTask <= 0 {
		c.MaxEdgesPerTask = defaultMaxRecorderEdgesPerTask
	}
	if c.VersionTTL <= 0 {
		c.VersionTTL = defaultRecorderVersionTTL
	}
	return c
}

// recorderJob 是队列元素；ack 非 nil 表示 Flush 屏障（处理到它时关闭 ack）。
type recorderJob struct {
	call ObservedCall
	ack  chan struct{}
}

// ExplorationRecorder 把 OnToolObserved 观察异步写入探索记忆。
//
// nil-safe：所有导出方法都接受 nil 接收者（off / 未接线的宿主无需分支）。
type ExplorationRecorder struct {
	cfg ExplorationRecorderConfig

	baseCtx context.Context
	cancel  context.CancelFunc

	queue chan recorderJob
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once

	mu        sync.Mutex
	version   string
	versionAt time.Time
	nodeCount map[string]int
	edgeCount map[string]int
}

// NewExplorationRecorder 创建采集器并启动后台 worker。
//
// Store 为 nil 或 Mode==ModeOff 时返回 nil：调用方持有的 nil 指针上调用
// Record/Flush/Close 都是 no-op，保证"未启用零写入"。
func NewExplorationRecorder(cfg ExplorationRecorderConfig) *ExplorationRecorder {
	cfg = cfg.normalize()
	if cfg.Store == nil || cfg.Mode == ModeOff {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &ExplorationRecorder{
		cfg:       cfg,
		baseCtx:   ctx,
		cancel:    cancel,
		queue:     make(chan recorderJob, cfg.QueueSize),
		done:      make(chan struct{}),
		nodeCount: make(map[string]int),
		edgeCount: make(map[string]int),
	}
	r.wg.Add(1)
	go r.worker()
	return r
}

// Record 投递一次工具观察（非阻塞、尽力而为）。
//
// 预过滤只做最便宜的判定（工具名 / 错误 / 会话 / 来源门禁）；队列满即丢弃。
func (r *ExplorationRecorder) Record(ctx context.Context, call ObservedCall) {
	if r == nil || r.cfg.Store == nil || r.cfg.Mode == ModeOff {
		return
	}
	tool := strings.ToLower(strings.TrimSpace(call.Tool))
	if tool != "grep" && tool != "view" {
		return
	}
	if strings.TrimSpace(call.Err) != "" || strings.TrimSpace(call.SessionID) == "" {
		return
	}
	// W6 写入门禁（fail closed）：只读子代理 / 只读会话 / 未标注来源零写入。
	if !call.Source.WriteAllowed() {
		return
	}
	select {
	case <-r.done:
		return
	default:
	}
	select {
	case r.queue <- recorderJob{call: call}:
	case <-r.done:
	default:
		// 队列满：丢弃本次观察（不阻塞工具结果路径）。
	}
}

// Flush 等待此前投递的观察全部处理完（测试与生命周期收尾用）。
//
// 返回 nil 只表示屏障已越过；单条观察的写失败经 OnError 上报，不从这里返回
// （保持"失败不冒泡"契约）。
func (r *ExplorationRecorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ack := make(chan struct{})
	select {
	case r.queue <- recorderJob{ack: ack}:
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 停止 worker 并等待其退出；幂等且 nil-safe。
//
// 取消 baseCtx 会让在途/待处理的写快速失败（写失败只走 OnError），
// 因此 Close 不会被锁等待拖住。
func (r *ExplorationRecorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.done)
		if r.cancel != nil {
			r.cancel()
		}
	})
	r.wg.Wait()
}

// worker 顺序消费队列；收到 done 后把已入队的元素处理完再退出。
func (r *ExplorationRecorder) worker() {
	defer r.wg.Done()
	for {
		select {
		case job := <-r.queue:
			r.handle(job)
		case <-r.done:
			for {
				select {
				case job := <-r.queue:
					r.handle(job)
				default:
					return
				}
			}
		}
	}
}

func (r *ExplorationRecorder) handle(job recorderJob) {
	if job.call.Tool != "" {
		if err := r.process(r.baseCtx, job.call); err != nil {
			r.reportError(err)
		}
	}
	if job.ack != nil {
		close(job.ack)
	}
}

func (r *ExplorationRecorder) reportError(err error) {
	if err == nil || r == nil || r.cfg.OnError == nil {
		return
	}
	r.cfg.OnError(err)
}

func (r *ExplorationRecorder) now() time.Time {
	if r == nil || r.cfg.Now == nil {
		return time.Now()
	}
	return r.cfg.Now()
}

// process 把一次观察同步落库；返回的错误只供 OnError 上报。
func (r *ExplorationRecorder) process(ctx context.Context, call ObservedCall) error {
	if r == nil || r.cfg.Store == nil || r.cfg.Mode == ModeOff {
		return nil
	}
	// W6 写入门禁（worker 侧二次判定）：即使调用方绕过 Record 预过滤，
	// 只读子代理 / 只读会话 / 未标注来源也不得落库。
	if !call.Source.WriteAllowed() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queryTarget, files, ok := r.mapObservation(call)
	if !ok {
		return nil
	}
	sessionID := strings.TrimSpace(call.SessionID)
	if sessionID == "" {
		return nil
	}
	// W6：探索节点写 session_id + task_id + workspace_id；task_id 为空时
	// 回退 session 级工作集（不编造任务语义）。
	taskID := strings.TrimSpace(call.TaskID)

	workspaceID, err := r.workspaceID(ctx)
	if err != nil {
		return fmt.Errorf("knowledge: exploration recorder: resolve workspace: %w", err)
	}
	version, err := r.knowledgeVersion(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("knowledge: exploration recorder: workspace version: %w", err)
	}

	explorationID, err := r.cfg.Store.UpsertExplorationSession(ctx, ExplorationSession{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
		TaskID:      taskID,
	})
	if err != nil {
		return fmt.Errorf("knowledge: exploration recorder: upsert session: %w", err)
	}

	now := r.now().UTC()
	nodes, edges := r.buildObservationRows(explorationID, version, queryTarget, files, now)
	nodes, edges = applyCrossTaskWriteFloor(taskID, nodes, edges)
	for _, node := range nodes {
		if !r.reserveNode(explorationID) {
			break
		}
		if _, err := r.cfg.Store.AppendExplorationNode(ctx, node); err != nil {
			return fmt.Errorf("knowledge: exploration recorder: append node %q: %w", node.Target, err)
		}
		r.noteNode(explorationID)
	}
	for _, edge := range edges {
		if !r.reserveEdge(explorationID) {
			break
		}
		if _, err := r.cfg.Store.AppendExplorationEdge(ctx, edge); err != nil {
			return fmt.Errorf("knowledge: exploration recorder: append edge: %w", err)
		}
		r.noteEdge(explorationID)
	}
	return nil
}

// mapObservation 把一次 grep/view 观察映射为 (query 摘要, file 目标列表)。
//
// ok=false 表示该观察不产生探索记忆（非 grep/view、路径不可解析等）。
// grep 的文件集合与 shadow 的 baseline 解析共用同一套函数与作用域补全规则；
// query 摘要与 shadow 的 queryHash 口径一致（tool + pattern + scope 的 SHA-256）。
func (r *ExplorationRecorder) mapObservation(call ObservedCall) (string, []string, bool) {
	tool := strings.ToLower(strings.TrimSpace(call.Tool))
	switch tool {
	case "grep":
		rawPatterns := grepPatternList(call.Args)
		pattern := strings.Join(rawPatterns, " | ")
		scopes := grepPathScopes(call.Args)
		globScope := firstNonEmpty(argString(call.Args, "glob"), argString(call.Args, "include"))
		queryTarget := queryHash("grep", pattern, strings.TrimSpace(strings.Join(scopes, " ")+" "+globScope))

		scopeBases := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			if base := relativizeWorkspacePath(scope, r.cfg.Workspace); base != "" {
				scopeBases = append(scopeBases, base)
			}
		}
		keys, _ := parseGrepBaselineMulti(call.Output, scopeBases)
		seen := make(map[string]bool, len(keys))
		files := make([]string, 0, minInt(len(keys), r.cfg.MaxTargetsPerCall))
		for _, key := range keys {
			file := pathFromKey(key)
			if file == "" || seen[file] {
				continue
			}
			seen[file] = true
			files = append(files, file)
			if len(files) >= r.cfg.MaxTargetsPerCall {
				break
			}
		}
		return queryTarget, files, true
	case "view":
		file := relativizeWorkspacePath(argString(call.Args, "file_path"), r.cfg.Workspace)
		if file == "" {
			return "", nil, false
		}
		offset := intArg(call.Args, "offset")
		if offset <= 0 {
			offset = 1
		}
		limit := intArg(call.Args, "limit")
		if limit <= 0 {
			limit = countNonEmptyLines(call.Output)
		}
		queryTarget := queryHash("view", file, fmt.Sprintf("%s:%d+%d", file, offset, limit))
		return queryTarget, []string{file}, true
	default:
		return "", nil, false
	}
}

// buildObservationRows 生成"query 节点 + file 节点 + query→file derived_from 边"。
//
// 同 target 的合并由 store 的稳定主键（ON CONFLICT use_count+1）保证，
// 这里只负责按观察顺序给出稳定行集合。
func (r *ExplorationRecorder) buildObservationRows(explorationID, version, queryTarget string, files []string, now time.Time) ([]ExplorationNode, []ExplorationEdge) {
	queryNode := ExplorationNode{
		ID:               ExplorationNodeID(explorationID, queryTarget),
		ExplorationID:    explorationID,
		NodeType:         NodeTypeQuery,
		Target:           queryTarget,
		Confidence:       observedQueryNodeConfidence,
		KnowledgeVersion: version,
		CreatedAt:        now,
	}
	nodes := make([]ExplorationNode, 0, 1+len(files))
	nodes = append(nodes, queryNode)
	edges := make([]ExplorationEdge, 0, len(files))
	for _, file := range files {
		fileNode := ExplorationNode{
			ID:               ExplorationNodeID(explorationID, file),
			ExplorationID:    explorationID,
			NodeType:         NodeTypeFile,
			Target:           file,
			Confidence:       observedFileNodeConfidence,
			KnowledgeVersion: version,
			CreatedAt:        now,
		}
		nodes = append(nodes, fileNode)
		edges = append(edges, ExplorationEdge{
			ID:            ExplorationEdgeID(explorationID, queryNode.ID, fileNode.ID, EdgeTypeDerivedFrom),
			ExplorationID: explorationID,
			FromNodeID:    queryNode.ID,
			ToNodeID:      fileNode.ID,
			EdgeType:      EdgeTypeDerivedFrom,
			Weight:        1.0,
		})
	}
	return nodes, edges
}

// crossTaskWriteFloor 是写入侧跨任务硬规则的下限（04 §4.4：跨任务复用 ≥ 0.90）。
//
// W6 明确不得下调：取 W3 的默认阈值，不暴露配置旋钮。
func crossTaskWriteFloor() float64 {
	return DefaultPlannerConfig().CrossTaskConfidence
}

// applyCrossTaskWriteFloor 执行写入侧跨任务硬规则（W6 / 04 §4.4 / §6 R10）：
//
// 无法证明任务归属（task_id 为空）时，只有 confidence ≥ 跨任务下限（0.90）的
// 节点才允许落库；低于下限的节点必须带任务作用域（同任务复用带 ≥0.80 由 W3
// Gate 判定），否则丢弃——低置信观察不得以"跨任务可复用"形态污染工作区共享
// 记忆。丢弃节点时同步丢弃以其为端点的边，保持行集合自洽。
func applyCrossTaskWriteFloor(taskID string, nodes []ExplorationNode, edges []ExplorationEdge) ([]ExplorationNode, []ExplorationEdge) {
	if strings.TrimSpace(taskID) != "" {
		return nodes, edges
	}
	floor := crossTaskWriteFloor()
	keptNodes := make([]ExplorationNode, 0, len(nodes))
	keptIDs := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if node.Confidence < floor {
			continue
		}
		keptNodes = append(keptNodes, node)
		keptIDs[node.ID] = true
	}
	if len(keptNodes) == len(nodes) {
		return nodes, edges
	}
	keptEdges := make([]ExplorationEdge, 0, len(edges))
	for _, edge := range edges {
		if keptIDs[edge.FromNodeID] && keptIDs[edge.ToNodeID] {
			keptEdges = append(keptEdges, edge)
		}
	}
	return keptNodes, keptEdges
}

// workspaceID 解析（或惰性登记）workspaces 行 id。
func (r *ExplorationRecorder) workspaceID(ctx context.Context) (string, error) {
	if r == nil || r.cfg.Store == nil {
		return "", errors.New("knowledge: exploration recorder: store is required")
	}
	if id := strings.TrimSpace(r.cfg.WorkspaceID); id != "" {
		return id, nil
	}
	if r.cfg.workspaceIDFor != nil {
		id, err := r.cfg.workspaceIDFor(ctx)
		if err != nil {
			return "", err
		}
		if id = strings.TrimSpace(id); id != "" {
			return id, nil
		}
	}
	workspace := strings.TrimSpace(r.cfg.Workspace)
	if workspace == "" {
		return "", errors.New("knowledge: exploration recorder: workspace is required")
	}
	return r.cfg.Store.EnsureWorkspace(ctx, Workspace{RootPath: workspace})
}

// knowledgeVersion 返回工作区知识版本（带 TTL 缓存）。
//
// 版本用于 W3 的 stale 判定（版本不匹配 → 不可复用）；缓存避免每次工具调用
// 都扫全量文件行（G3：写入开销落在预算内）。
func (r *ExplorationRecorder) knowledgeVersion(ctx context.Context, workspaceID string) (string, error) {
	r.mu.Lock()
	if r.version != "" && r.now().Sub(r.versionAt) < r.cfg.VersionTTL {
		version := r.version
		r.mu.Unlock()
		return version, nil
	}
	r.mu.Unlock()

	version, err := WorkspaceVersion(ctx, r.cfg.Store, workspaceID)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.version, r.versionAt = version, r.now()
	r.mu.Unlock()
	return version, nil
}

func (r *ExplorationRecorder) reserveNode(explorationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeCount[explorationID] < r.cfg.MaxNodesPerTask
}

func (r *ExplorationRecorder) noteNode(explorationID string) {
	r.mu.Lock()
	r.nodeCount[explorationID]++
	r.mu.Unlock()
}

func (r *ExplorationRecorder) reserveEdge(explorationID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.edgeCount[explorationID] < r.cfg.MaxEdgesPerTask
}

func (r *ExplorationRecorder) noteEdge(explorationID string) {
	r.mu.Lock()
	r.edgeCount[explorationID]++
	r.mu.Unlock()
}
