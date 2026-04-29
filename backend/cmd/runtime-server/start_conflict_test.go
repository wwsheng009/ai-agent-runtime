package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
)

func TestDetectRuntimeServerStartConflictReturnsManagedInstance(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "runtime-server.pid")
	runningConfigPath := filepath.Join(t.TempDir(), "running-config.yaml")
	if err := runtimeserver.WriteInstanceInfo(pidFile, runtimeserver.InstanceInfo{
		PID:        os.Getpid(),
		ListenAddr: "127.0.0.1:8101",
		ConfigPath: runningConfigPath,
	}); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	requestedConfigPath := filepath.Join(t.TempDir(), "requested-config.yaml")
	conflict, err := detectRuntimeServerStartConflict(requestedConfigPath, "", pidFile)
	if err != nil {
		t.Fatalf("detect conflict: %v", err)
	}
	if conflict == nil {
		t.Fatalf("expected conflict, got nil")
	}
	if conflict.Reason != "managed_instance" {
		t.Fatalf("expected managed_instance, got %q", conflict.Reason)
	}
	if conflict.PID != os.Getpid() {
		t.Fatalf("expected pid=%d, got %d", os.Getpid(), conflict.PID)
	}
	if conflict.RunningConfigPath != runningConfigPath {
		t.Fatalf("expected running config %q, got %q", runningConfigPath, conflict.RunningConfigPath)
	}
	if conflict.RequestedConfigPath != requestedConfigPath {
		t.Fatalf("expected requested config %q, got %q", requestedConfigPath, conflict.RequestedConfigPath)
	}
}

func TestFormatRuntimeServerStartConflictMentionsOldInstanceWhenConfigsDiffer(t *testing.T) {
	message := formatRuntimeServerStartConflict(&runtimeServerStartConflict{
		PID:                 1234,
		ListenAddr:          "0.0.0.0:8101",
		PIDFile:             filepath.Join("logs", "runtime-server.pid"),
		RunningConfigPath:   filepath.Join("configs", "config-old.yaml"),
		RequestedConfigPath: filepath.Join("configs", "config.yaml"),
		Reason:              "managed_instance",
	})

	if !strings.Contains(message, "当前运行实例与本次请求配置不同") {
		t.Fatalf("expected mismatch hint, got %q", message)
	}
	if !strings.Contains(message, "stop 或 restart") {
		t.Fatalf("expected restart hint, got %q", message)
	}
}

func TestFormatRuntimeServerStartConflictMentionsNoReloadWhenConfigsMatch(t *testing.T) {
	configPath := filepath.Join("configs", "config.yaml")
	message := formatRuntimeServerStartConflict(&runtimeServerStartConflict{
		PID:                 1234,
		ListenAddr:          "0.0.0.0:8101",
		PIDFile:             filepath.Join("logs", "runtime-server.pid"),
		RunningConfigPath:   configPath,
		RequestedConfigPath: configPath,
		Reason:              "managed_instance",
	})

	if !strings.Contains(message, "本次 start 不会重新加载配置") {
		t.Fatalf("expected no-reload hint, got %q", message)
	}
}
