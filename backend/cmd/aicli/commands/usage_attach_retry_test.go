package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// attachWarnCapture 捕获 usageAttachWarn 输出（包级函数变量，测试内替换）。
type attachWarnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *attachWarnCapture) record(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

func (c *attachWarnCapture) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, msg := range c.msgs {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

func (c *attachWarnCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

func captureUsageAttachWarn(t *testing.T) *attachWarnCapture {
	t.Helper()
	captured := &attachWarnCapture{}
	previous := usageAttachWarn
	usageAttachWarn = captured.record
	t.Cleanup(func() { usageAttachWarn = previous })
	return captured
}

// TestEnsureLocalUsageServiceRetriesAfterAttachFailure 回归：启动期 attach
// 失败（多进程竞争 sqlite 写锁、路径瞬时不可用）不能把失败结果永久缓存。
//
// 此前用 sync.Once：一次瞬时失败即意味着该进程整个生命周期都不再写入
// usage_requests —— Web/TUI 分析视图静默为空，日志里也没有任何线索
// （只能翻数据库取证）。
func TestEnsureLocalUsageServiceRetriesAfterAttachFailure(t *testing.T) {
	warnings := captureUsageAttachWarn(t)
	dir := t.TempDir()
	runtimeStore, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path: filepath.Join(dir, "session_runtime.sqlite"),
	})
	if err != nil {
		t.Fatalf("NewSQLiteRuntimeStore: %v", err)
	}
	defer func() { _ = runtimeStore.Close() }()

	// 用同名目录占住分析库路径：Open 失败且属于确定性失败（快速返回，非锁冲突）。
	blocked := filepath.Join(dir, usageanalytics.DefaultDBFileName)
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir blocked path: %v", err)
	}

	host := &localChatRuntimeHost{
		EventBus:     runtimeevents.NewBus(),
		RuntimeStore: runtimeStore,
	}
	if service := ensureLocalUsageService(host); service != nil {
		t.Fatalf("分析库路径被占住时不应返回服务")
	}
	if !warnings.contains("挂载失败") {
		t.Fatalf("attach 失败必须留痕，warnings = %v", warnings.messages())
	}

	// 解除阻塞后重试：必须真正重试（旧实现会让进程永久失去采集能力）。
	if err := os.Remove(blocked); err != nil {
		t.Fatalf("remove blocked path: %v", err)
	}
	service := ensureLocalUsageService(host)
	if service == nil {
		t.Fatalf("解除阻塞后应重试成功，warnings = %v", warnings.messages())
	}
	t.Cleanup(service.Close)

	// 成功结果缓存：后续调用复用同一实例（避免重复 attach 同一 EventBus）。
	if again := ensureLocalUsageService(host); again != service {
		t.Fatalf("成功后应复用同一服务实例")
	}
	if host.usageSvc != service {
		t.Fatalf("服务应缓存到 host.usageSvc")
	}
}
