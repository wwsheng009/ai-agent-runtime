package commands

import (
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// chat_residency_runtime.go —— T8b-1：residency 运行时接线（进程级）。
//
// 语义：
//   - 默认 nil（未启用）→ 所有接线为空操作，行为与引入前逐字节一致；
//   - 启用但**不逐出**（evictPrefix=false，T10-4 后的默认）→ **不打开 store**。
//     "只窗口化"档不需要写穿目标；提前建库会让每个纯聊天会话都落一个
//     session_cells.sqlite（默认开启后实测：大量测试因文件句柄未释放而清理失败）。
//   - 启用且逐出（evictPrefix=true）→ 在会话目录打开写穿 store（session_cells.sqlite）；
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
}

var transcriptResidencyRuntimeState atomic.Pointer[transcriptResidencyRuntime]

// installTranscriptResidencyRuntime 安装/替换 residency 运行时。
func installTranscriptResidencyRuntime(opts ui.TranscriptResidencyOptions, sessionDir string) *transcriptResidencyRuntime {
	if !opts.Enabled {
		// 替换/回退时先关闭旧 store：只清指针会让 sqlite 句柄泄漏
		// （回退演练实测：TempDir 清理时报 file in use）。
		if previous := transcriptResidencyRuntimeState.Load(); previous != nil && previous.Store != nil {
			if closer, ok := previous.Store.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
		transcriptResidencyRuntimeState.Store(nil)
		return nil
	}
	// 重复安装（配置热更新）同样先关旧 store，避免句柄泄漏与文件锁冲突。
	if previous := transcriptResidencyRuntimeState.Load(); previous != nil && previous.Store != nil {
		if closer, ok := previous.Store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
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
	// 惰性 store（T10-4）：不逐出就不需要写穿目标。默认档因此完全不碰磁盘。
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
	path := filepath.Join(sessionDir, "session_cells.sqlite")
	store, err := openSQLiteCellBodyStore(path)
	if err != nil {
		runtime.StoreErr = err.Error()
		transcriptResidencyRuntimeState.Store(runtime)
		return runtime
	}
	runtime.Store = store
	runtime.StorePath = path
	transcriptResidencyRuntimeState.Store(runtime)
	return runtime
}

// currentTranscriptResidencyRuntime 返回当前运行时（未启用为 nil）。
func currentTranscriptResidencyRuntime() *transcriptResidencyRuntime {
	return transcriptResidencyRuntimeState.Load()
}

// transcriptResidencyRuntimeStatus 是状态投影（T8b-5 的前置口径）。
func transcriptResidencyRuntimeStatus() (enabled bool, storePath string, storeErr string) {
	runtime := currentTranscriptResidencyRuntime()
	if runtime == nil {
		return false, "", ""
	}
	return true, runtime.StorePath, runtime.StoreErr
}

// transcriptResidencyRuntimeEnabled 报告进程级 residency 是否已启用。调试端点
// （T8b-4）据此决定缺省窗口：关闭时保持历史全量读取（默认路径不变）。
func transcriptResidencyRuntimeEnabled() bool {
	enabled, _, _ := transcriptResidencyRuntimeStatus()
	return enabled
}
