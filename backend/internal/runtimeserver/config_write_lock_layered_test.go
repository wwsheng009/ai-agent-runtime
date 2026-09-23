package runtimeserver

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

// 分层文档保存的锁编排（方案 §9.1 U-2 / §12 R4，2026-09-22 第四次增补）。
//
// SaveDocument 在分层模式下把落盘交给 ApplyMergedDocumentChanges，由它按**目标层文件**
// 各取一次同一把配置写锁（config_file_write.go / config_merged_document.go）。这段编排有
// 两处一旦写错就只在并发/分层场景暴露的失效模式：
//
//  1. 目标层文件被别的写者占锁时，保存必须等待——若层写入绕过锁，读-改-写仍会丢更新；
//  2. 保存进入分层分支前必须**先释放本文档锁**：目标层可能就是本文档（project 层），
//     持锁进入按层取锁即自锁（同一把非重入锁），表现为保存永久不返回。
//
// 两种失效模式都用超时判定钉成失败，而不是让测试挂死。

// setupLayeredConfigForLockTest 建一套「用户层 + 项目层」的分层配置：用户层持有
// providers.items.openai.base_url，项目层持有 aicli.chat.default_model。返回文档服务与
// 两层文件路径（用户层 = $HOME/.aicli/config.yaml，项目层 = <project>/.aicli/config.yaml）。
func setupLayeredConfigForLockTest(t *testing.T) (service *LocalConfigDocumentService, userConfig, projectConfig string) {
	t.Helper()

	home := t.TempDir()
	previousHome := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(previousHome) })

	userConfig = filepath.Join(home, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, userConfig, `
providers:
  items:
    openai:
      base_url: https://user.example
`)
	projectDir := t.TempDir()
	projectConfig = filepath.Join(projectDir, ".aicli", aiclipaths.DefaultConfigFileName)
	writeLayeredConfigFile(t, projectConfig, `
aicli:
  chat:
    default_model: project-model
`)
	t.Chdir(projectDir)
	t.Setenv(agentconfig.MergeConfigEnvVar, "on")

	return NewLocalConfigDocumentService(projectConfig), userConfig, projectConfig
}

// layeredSaveRequestFor 读取分层合并视图，交给 mutate 改一处，返回结构化保存请求。
func layeredSaveRequestFor(t *testing.T, service *LocalConfigDocumentService, mutate func(t *testing.T, parsed map[string]interface{})) skillsapi.ConfigDocumentSaveRequest {
	t.Helper()

	doc, err := service.LoadDocument()
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if len(doc.Layers) == 0 {
		t.Fatal("分层配置未生效：Layers 为空（用例前置条件不成立）")
	}
	parsed := copyDocumentMap(t, doc.Parsed)
	mutate(t, parsed)
	return skillsapi.ConfigDocumentSaveRequest{Parsed: parsed, Mode: "structured"}
}

// setUserLayerBaseURL 改一个**用户层所有**的键（落点 = 用户层文件）。
func setUserLayerBaseURL(t *testing.T, parsed map[string]interface{}, baseURL string) {
	t.Helper()
	providers, _ := parsed["providers"].(map[string]interface{})
	items, _ := providers["items"].(map[string]interface{})
	openai, _ := items["openai"].(map[string]interface{})
	if openai == nil {
		t.Fatalf("分层文档缺少 providers.items.openai：%#v", parsed)
	}
	openai["base_url"] = baseURL
}

// setProjectLayerDefaultModel 改一个**项目层所有**的键（落点 = 项目层文件，也就是
// documentPath 本身）。
func setProjectLayerDefaultModel(t *testing.T, parsed map[string]interface{}, model string) {
	t.Helper()
	aicliSection, _ := parsed["aicli"].(map[string]interface{})
	chat, _ := aicliSection["chat"].(map[string]interface{})
	if chat == nil {
		t.Fatalf("分层文档缺少 aicli.chat：%#v", parsed)
	}
	chat["default_model"] = model
}

// TestLayeredConfigDocumentSaveWaitsOnTargetLayerLock：占住**目标层文件**的写锁后发起分层
// 保存——保存必须等待锁释放。能直接完成说明层写入没走共享写锁（读-改-写仍可并发丢更新）。
func TestLayeredConfigDocumentSaveWaitsOnTargetLayerLock(t *testing.T) {
	service, userConfig, _ := setupLayeredConfigForLockTest(t)
	req := layeredSaveRequestFor(t, service, func(t *testing.T, parsed map[string]interface{}) {
		setUserLayerBaseURL(t, parsed, "https://locked.example")
	})

	unlock := agentconfig.LockConfigFileWrite(userConfig)
	done := make(chan error, 1)
	go func() {
		_, err := service.SaveDocument(req)
		done <- err
	}()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("目标层文件被占锁时分层保存已完成：层写入未走共享写锁（err=%v）", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("释放目标层锁后保存失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("释放目标层锁后保存仍未完成（可能二次取锁自锁）")
	}

	if raw := readLayeredConfigFile(t, userConfig); !strings.Contains(raw, "locked.example") {
		t.Fatalf("目标层未落盘本次变更:\n%s", raw)
	}
}

// TestLayeredConfigDocumentSaveDoesNotSelfLock：改**项目层所有**的键时，目标层文件就是
// documentPath 本身。分层分支在委托 ApplyMergedDocumentChanges 之前先释放本文档锁；若改回
// 「持本文档锁进入按层取锁」，就会在同一把非重入锁上自锁，保存永不返回（这里用超时钉成
// 失败）。
func TestLayeredConfigDocumentSaveDoesNotSelfLock(t *testing.T) {
	service, userConfig, projectConfig := setupLayeredConfigForLockTest(t)
	req := layeredSaveRequestFor(t, service, func(t *testing.T, parsed map[string]interface{}) {
		setProjectLayerDefaultModel(t, parsed, "self-lock-check-model")
	})

	done := make(chan error, 1)
	go func() {
		_, err := service.SaveDocument(req)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("分层保存失败: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("分层保存未在超时内完成：持本文档锁进入按层取锁即自锁（应先释放再委托）")
	}

	if raw := readLayeredConfigFile(t, projectConfig); !strings.Contains(raw, "self-lock-check-model") {
		t.Fatalf("项目层未落盘本次变更:\n%s", raw)
	}
	if raw := readLayeredConfigFile(t, userConfig); strings.Contains(raw, "self-lock-check-model") {
		t.Fatalf("项目层所有的键不得被钉进用户层:\n%s", raw)
	}
}
