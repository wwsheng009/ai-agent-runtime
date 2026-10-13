package main

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// openSharedSessionLeaseStore 打开与宿主共享的会话执行租约存储
// （runtime.yaml 的 sessionRuntime.storePath / storeDSN）。
//
// 这是 E1「不抢占活跃会话」的依据：宿主（runtime-server / aicli）用同一个
// chat.SessionLeaseStore 持有会话执行租约，daemon 必须查**同一份存储**才能看到
// 会话是否正在被跑（见 docs/automation-tasks/runtimehost-executor-extraction-plan.md §2.5a）。
//
// 未配置 / 打开失败 → 返回错误，调用方应 fail-closed（不内嵌）。
func openSharedSessionLeaseStore(configPath string) (chat.SessionLeaseStore, func(), error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, nil, fmt.Errorf("no --config: sessionRuntime store path cannot be resolved")
	}
	manager := config.NewRuntimeManager(configPath)
	if err := manager.Load(); err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	sessionRuntime := manager.Get().SessionRuntime
	path := strings.TrimSpace(sessionRuntime.StorePath)
	dsn := strings.TrimSpace(sessionRuntime.StoreDSN)
	if path == "" && dsn == "" {
		return nil, nil, fmt.Errorf("sessionRuntime.storePath/storeDSN not configured")
	}
	store, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{Path: path, DSN: dsn})
	if err != nil {
		return nil, nil, fmt.Errorf("open session runtime store: %w", err)
	}
	return store, func() { _ = store.Close() }, nil
}
