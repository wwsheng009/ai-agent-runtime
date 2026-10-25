package commands

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_residency_runtime.go —— T8b-1：residency 运行时接线（进程级）。
//
// 语义：
//   - 未启用 → 所有接线为空操作；
//   - 启用但不逐出（evictPrefix=false）→ 不打开 store（只窗口化）；
//   - 启用且逐出（evictPrefix=true）→ 不立即建库，等 Scene 超过 resident
//     high-water（默认 512×1.25=640 cells）时再惰性打开 session_cells.sqlite。
//     短会话因此没有额外文件，长会话才承担写穿成本；
//   - 打开失败/目录不可用 → fail-closed：保留 runtime 记录错误，但 Store=nil，
//     任何写穿路径（scene trim / 冷区记账）都不会被安装；
//   - 生命周期与进程一致；会话切换由各消费者按 SessionID 隔离。

type transcriptResidencyRuntime struct {
	Options    ui.TranscriptResidencyOptions
	Policy     ui.ResidencyPolicy
	Store      ui.CellBodyStore
	SessionDir string
	StorePath  string
	// StoreErr 记录"store 未打开的原因"：既包含真实错误（目录不可用/打开失败），
	// 也包含刻意的惰性跳过（evictPrefix=false / bodyStore=off）。消费方据此
	// 区分"降级"与"正常未启用写穿"，不要把前者当成故障。
	StoreErr string
	// StoreLazy 表示逐出已启用但尚未越过 resident high-water，store 还没有打开。
	// 这是正常状态，不是故障。
	StoreLazy bool

	storeMu sync.Mutex
}

var transcriptResidencyRuntimeState atomic.Pointer[transcriptResidencyRuntime]

// installTranscriptResidencyRuntime 安装/替换 residency 运行时。
func installTranscriptResidencyRuntime(opts ui.TranscriptResidencyOptions, sessionDir string) *transcriptResidencyRuntime {
	if !opts.Enabled {
		// 替换/回退时先关闭旧 store：只清指针会让 sqlite 句柄泄漏
		// （回退演练实测：TempDir 清理时报 file in use）。
		if previous := transcriptResidencyRuntimeState.Load(); previous != nil {
			previous.closeStore()
		}
		transcriptResidencyRuntimeState.Store(nil)
		return nil
	}
	// 重复安装（配置热更新）同样先关旧 store，避免句柄泄漏与文件锁冲突。
	if previous := transcriptResidencyRuntimeState.Load(); previous != nil {
		previous.closeStore()
	}
	runtime := &transcriptResidencyRuntime{
		Options:    opts,
		Policy:     ui.ResidencyPolicyFromOptions(opts),
		SessionDir: sessionDir,
	}
	if strings.EqualFold(strings.TrimSpace(opts.BodyStore), "off") {
		runtime.StoreErr = "bodyStore=off（只窗口化、不逐出）"
		transcriptResidencyRuntimeState.Store(runtime)
		return runtime
	}
	if !opts.EvictPrefix {
		runtime.StoreErr = "evictPrefix=false（只窗口化；写穿 store 未打开）"
		transcriptResidencyRuntimeState.Store(runtime)
		return runtime
	}
	if strings.TrimSpace(sessionDir) == "" {
		runtime.StoreErr = "session dir unavailable"
		transcriptResidencyRuntimeState.Store(runtime)
		return runtime
	}
	// 长会话才需要写穿：等 ensureSceneTrimPolicy 发现 resident 超过 high-water
	// 后调用 ensureStore。这样不会给每个短会话创建 session_cells.sqlite。
	runtime.StoreLazy = true
	transcriptResidencyRuntimeState.Store(runtime)
	return runtime
}

// currentTranscriptResidencyRuntime 返回当前运行时（未启用为 nil）。
func currentTranscriptResidencyRuntime() *transcriptResidencyRuntime {
	return transcriptResidencyRuntimeState.Load()
}

// ensureStore 在首次真正需要逐出时打开写穿库。并发调用只打开一次。
func (r *transcriptResidencyRuntime) ensureStore() ui.CellBodyStore {
	if r == nil {
		return nil
	}
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	if r.Store != nil {
		return r.Store
	}
	if r.StoreErr != "" && !r.StoreLazy {
		// 已经明确不可用（bodyStore=off / 目录缺失 / 打开失败）：保持 fail-closed，
		// 不在每个 changeset 上重复尝试。
		return nil
	}
	if !r.Options.Enabled || !r.Options.EvictPrefix {
		return nil
	}
	if strings.TrimSpace(r.SessionDir) == "" {
		r.StoreErr = "session dir unavailable"
		r.StoreLazy = false
		return nil
	}
	path := filepath.Join(r.SessionDir, "session_cells.sqlite")
	store, err := openSQLiteCellBodyStore(path)
	if err != nil {
		r.StoreErr = err.Error()
		r.StoreLazy = false
		return nil
	}
	r.Store = store
	r.StorePath = path
	r.StoreErr = ""
	r.StoreLazy = false
	return store
}

func (r *transcriptResidencyRuntime) storeSnapshot() (ui.CellBodyStore, string, string, bool) {
	if r == nil {
		return nil, "", "", false
	}
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	return r.Store, r.StorePath, r.StoreErr, r.StoreLazy
}

func (r *transcriptResidencyRuntime) closeStore() {
	if r == nil {
		return
	}
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	if r.Store != nil {
		if closer, ok := r.Store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	r.Store = nil
	r.StorePath = ""
}

// transcriptResidencyRuntimeStatus 是状态投影（T8b-5 的前置口径）。
func transcriptResidencyRuntimeStatus() (enabled bool, storePath string, storeErr string) {
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil {
		return false, "", ""
	}
	_, path, errText, _ := runtime.storeSnapshot()
	return true, path, errText
}

// transcriptResidencyRuntimeEnabled 报告进程级 residency 是否已启用。调试端点
// （T8b-4）据此决定缺省窗口：关闭时保持历史全量读取（默认路径不变）。
func transcriptResidencyRuntimeEnabled() bool {
	enabled, _, _ := transcriptResidencyRuntimeStatus()
	return enabled
}
