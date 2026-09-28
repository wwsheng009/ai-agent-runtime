package main

import (
	"context"
	"os"
	"strings"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// bootRuntimeServerKnowledge 在启动阶段接入知识层（Phase 1 交付 6）。
//
// 角色语义：runtime-server 是长驻进程，**默认角色是 writer owner**（除非已有其它
// owner），因为它是唯一能保证「启动后索引一次、之后增量」的常驻宿主；第二个实例
// 会在 owner 仲裁中降级为 reader，不会双写。
//
// workspace 锚点顺序：runtime.yaml 的 `workspace.root`（相对配置文件解析）→ 进程 cwd。
// 两者都拿不到时不接入，退化成"无知识层"，绝不阻塞启动。
//
// `mode=off`（默认）时本函数**不调用 Activate**，因此不建库、不建锁文件、不起
// goroutine —— 这是"off 与无知识层逐字节一致"的落点。
//
// 与 `06` §4 Phase 1 交付 6 的一处刻意偏离：计划写的是"向 internal/background 注册
// `knowledge.index.initial` 任务"，但 `internal/background.Manager` 只有 shell 作业
// 通道（`SubmitShell`），进程内任务没有公开注册口（`setRunJobImpl` 是测试缝）。
// 把索引塞进 shell 作业会多起一个进程并重复打开同一个 store，反而破坏单写者不变量。
// 因此首次全量索引作为 Activate 内部的后台 goroutine 运行：同样不阻塞启动、
// 不阻塞 turn，可在 Close 时取消并等待退出。该偏离登记在 CHANGELOG。
func bootRuntimeServerKnowledge(runtimeConfig *runtimecfg.RuntimeConfig, configFile string) *knowledge.Activation {
	if runtimeConfig == nil {
		return nil
	}
	if runtimeConfig.Knowledge.Mode == knowledge.ModeOff || runtimeConfig.Knowledge.Mode == "" {
		return nil
	}

	workspace := strings.TrimSpace(runtimeConfig.Workspace.Root)
	if workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			workspace = strings.TrimSpace(wd)
		}
	} else if !isAbsWorkspaceRoot(workspace) {
		workspace = resolvePathFromConfigFile(configFile, workspace)
	}
	if workspace == "" {
		logpkg.Warnf("knowledge: mode=%s but no workspace root (runtime.yaml workspace.root 未设置且 cwd 不可用)，降级为 off",
			runtimeConfig.Knowledge.Mode)
		return nil
	}

	act, err := knowledge.Activate(context.Background(), runtimeConfig.Knowledge, workspace, knowledge.ActivationOptions{
		OnIndexDone: func(result knowledge.IndexResult, indexErr error) {
			if indexErr != nil {
				logpkg.Warnf("knowledge: initial index for %q failed: %v", workspace, indexErr)
				return
			}
			logpkg.Infof("knowledge: initial index for %q done: scanned=%d indexed=%d skipped=%d symbols=%d refs=%d errors=%d",
				workspace, result.Scanned, result.Indexed, result.Skipped, result.Symbols, result.Refs, result.Errors)
		},
	})
	if err != nil {
		// 不阻塞启动：知识层是内部能力，打不开就退化成"无知识层"。
		logpkg.Warnf("knowledge: activation for %q failed, continuing without it: %v", workspace, err)
		return nil
	}
	if act != nil {
		logpkg.Infof("knowledge: enabled mode=%s role=%s workspace=%s", act.Mode(), act.Role(), workspace)
	}
	return act
}

// isAbsWorkspaceRoot 判断路径是否为绝对路径（Windows 盘符 / UNC 与 POSIX 都覆盖）。
func isAbsWorkspaceRoot(path string) bool {
	if path == "" {
		return false
	}
	return os.IsPathSeparator(path[0]) || (len(path) > 1 && path[1] == ':')
}

// knowledgeAttributionSink 把 usage ledger store 折叠成 exploration_attribution
// 落库口：store 未启用（nil）或具体类型未实现该接口时返回 nil，观察器随之整体
// no-op（与 CLI 宿主 ledgerAttributionSink 同口径）。
func knowledgeAttributionSink(store any) knowledge.AttributionSink {
	sink, _ := store.(knowledge.AttributionSink)
	return sink
}
