// Package schedulehost 的在岗可见性（S3，§4.1/§4.2）：
//
//	ProbePresence 回答"现在有没有调度者在线、能不能跑当前 store"（三态 +
//	幽灵 leader 检测），供 schedule_create 回执、/schedule 面板与 daemon
//	/status 三个面共用同一口径；SchedulerCapability 构造节点档案上的
//	store 兼容标记（scheduler:store-v<N>）。
package schedulehost

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

const (
	// CapabilitySchedulerStorePrefix 是 store 兼容档能力位前缀（§4.2 M5）：
	// scheduler:store-v<N> 声明"N 是本节点理解的最高 store schema 版本"。
	// 与普通 CapabilityScheduler 并存：旧读者仍按 scheduler 识别节点，
	// 新读者可从标记判定版本兼容。
	CapabilitySchedulerStorePrefix = "scheduler:store-v"
)

// SchedulerCapability 构造带 store 兼容档的能力位（§4.2）。
func SchedulerCapability(schemaVersion int) string {
	return fmt.Sprintf("%s%d", CapabilitySchedulerStorePrefix, schemaVersion)
}

// SchedulerKindPriority 返回调度者角色的优先级（数字小 = 优先级高，与
// schedule.driver.participants 的排序语义一致）：daemon > runtime-server > aicli。
// 未知 kind 返回 0（最高）——保守方向：宁可让位，不抢。
func SchedulerKindPriority(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "scheduled", RoleDaemon:
		return 1
	case RoleRuntimeServer:
		return 2
	case "chat", RoleAICLI:
		return 3
	default:
		return 0
	}
}

// HigherPrioritySchedulerOnline 报告是否存在"比 myKind 优先级更高且存活"的
// 调度者（S5 优先级让位 v2）：扫描节点档案，取 live + 带调度能力位的节点，
// 按 SchedulerKindPriority 比较。返回命中的 kind（空串 = 无）。
//
// 语义：只回答"要不要让位"这一观测问题，不参与控制决策；读取与抢占之间的
// 竞态只影响让位时机（早让/晚让一个轮询周期），不影响唯一性（租约 + claim 兜底）。
func HigherPrioritySchedulerOnline(paths mesh.Paths, myKind, myNodeID string) (bool, string) {
	if !paths.Enabled() {
		return false, ""
	}
	myRank := SchedulerKindPriority(myKind)
	if myRank <= 1 {
		// 已是最高优先级（daemon / 未知角色）：无让位对象。
		return false, ""
	}
	view := mesh.BuildView(paths, mesh.ViewOptions{})
	for i := range view.Nodes {
		node := &view.Nodes[i]
		if node.NodeID == myNodeID || node.State != mesh.NodeStateLive {
			continue
		}
		if _, hasScheduler := schedulerDeclaredSchema(node.Record); !hasScheduler {
			continue
		}
		rank := SchedulerKindPriority(node.Kind)
		if rank > 0 && rank < myRank {
			return true, node.Kind
		}
	}
	return false, ""
}

// creatorAffinityOnline 报告"最近创建活动任务的创建者会话"是否宿主在
// 另一个存活且具调度能力位的节点上（S6 创建者亲和）：返回该节点 kind（空串 =
// 不存在亲和对象）。供多 aicli 并存时决定"谁该持调度租约"——创建作业的
// aicli 优先于旁观者 aicli。
//
// 语义与 HigherPrioritySchedulerOnline 相同：只回答观测问题，不参与控制
// 决策；读取竞态只影响让位时机（早让/晚让一个轮询周期），唯一性仍由
// 租约 + claim 兜底。
func creatorAffinityOnline(paths mesh.Paths, myNodeID string, sessionIDs []string) (bool, string) {
	if !paths.Enabled() || len(sessionIDs) == 0 {
		return false, ""
	}
	view := mesh.BuildView(paths, mesh.ViewOptions{})
	for i := range view.Nodes {
		node := &view.Nodes[i]
		if node.NodeID == myNodeID || node.State != mesh.NodeStateLive {
			continue
		}
		if node.Session == nil || strings.TrimSpace(node.Session.ID) == "" {
			continue
		}
		sessionID := strings.TrimSpace(node.Session.ID)
		for _, wanted := range sessionIDs {
			if wanted != "" && wanted == sessionID {
				return true, node.Kind
			}
		}
	}
	return false, ""
}

// CreatorAffinitySource 是创建者亲和的输入接缝：返回当前**活动任务**里最近
// 创建的 N 个任务的创建者会话 id（新到旧；空切片/nil = 无活动任务，不亲和）。
// 由持有 Store 的调用方实现（通常直接包 store.List），Leader 只在让位检查时
// 调用——读失败按"无亲和"处理（fail-open：亲和是体验优化，不挡调度）。
type CreatorAffinitySource interface {
	ActiveCreatorSessions(limit int) []string
}

// StoreCreatorAffinity 用 schedule.Store 实现创建者亲和源：按到期时点升序
// 返回活动任务的创建者会话（最快到期在前，去重）。
type StoreCreatorAffinity struct {
	Store schedule.Store
}

// ActiveCreatorSessions 返回活动任务的创建者会话 id，最快到期在前。
// 排序不依赖 Store.List 的 SQL 次序（TEXT 时点列的字典序在毫秒精度下跨机器
// 不可靠），显式按 ScheduledAt 升序排——最快到期的任务马上要投递，其创建者
// 节点应优先持租，投递走 L1 就近直达；亲和对象随任务临近稳定收敛。
func (s StoreCreatorAffinity) ActiveCreatorSessions(limit int) []string {
	if s.Store == nil || limit <= 0 {
		return nil
	}
	tasks, err := s.Store.List()
	if err != nil {
		return nil
	}
	active := make([]schedule.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == schedule.StatusActive && strings.TrimSpace(task.CreatedBySessionID) != "" {
			active = append(active, task)
		}
	}
	if len(active) == 0 {
		return nil
	}
	// 升序 = 最快到期的任务在前（它马上要投递，创建者节点应优先持租走 L1）。
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].Record.ScheduledAt.Before(active[j].Record.ScheduledAt)
	})
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)
	for _, task := range active {
		if len(out) >= limit {
			break
		}
		creator := strings.TrimSpace(task.CreatedBySessionID)
		if _, dup := seen[creator]; dup {
			continue
		}
		seen[creator] = struct{}{}
		out = append(out, creator)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// StoreCompatCheck 用"读取当前 store schema 版本"的函数构造领导权自检
// （§4.2：选主前/持租约期间校验）。库版本高于本二进制支持档时报错——调用方
// （Leader）据此放弃/退出领导权，让更新的节点接管，而不是占着租约空转。
func StoreCompatCheck(schemaVersion func(ctx context.Context) (int, error)) func() error {
	return func() error {
		version, err := schemaVersion(context.Background())
		if err != nil {
			return err
		}
		if supported := schedule.SupportedSchemaVersion(); version > supported {
			return fmt.Errorf(
				"schedule store schema v%d is newer than this binary (v%d); release the lease and let a newer node take over",
				version, supported)
		}
		return nil
	}
}

// PresenceState 是创建/查看时的在岗三态（§4.1）。
type PresenceState string

const (
	// PresenceOnline：有存活调度者持租约，且其声明兼容当前 store。
	PresenceOnline PresenceState = "driver_online"
	// PresenceAbsent：无调度者持租约，或持有者已被判定非存活（幽灵 leader）。
	PresenceAbsent PresenceState = "driver_absent"
	// PresenceIncompatible：有调度者在岗，但其 store 兼容档低于当前 store
	// （旧版二进制），或未声明兼容档（无法判定）。
	PresenceIncompatible PresenceState = "driver_incompatible"
)

// Presence 是在岗检查的一致性快照（/schedule、daemon /status、create 回执共用）。
type Presence struct {
	State        PresenceState `json:"state"`
	HolderNodeID string        `json:"holder_node_id,omitempty"`
	HolderKind   string        `json:"holder_kind,omitempty"`
	HolderPID    int           `json:"holder_pid,omitempty"`
	// LeaseExpiresAt 是租约到期时间（持有者掉线后接管窗口的上界）。
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	// LeaseEpoch 是持有者任期的 fencing token（M2；legacy 租约 → 0）。
	LeaseEpoch int64 `json:"lease_epoch,omitempty"`
	// Ghost 标记"租约存在但持有者不在线/不在档案"（幽灵 leader，M8 只读半边）。
	Ghost bool `json:"ghost,omitempty"`
	// StoreSchema 是本次检查面对的 store schema（调用方传入：本二进制的支持档，
	// 本地库已由本进程打开时即其当前版本）。
	StoreSchema int `json:"store_schema"`
	// DriverStoreSchema 是调度者声明的最高支持档（缺省 0 = 未声明）。
	DriverStoreSchema int `json:"driver_store_schema,omitempty"`
	// Message 是人类可读的判定说明（在线时为欢迎语；离线/不兼容时为可操作提示）。
	Message   string    `json:"message,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Notice 把非在岗状态翻译成创建回执可用的警告；在线时为空串。
func (p Presence) Notice() string {
	switch p.State {
	case PresenceOnline:
		return ""
	case PresenceIncompatible:
		if p.DriverStoreSchema <= 0 {
			return fmt.Sprintf(
				"调度者版本过旧：在岗调度者 %s 未声明 store 兼容档（旧版二进制）——任务已创建，但不会准时触发；请升级调度者（scheduled / runtime-server）。",
				p.holderLabel())
		}
		return fmt.Sprintf(
			"调度者版本过旧：在岗调度者 %s 支持的 store 最高为 v%d，当前 store 为 v%d——任务已创建，但不会准时触发；请升级调度者（scheduled / runtime-server）二进制。",
			p.holderLabel(), p.DriverStoreSchema, p.StoreSchema)
	default:
		message := "当前无调度者在岗，任务不会准时触发；请启动 scheduled 或保持 runtime-server 运行。"
		if p.Ghost {
			message += fmt.Sprintf("（检测到幽灵 leader：租约持有者 %s 已不在线，将在租约到期后被接管）", p.holderLabel())
		}
		return message
	}
}

func (p Presence) holderLabel() string {
	if p.HolderNodeID == "" {
		return "(未知节点)"
	}
	if p.HolderKind == "" {
		return p.HolderNodeID
	}
	return p.HolderNodeID + "/" + p.HolderKind
}

// ProbePresence 探测本机 mesh 上的调度者在岗状态（storeSchema 传当前 store 版本，
// 通常为 schedule.SupportedSchemaVersion()）。
func ProbePresence(storeSchema int) Presence {
	return ProbePresenceWithPaths(mesh.ResolvePaths(), storeSchema)
}

// ProbePresenceWithPaths 是 ProbePresence 的显式路径版本（测试与多根部署用）。
// 判定规则（§4.1）：
//   - 无租约 → driver_absent；
//   - 租约持有者不在档案 / 非 live / 无调度能力位 → driver_absent（ghost=true）；
//   - 持有者在岗但未声明兼容档或档位低于 storeSchema → driver_incompatible；
//   - 其余 → driver_online。
//
// 已知边界：daemon 以 degraded=standalone 信号量模式（mesh 完全不可用）运行时
// 不写租约，本探针会报 absent——该模式本身就是带警告的单机降级，错误方向是
// “多报不在岗”，不会静默假在线。
func ProbePresenceWithPaths(paths mesh.Paths, storeSchema int) Presence {
	now := time.Now().UTC()
	presence := Presence{StoreSchema: storeSchema, CheckedAt: now}
	if !paths.Enabled() {
		presence.State = PresenceAbsent
		presence.Message = "mesh 未启用，无法判定调度者在岗状态。"
		return presence
	}
	lease, ok := mesh.ReadLease(paths.LeasePath(LeasePurpose, LeaseKey))
	if !ok {
		presence.State = PresenceAbsent
		presence.Message = "无人持有调度者租约。"
		return presence
	}
	presence.HolderNodeID = lease.OwnerNodeID
	presence.LeaseExpiresAt = lease.ExpiresAt()
	presence.LeaseEpoch = lease.Epoch

	view := mesh.BuildView(paths, mesh.ViewOptions{})
	var holder *mesh.NodeView
	for i := range view.Nodes {
		if view.Nodes[i].NodeID == lease.OwnerNodeID {
			holder = &view.Nodes[i]
			break
		}
	}
	if holder == nil {
		presence.State = PresenceAbsent
		presence.Ghost = true
		presence.Message = "租约持有者不在节点档案中。"
		return presence
	}
	presence.HolderKind = holder.Kind
	presence.HolderPID = holder.PID
	if holder.State != mesh.NodeStateLive {
		presence.State = PresenceAbsent
		presence.Ghost = true
		presence.Message = fmt.Sprintf("租约持有者 %s 状态为 %s（非 live）。", lease.OwnerNodeID, holder.State)
		return presence
	}

	declared, hasScheduler := schedulerDeclaredSchema(holder.Record)
	if !hasScheduler {
		presence.State = PresenceAbsent
		presence.Ghost = true
		presence.Message = fmt.Sprintf("租约持有者 %s 未声明调度能力位。", lease.OwnerNodeID)
		return presence
	}
	presence.DriverStoreSchema = declared
	if declared <= 0 {
		presence.State = PresenceIncompatible
		presence.Message = fmt.Sprintf("调度者 %s 未声明 store 兼容档（旧版二进制）。", presence.holderLabel())
		return presence
	}
	if declared < storeSchema {
		presence.State = PresenceIncompatible
		presence.Message = fmt.Sprintf("调度者 %s 支持 store v%d，低于当前 v%d。", presence.holderLabel(), declared, storeSchema)
		return presence
	}
	presence.State = PresenceOnline
	return presence
}

// schedulerDeclaredSchema 从节点档案能力位提取调度者声明：是否具调度能力、
// 声明的最高 store 兼容档（0 = 有 scheduler 位但未带兼容标记）。
func schedulerDeclaredSchema(record *mesh.NodeRecord) (int, bool) {
	if record == nil {
		return 0, false
	}
	declared := 0
	hasScheduler := false
	for _, capability := range record.Capabilities {
		trimmed := strings.TrimSpace(capability)
		if strings.EqualFold(trimmed, CapabilityScheduler) {
			hasScheduler = true
			continue
		}
		rest, found := strings.CutPrefix(strings.ToLower(trimmed), CapabilitySchedulerStorePrefix)
		if !found {
			continue
		}
		hasScheduler = true
		if version, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && version > declared {
			declared = version
		}
	}
	return declared, hasScheduler
}
