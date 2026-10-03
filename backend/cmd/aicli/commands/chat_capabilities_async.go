package commands

// chat_capabilities_async.go — 「先渲染 UI，再异步装载能力面」的启动编排。
//
// 实测（Windows / 仓库根目录，`AICLI_STARTUP_TIMING=1`）首帧前的启动耗时：
//
//	capabilities_mcp            ~120ms
//	tools_manager_ctor         ~2.40s   runtimetools.NewDefaultManagerWithRuntimeConfig
//	tools_list                 ~3.31s   toolManager.ListTools
//	host_bootstrap_manager     ~10.32s  runtimebootstrap.NewManager（DiscoverOnly 技能扫描）
//	host_supervision_plane      ~53ms
//	其余（解析/持久化/首帧）   ~140ms
//
// 即 ~98% 的首帧前时间花在三段「发现型」调用上，而它们产出的工具面/skills 只在
// 真正发起 turn 时才被需要——用户却要盯着一个还没画出来的界面等十几秒。
//
// 因此把能力面拆成两段（见 chat_setup.go）：
//   - discoverChatCapabilities：耗时的发现/建连，产物全是局部值，只读 session，
//     放到首帧之后的后台 goroutine；
//   - attachChatCapabilities：把产物挂到 session，是唯一写 session 能力字段的
//     地方，必须留在主 goroutine——首帧之后主 goroutine 仍在读这些字段（状态行、
//     executor descriptor、工具可用性），让后台去写就是数据竞争。
//
// 门控语义与 chat_actor_warmup.go 的 actor warmup 一致（done channel + wait），
// 区别只是 await 点更靠前（executor 就绪，而不是 actor 就绪）。

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// chatCapabilityLoad 是会话级能力面装载的门控句柄。
//
// 生命周期由 session.capabilitiesLoad 持有：prepareChatCapabilitiesAsync 装上它，
// ensureChatExecutor 消费它（await → 在主 goroutine 上完成挂载）。
type chatCapabilityLoad struct {
	done chan struct{}
	// discover 在后台 goroutine 上跑，只读 session，产出局部值。
	discover func() (*chatCapabilityDiscovery, error)
	// attach 在主 goroutine 上跑（await 之后），是唯一写 session 能力字段的地方。
	attach func(*chatCapabilityDiscovery) (func(), error)

	// mu 保护 err/discovery（等待者并发读取）。
	mu        sync.Mutex
	err       error
	discovery *chatCapabilityDiscovery

	// attachMu 串行化挂载阶段：并发 await 者里只有一个真正写 session。
	attachMu   sync.Mutex
	attachDone bool
}

// completeDiscovery 公布发现阶段产物。后台 goroutine 只调它，绝不碰 session。
func (l *chatCapabilityLoad) completeDiscovery(discovery *chatCapabilityDiscovery, err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.discovery = discovery
	l.err = err
	l.mu.Unlock()
	close(l.done)
}

// currentErr 读取当前已知的装载错误。
func (l *chatCapabilityLoad) currentErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// awaitDiscovery 阻塞到发现阶段结束，返回其错误。
func (l *chatCapabilityLoad) awaitDiscovery(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-l.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return l.currentErr()
}

// runAttach 在主 goroutine 上执行挂载阶段，且只执行一次。
func (l *chatCapabilityLoad) runAttach(ctx context.Context) error {
	if l == nil || l.attach == nil {
		return nil
	}
	l.attachMu.Lock()
	defer l.attachMu.Unlock()
	if l.attachDone {
		return l.currentErr()
	}
	if err := l.awaitDiscovery(ctx); err != nil {
		l.attachDone = true
		return err
	}
	l.mu.Lock()
	discovery := l.discovery
	l.mu.Unlock()

	_, err := l.attach(discovery)

	l.mu.Lock()
	l.err = err
	l.discovery = nil
	l.mu.Unlock()
	l.attachDone = true
	return err
}

// installChatCapabilitiesGate 安装门控句柄，但**不**启动装载。
//
// 拆成 install/start 是为了保证「安装早于首帧、启动晚于首帧」：
//   - 早于首帧：首帧后用户立刻提交时，ensureChatExecutor 能看到 handle 并 await；
//   - 晚于首帧：装载不与首帧渲染争抢 CPU，也确保首帧不被拖慢。
func installChatCapabilitiesGate(
	session *ChatSession,
	discover func() (*chatCapabilityDiscovery, error),
	attach func(*chatCapabilityDiscovery) (func(), error),
) {
	if session == nil {
		return
	}
	session.capabilitiesMu.Lock()
	if session.capabilitiesLoad == nil && discover != nil {
		session.capabilitiesLoad = &chatCapabilityLoad{
			done:     make(chan struct{}),
			discover: discover,
			attach:   attach,
		}
	}
	session.capabilitiesMu.Unlock()
}

// currentChatCapabilityLoad 返回本会话的能力面装载句柄（未安装时返回 nil）。
func currentChatCapabilityLoad(session *ChatSession) *chatCapabilityLoad {
	if session == nil {
		return nil
	}
	session.capabilitiesMu.Lock()
	defer session.capabilitiesMu.Unlock()
	return session.capabilitiesLoad
}

// sessionCapabilitiesLoadView 回显异步能力面门控的真实状态。
//
// 这是「各闸全绿（present/enabled/dirs 都对）但 binding=nil」时唯一能区分下列
// 三种处境的观测点：
//   - 门控未安装：此会话根本没走异步装载路径（discover/attach 都不会执行，
//     因此装载侧的 error 也无从产生——诊断字段全空是必然，不是"没报错"）；
//   - 门控已安装、发现仍在跑或已报错：err 直接给出真实原因；
//   - 门控已安装、发现已完成且无错：问题在挂载侧或 skills 本身。
//
// 缺了这一层，前面所有诊断都只能看到"没挂上"这个结果，看不到"装载是否启动过"。
func sessionCapabilitiesLoadView(session *ChatSession) string {
	load := currentChatCapabilityLoad(session)
	if load == nil {
		return "load=none(gate未安装:此会话未走异步装载路径)"
	}
	discoveryDone := false
	select {
	case <-load.done:
		discoveryDone = true
	default:
	}
	load.mu.Lock()
	errVal := load.err
	load.mu.Unlock()
	if errVal != nil {
		return fmt.Sprintf("load=error(discovery_done=%v) %q", discoveryDone, errVal.Error())
	}
	if discoveryDone {
		return "load=discovered(err=nil,attach待turn或已挂)"
	}
	return "load=discovering(err=nil)"
}

// chatCapabilitiesMountingState 报告技能面尚不可用时的真实阶段，供
// /web/api/skills 在列表为空时给出可区分反馈（冷启动懒装载：discovering →
// attach_pending → 可用）。skillsReady=true（目录已非空）时恒 false；
// 未安装门控（同步/非交互/runtime-server 路径）返回 false，不做虚假承诺。
func chatCapabilitiesMountingState(session *ChatSession, skillsReady bool) (bool, string) {
	if skillsReady {
		return false, ""
	}
	load := currentChatCapabilityLoad(session)
	if load == nil {
		return false, ""
	}
	select {
	case <-load.done:
		if err := load.currentErr(); err != nil {
			return true, "error"
		}
		return true, "attach_pending"
	default:
		return true, "discovering"
	}
}

// awaitChatCapabilities 在真正需要 turn 执行器之前阻塞到能力面就绪，并在主
// goroutine 上完成挂载。未安装（同步路径 / 非交互 / runtime-server 路径）时 no-op。
func awaitChatCapabilities(ctx context.Context, session *ChatSession) error {
	load := currentChatCapabilityLoad(session)
	if load == nil {
		return nil
	}
	return load.runAttach(ctx)
}

// runChatCapabilitiesLoad 在已安装的门控上执行「发现」阶段，并推进动态栏上的
// 启动进度。它只读 session：挂载阶段留给 await 在主 goroutine 上完成。
func runChatCapabilitiesLoad(session *ChatSession) {
	load := currentChatCapabilityLoad(session)
	if load == nil || load.discover == nil {
		return
	}
	// 动态栏只呈现用户可读的阶段短语，精确分布仍看 AICLI_STARTUP_TIMING。
	showChatStartupProgress(session, chatStartupProgressPhaseTools)
	discovery, err := load.discover()
	load.completeDiscovery(discovery, err)
	if err != nil {
		// 发现失败不静默：既有同步路径把它升级成启动失败，这里保持一致，让首个
		// turn 在 ensureChatExecutor 处拿到同一个错误而不是空工具面。
		logpkg.Warnf("AICLI chat capabilities background load failed: %v", err)
		// 把 error 留在 session 上，让 /skills 诊断能直接复述——能力面初始化失败
		// 会导致 skills/runtime host/tools 全缺失，但用户只能看到「total=0」这
		// 样的二级现象；把 error 暴露出来才能止住「怎么查不出错」的循环。
		if session != nil {
			session.CapabilitiesInitError = err.Error()
		}
		fmt.Fprintf(os.Stderr, "Warning: 能力面初始化失败（工具/Skills/运行时宿主不可用）: %v\n", err)
		clearChatStartupProgress(session)
		// 这批 mark 落在 ready 那次 flush 之后，补一次才能在 AICLI_STARTUP_TIMING
		// 里看到后台装载的真实耗时分布。
		flushChatStartupTiming()
		return
	}
	markChatStartup("capabilities_discovery_done")
	// 发现结束即撤行：耗时的后台扫描（工具枚举 + 技能目录扫描）已完成，
	// 用户可见的「加载」结束。挂载只是首 turn 前的快速字段赋值
	//（attachChatCapabilities），不再单独占动态栏；首个 turn 提交时
	// awaitChatCapabilitiesForTurn 会兜底再清一次（幂等）。
	// 不清的话，用户不提交 turn 就会一直盯着「加载工具 (Ns)」。
	clearChatStartupProgress(session)
	// 这批 mark 落在 ready 那次 flush 之后，补一次才能在 AICLI_STARTUP_TIMING
	// 里看到后台装载的真实耗时分布。
	flushChatStartupTiming()
}

// awaitChatCapabilitiesForTurn 是 turn 路径上带超时的门控入口。
//
// 后台发现可能长达十几秒（技能扫描），因此给它一个显式上限：超时返回可读错误，
// 而不是让界面无限期停在「加载工具 (Ns)」。上限显著大于实测耗时，只用来兜住
// 真实的卡死/挂起。
//
// 区分两种失败并如实回传：到点仍未完成才是「超时」；发现/挂载自身返回错误
// （例如技能注册失败）必须原样以「初始化失败」呈现，不能被统一贴上超时标签，
// 否则真实根因会被错误的恢复建议盖掉。
const chatCapabilitiesWaitLimit = 3 * time.Minute

func awaitChatCapabilitiesForTurn(ctx context.Context, session *ChatSession) error {
	if currentChatCapabilityLoad(session) == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, chatCapabilitiesWaitLimit)
	defer cancel()
	err := awaitChatCapabilities(waitCtx, session)
	// 挂载完成即撤行：动态栏回到 Ready/空闲态，秒表不留在最后一个阶段。
	clearChatStartupProgress(session)
	if err == nil {
		markChatStartup("capabilities_attach_done")
		flushChatStartupTiming()
		return nil
	}
	if ctx.Err() != nil {
		if session != nil {
			session.CapabilitiesInitError = err.Error()
		}
		return err
	}
	if waitCtx.Err() == nil {
		message := fmt.Sprintf("能力面初始化失败: %v", err)
		if session != nil {
			session.CapabilitiesInitError = message
		}
		return fmt.Errorf("能力面初始化失败: %w", err)
	}
	if session != nil {
		session.CapabilitiesInitError = fmt.Sprintf("启动装载工具面超时（%s）: %v", chatCapabilitiesWaitLimit, err)
	}
	return fmt.Errorf("启动装载工具面超时（%s）: %w", chatCapabilitiesWaitLimit, err)
}
