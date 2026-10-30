package toolsurface

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolregistry"
)

// 本文件实现"结果契约依赖"的工具面闭包
// （docs/plan/tool-surface-dependency-auto-exposure-design-20261029.md §5）。
//
// 背景：shell 不调用 job_output，但 shell 超时 promoted 成 job 引用后，模型
// 必须能调用 job_output 才读得到结果。这是**结果/数据契约依赖**，不是函数
// 调用依赖。声明在 toolregistry.Entry.Dependencies 里（单一真源），本包只
// 负责按声明做闭包与校验。
//
// 职责边界（关键约束，勿越界）：
//   - ResolveDependencies 只做**名字解析**，返回"应该有哪些工具"，它本身
//     不是授权来源。不得在此创建 manager、读配置、启动 job。
//   - 补入结果必须由调用方交给 policy 层表达为 policy.ContractualTools 成员
//     （见 ResolveContractualTools），不能直接塞进 AllowedTools——否则会绕过
//     "deny 恒优先"与"子代理白名单继承"两条既有不变量。

// neverAutoExpose 是永不由依赖闭包自动补入的工具。
//
// 依赖关系只能补入**只读 consumer**。控制/终止面（起停、监控、写 stdin）
// 必须由用户或 profile 显式允许，否则会自动暴露的工具面就会成为权限提升
// 通道。toolregistry 里的 AutoExpose 标记是第一道闸；这里是第二道，防止
// 后续有人误把 AutoExpose 置真。
var neverAutoExpose = map[string]bool{
	"job_start":   true,
	"job_cancel":  true,
	"job_monitor": true,
	"job_control": true,
	"job_input":   true,
}

// SurfaceResolutionInput 是一次依赖解析的输入。所有名字都会被归一化为
// canonical name，因此别名（bash / task_output）与规范名等价。
type SurfaceResolutionInput struct {
	// Base 是已按 profile 与 allowlist 裁剪后的工具面（canonical 或别名）。
	Base []string
	// Denied 是显式拒绝的工具；deny 恒优先于 auto-expose。
	Denied []string
	// ReadOnly 为真时，闭包只允许只读 consumer，不得引入 shell 类能力。
	ReadOnly bool
	// LiteralNames 为真时，Base 与补入项按字面保留，不做别名归一。
	//
	// remote-tools 需要它：它的 tools allowlist 是**精确字符串匹配**
	// （见 remotetools/tools.go 的 allowed[name] 判断），把 bash 归一成 shell
	// 会改变既有语义（设计文档 D12 明确本期不引入归一）。依赖声明本身仍按
	// canonical 查表，因此声明侧无需重复。
	LiteralNames bool
	// Disabled 为真表示"工具面被完全关闭"（DisableTools 语义）：
	// 返回空面，且**不**跑依赖闭包。契约只填补遗漏项，不能重启关闭的面。
	Disabled bool
}

// SurfaceResolution 是解析结果。
type SurfaceResolution struct {
	// Tools 是最终生效的工具名（含自动补入项），按名字排序。
	Tools []string
	// AutoExposed 是因依赖被自动补入的工具名，按名字排序。
	AutoExposed []string
	// Warnings 是 recommended 依赖缺失等非致命信息。
	Warnings []string
	// Errors 是 required 依赖被 deny 或无法补入等致命问题。
	// 调用方必须把非空的 Errors 视为装配失败（fail closed）。
	Errors []error
	// BrokenProducers 是 required 依赖无法满足的生产者名（canonical）。
	//
	// 两类调用方需要它，且用法不同：
	//   - remote：直接拒绝构建（整面向量 fail closed）；
	//   - local：从生效面撤掉这些生产者，避免把"能起 job 但读不到结果"的
	//     工具交给模型——撤生产者比撤整个会话策略更不打扰用户。
	BrokenProducers []string
}

// Failed 报告解析是否失败。
func (r SurfaceResolution) Failed() bool { return len(r.Errors) > 0 }

// ResolveDependencies 按 toolregistry 里声明的依赖做闭包。
//
// 语义：
//   - Base 先剥掉 Denied 与 readOnly 不兼容项，再反复扫描依赖直到稳定；
//   - required 依赖被 deny 或无法补入 => Errors（fail closed）；
//   - recommended 缺失 => Warnings（只提示，不阻断）；
//   - 幂等：重复调用不重复补入、不重复告警。
func ResolveDependencies(in SurfaceResolutionInput) SurfaceResolution {
	res := SurfaceResolution{}
	if in.Disabled {
		return res
	}

	denied := make(map[string]bool, len(in.Denied))
	for _, name := range in.Denied {
		if canonical := toolregistry.Canonical(name); canonical != "" {
			denied[canonical] = true
		}
	}

	resolved := make(map[string]bool, len(in.Base))
	order := make([]string, 0, len(in.Base))
	add := func(name string) bool {
		if name == "" || resolved[name] {
			return false
		}
		resolved[name] = true
		order = append(order, name)
		return true
	}

	for _, name := range in.Base {
		canonical := toolregistry.Canonical(name)
		if canonical == "" || denied[canonical] {
			continue
		}
		if in.ReadOnly && !readOnlyCompatible(canonical) {
			continue
		}
		// LiteralNames 场景下保留调用方给的名字（remote 的精确匹配语义），
		// 但上面的 denied / readOnly 判定仍走 canonical，保证声明侧唯一真源。
		if in.LiteralNames {
			add(strings.TrimSpace(name))
			continue
		}
		add(canonical)
	}

	autoExposed := make(map[string]bool)
	brokenProducers := make(map[string]bool)
	markBroken := func(producer string) {
		if producer != "" {
			brokenProducers[producer] = true
		}
	}
	// 去重，避免闭包多轮扫描把同一条告警/错误重复记录。
	seenNotice := make(map[string]bool)
	noteWarning := func(msg string) {
		if seenNotice[msg] {
			return
		}
		seenNotice[msg] = true
		res.Warnings = append(res.Warnings, msg)
	}
	noteError := func(err error) {
		if err == nil || seenNotice[err.Error()] {
			return
		}
		seenNotice[err.Error()] = true
		res.Errors = append(res.Errors, err)
	}

	for changed := true; changed; {
		changed = false
		// 快照当前集合：闭包过程中 order 会增长，不能边遍历边追加。
		snapshot := make([]string, len(order))
		copy(snapshot, order)
		for _, tool := range snapshot {
			entry, ok := toolregistry.LookupLive(tool)
			if !ok || len(entry.Dependencies) == 0 {
				continue
			}
			for _, dep := range entry.Dependencies {
				target := toolregistry.Canonical(dep.Tool)
				if target == "" {
					continue
				}
				emitted := target
				if in.LiteralNames {
					emitted = toolregistry.Normalize(dep.Tool)
				}
				if denied[target] {
					if dep.Mode == toolregistry.DependencyRequired {
						markBroken(tool)
						noteError(fmt.Errorf(
							"tool %q requires %q, but it is explicitly denied: %s",
							tool, target, dep.Reason))
					} else {
						noteWarning(fmt.Sprintf(
							"tool %q recommends %q, but it is explicitly denied: %s",
							tool, target, dep.Reason))
					}
					continue
				}
				if resolved[target] || (in.LiteralNames && resolved[emitted]) {
					continue
				}
				if dep.AutoExpose && canAutoExpose(target, in.ReadOnly) {
					if add(emitted) {
						autoExposed[target] = true
						changed = true
					}
					continue
				}
				if dep.Mode == toolregistry.DependencyRequired {
					markBroken(tool)
					noteError(fmt.Errorf(
						"tool %q requires %q, but it cannot be auto-exposed: %s",
						tool, target, dep.Reason))
				} else {
					noteWarning(fmt.Sprintf(
						"tool %q recommends %q, which is not exposed: %s",
						tool, target, dep.Reason))
				}
			}
		}
	}

	sort.Strings(order)
	res.Tools = order
	if len(autoExposed) > 0 {
		names := make([]string, 0, len(autoExposed))
		for name := range autoExposed {
			names = append(names, name)
		}
		sort.Strings(names)
		res.AutoExposed = names
	}
	sort.Strings(res.Warnings)
	if len(brokenProducers) > 0 {
		names := make([]string, 0, len(brokenProducers))
		for name := range brokenProducers {
			names = append(names, name)
		}
		sort.Strings(names)
		res.BrokenProducers = names
	}
	return res
}

// ResolveContractualTools 把依赖解析结果表达成 policy.ContractualTools 集合。
//
// 这是 §3.4 定调的落地面：auto-expose 的补入不做成 AllowedTools 成员，而是
// 并入契约集合，由既有 AllowTool 判定顺序（deny → contractual → ...）生效。
// 这样三条既有不变量自动复用：
//   - deny 恒优先（AllowTool 第一步就查 deny）；
//   - 空 allowlist 不重启（contractualToolAllowed 对空面返回 false）；
//   - 子代理白名单继承（appendContractualToolNames 读同一集合）。
//
// existing 为 nil 时会被创建。返回值总是非 nil（除非既无 existing 又无补入项，
// 此时返回 nil 以保持"无契约"的零值语义）。
func ResolveContractualTools(existing map[string]bool, in SurfaceResolutionInput) (map[string]bool, SurfaceResolution) {
	res := ResolveDependencies(in)
	if len(res.AutoExposed) == 0 {
		return existing, res
	}
	out := make(map[string]bool, len(existing)+len(res.AutoExposed))
	for name, ok := range existing {
		if ok {
			out[name] = true
		}
	}
	for _, name := range res.AutoExposed {
		out[name] = true
	}
	if len(out) == 0 {
		return nil, res
	}
	return out, res
}

// canAutoExpose 判断某个 consumer 是否允许被依赖闭包自动补入。
//
// 规则：永不放行控制面（neverAutoExpose）；不得补入写/执行/委派类工具
// ——自动暴露不能扩大副作用面。
func canAutoExpose(name string, readOnly bool) bool {
	if neverAutoExpose[name] {
		return false
	}
	entry, ok := toolregistry.LookupLive(name)
	if !ok {
		// 未登记的名字不猜：宁可报 required 缺失，也不凭启发式放行。
		return false
	}
	if entry.WriteLike || entry.ShellLike || entry.Delegation {
		return false
	}
	if readOnly && entry.PlanWrite {
		return false
	}
	return true
}

// readOnlyCompatible 报告某工具在只读面是否可保留。
//
// 与 policy 的分工：policy 对 shell 类工具是"按命令"判定，不按名字拦；这里
// 保守一档——依赖闭包与只读面裁剪只保留明确无副作用的名字，避免把执行面
// 悄悄带进只读会话。
func readOnlyCompatible(name string) bool {
	if neverAutoExpose[name] {
		return false
	}
	entry, ok := toolregistry.LookupLive(name)
	if !ok {
		return true
	}
	if entry.WriteLike || entry.ShellLike || entry.Delegation {
		return false
	}
	return true
}

// DependenciesOf 返回某工具声明的依赖（canonical 归一后），供 doctor、
// banner 与测试使用，避免调用方各写一份查表逻辑。
func DependenciesOf(name string) []toolregistry.ToolDependency {
	entry, ok := toolregistry.LookupLive(strings.TrimSpace(name))
	if !ok {
		return nil
	}
	return entry.Dependencies
}
