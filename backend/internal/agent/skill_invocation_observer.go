package agent

import (
	"strings"
	"sync"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// skillInvocationObserver 在主循环工具执行面观测隐式技能调用（SK-3 / SK-7 观测链）。
//
// 与 Executor 的索引互补：Executor 索引只覆盖可执行技能、观测技能桥内部的工具循环；
// 这里的索引包含文档模式技能，观测主循环直接执行的工具（read_file SKILL.md、
// run_shell_command 落在技能 scripts/ 或根目录），补齐"正文注入 + 主循环执行脚本"链路。
type skillInvocationObserver struct {
	mu    sync.Mutex
	gen   uint64
	index *runtimeskill.ImplicitInvocationIndex
}

func (o *skillInvocationObserver) detect(gen uint64, summaries []*runtimeskill.SkillSummary, toolName string, args map[string]interface{}) []runtimeskill.ImplicitInvocation {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if o.index == nil || o.gen != gen {
		o.index = runtimeskill.BuildImplicitInvocationIndexWithOptions(
			summaries,
			runtimeskill.ImplicitInvocationIndexOptions{IncludeDocumentMode: true},
		)
		o.gen = gen
	}
	index := o.index
	o.mu.Unlock()
	return runtimeskill.DetectImplicitInvocations(index, toolName, args)
}

// noteSkillRegistryMutation 标记技能面已变化；懒建索引下次判定时自动重建。
func (a *Agent) noteSkillRegistryMutation() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.skillIndexGen++
	a.mu.Unlock()
}

// DetectMainLoopSkillInvocations 判定一次主循环工具调用是否命中隐式技能调用（SK-3）。
// 索引口径包含文档模式技能；返回值已按 name 稳定排序，调用方负责 turn 级去重与事件发布。
func (a *Agent) DetectMainLoopSkillInvocations(toolName string, args map[string]interface{}) []runtimeskill.ImplicitInvocation {
	if a == nil || a.skillRouter == nil {
		return nil
	}
	registry := a.skillRouter.Registry()
	if registry == nil {
		return nil
	}
	a.mu.RLock()
	gen := a.skillIndexGen
	a.mu.RUnlock()
	return a.skillObserver.detect(gen, registry.ListSummaries(), toolName, args)
}

// observeImplicitSkillInvocations 观测主循环工具面命中并发布 skills.invoked（SK-3）。
// seen 是 turn 级去重集合（键 scope:path:name），由调用方持有；只观测成功执行的调用。
func (loop *ReActLoop) observeImplicitSkillInvocations(traceID, sessionID string, step int, toolCalls []types.ToolCall, results []toolExecutionResult, seen map[string]struct{}) {
	if loop == nil || loop.agent == nil || seen == nil {
		return
	}
	for i, call := range toolCalls {
		if i >= len(results) || strings.TrimSpace(results[i].Error) != "" {
			continue
		}
		matches := loop.agent.DetectMainLoopSkillInvocations(call.Name, call.Args)
		if len(matches) == 0 {
			continue
		}
		loop.agent.publishSkillInvoked(traceID, sessionID, step, matches, seen)
	}
}

// publishSkillInvoked 发布隐式技能调用事件（总线级：Event.SessionID 留空，避免被 A 通道
// 记为"未落盘丢弃"；会话归属只写进载荷），并按 seen 做 turn 级去重。
func (a *Agent) publishSkillInvoked(traceID, sessionID string, step int, invocations []runtimeskill.ImplicitInvocation, seen map[string]struct{}) {
	for _, inv := range runtimeskill.DedupeInvocations(invocations) {
		key := runtimeskill.InvocationKey(inv)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		payload := runtimeskill.SkillInvokedEventPayload(inv)
		payload["source"] = "main_loop"
		payload["step"] = step
		if sessionID != "" {
			payload["session_id"] = sessionID
		}
		if traceID != "" {
			payload["trace_id"] = traceID
		}
		a.emitRuntimeEvent(runtimeskill.SkillInvokedEventType, "", "", payload)
	}
}
