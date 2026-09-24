package profile

import (
	"fmt"
	"sort"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

// runtime.overrides 语义（Batch 7 / D12 模式 B、D13 会话级 overlay、D14 覆盖白名单）。
//
// 键路径语法：点分隔的键段（`aicli.chat.stream`），始终从配置文档根开始。
// 不支持转义；键段本身不得为空。大小写不敏感匹配（与 MergeConfigYAML 的
// 大小写折叠语义一致）。
//
// 合并语义**不在这里重新定义**：即 agentconfig.MergeConfigYAML——嵌套 map 递归
// 合并，标量与 slice 由 overlay 整值替换，overlay 未写的键保持 base 值。因此特殊
// 值的含义是：
//
//   - 标量（字符串/数字/布尔）：替换 base 标量；
//   - 列表：整表替换（绝不追加）；
//   - 空映射 `{}`：与 MergeConfigYAML 一致，**不改变 base**（等价于未声明）；
//     需要屏蔽整棵子树请用 `null`（校验器会对 `{}` 给出 warning，避免误以为
//     它能清空）；
//   - `null`：写入显式 null，屏蔽 base 值（解码为零值），即"删除/屏蔽"。
const (
	// OverrideOriginProfile 是覆盖键在 origins 中的来源标记。复用既有
	// origins map[string]string 结构（runtimeserver/config_document.go），
	// 不新增第二套格式（Batch 7 任务 3）。
	OverrideOriginProfile = "profile"
)

// overrideAllowRules 是 D14 放行域。规则按"前缀 + 段位相等"匹配：规则覆盖该
// 路径本身及其所有子键；`*` 匹配恰好一个键段。默认拒绝——不在白名单内的键
// 一律报错，避免"配了不生效"的 dormant 陷阱（R9）。
var overrideAllowRules = [][]string{
	{"aicli", "chat"},
	{"aicli", "log"},
	{"aicli", "retry"},
	{"aicli", "timeout"},
	{"aicli", "balance"},
	{"aicli", "theme"},
	{"aicli", "model_cards"},
	{"aicli", "subagents"},
	{"aicli", "teams"},
	{"aicli", "main_agent"},
	{"aicli", "mcp", "config_file"},
	{"providers", "default_provider"},
	{"providers", "model_aliases"},
}

// skillsRuntimeAllowKeys 是**根级** `skills_runtime.<key>` 的字段级白名单
// （Batch 9 / V11 / Q9）。注意路径从配置根开始：`SkillsRuntime` 挂在根节点
// （agentconfig/config.go:42），`AICLIConfig` **没有** skills_runtime 节
// （config.go:525-541）——Batch 7 曾写成 `aicli.skills_runtime.*`，该路径在
// schema 中不存在（dormant/R9），Batch 9 修正为根级路径。
//
// 只放行 aicli 会话相关的非密钥字段；`admin_token` / `jwt_secret` /
// `api_key_scopes` 等集成与安全字段由 overrideDenyRules 显式拒绝，其余字段
// 默认拒绝（D14）。
var skillsRuntimeAllowKeys = map[string]struct{}{
	"enabled":                    {},
	"skill_dir":                  {},
	"skill_dirs":                 {},
	"extra_skill_dirs":           {},
	"aicli_skill_exposure_mode":  {},
	"aicli_skill_exposure_top_k": {},
}

// providerItemAllowFields 是 `providers.items.<name>.<field>` 的字段级白名单
// （Q9 结论：放行非密钥/非端点字段）。未列出的字段默认拒绝。
var providerItemAllowFields = map[string]struct{}{
	"enabled":                    {},
	"type":                       {},
	"protocol":                   {},
	"compatibility":              {},
	"api_path":                   {},
	"default_model":              {},
	"supported_models":           {},
	"models_path":                {},
	"models_verified_at":         {},
	"model_mappings":             {},
	"model_capabilities":         {},
	"support_types":              {},
	"response_marker_rules":      {},
	"max_tokens_limit":           {},
	"max_token":                  {},
	"supports_max_output_tokens": {},
	"timeout":                    {},
	"requests_per_minute":        {},
	"enable_image_generation":    {},
}

// overrideDenyRules 是 D14 的硬拒绝域（安全/信任边界）。deny 先于 allow 判定，
// 命中即报错；显式列出是为了给出可执行的报错信息（而不是笼统的"不在白名单"）。
var overrideDenyRules = [][]string{
	{"skills_runtime", "admin_token"},
	{"skills_runtime", "jwt_secret"},
	{"skills_runtime", "api_key_scopes"},
	{"aicli", "auth"},
	{"aicli", "foldertrust"},
	{"foldertrust"},
	{"runtime", "mode"},
	{"runtime", "server_url"},
	{"providers", "items", "*", "api_key"},
	{"providers", "items", "*", "api_keys"},
	{"providers", "items", "*", "api_key_ref"},
	{"providers", "items", "*", "account_auth_ref"},
	{"providers", "items", "*", "auth_mode"},
	{"providers", "items", "*", "auth_ref"},
	{"providers", "items", "*", "base_url"},
	{"providers", "items", "*", "forward_url"},
	{"providers", "items", "*", "headers"},
	{"providers", "items", "*", "header_mappings"},
	{"providers", "items", "*", "header_mapping_rules"},
	{"providers", "items", "*", "proxy"},
	{"providers", "items", "*", "account"},
}

// ParseOverridePath 把点分隔键路径拆成键段。空路径与空段都是错误（"解析不了
// 就报错"，绝不静默丢弃）。
func ParseOverridePath(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("覆盖键路径为空")
	}
	segments := strings.Split(trimmed, ".")
	path := make([]string, 0, len(segments))
	for _, segment := range segments {
		value := strings.TrimSpace(segment)
		if value == "" {
			return nil, fmt.Errorf("覆盖键路径 %q 含空键段", raw)
		}
		path = append(path, value)
	}
	return path, nil
}

// OverrideKeyList 返回覆盖键树的叶子路径（点分隔、已排序）。空映射 `{}` 也记为
// 该路径本身的一个叶子（它是一个被声明的键，只是合并时无操作）。返回顺序稳定，
// 便于报告与断言。
func OverrideKeyList(overrides map[string]interface{}) []string {
	leaves := overrideLeaves(overrides)
	if len(leaves) == 0 {
		return nil
	}
	keys := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		keys = append(keys, leaf.path)
	}
	return keys
}

// overrideLeaf 是一个覆盖叶子（最深层被声明的键）及其原始值。
type overrideLeaf struct {
	path  string
	value interface{}
}

func overrideLeaves(overrides map[string]interface{}) []overrideLeaf {
	if len(overrides) == 0 {
		return nil
	}
	leaves := make([]overrideLeaf, 0, len(overrides))
	collectOverrideLeaves(overrides, "", &leaves)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].path < leaves[j].path })
	return leaves
}

func collectOverrideLeaves(node map[string]interface{}, prefix string, out *[]overrideLeaf) {
	names := make([]string, 0, len(node))
	for name := range node {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if child, ok := node[name].(map[string]interface{}); ok && len(child) > 0 {
			collectOverrideLeaves(child, path, out)
			continue
		}
		*out = append(*out, overrideLeaf{path: path, value: node[name]})
	}
}

// OverrideOrigins 把每个覆盖叶子标记为 profile 来源（复用 origins 结构）。
func OverrideOrigins(overrides map[string]interface{}) map[string]string {
	keys := OverrideKeyList(overrides)
	if len(keys) == 0 {
		return nil
	}
	origins := make(map[string]string, len(keys))
	for _, key := range keys {
		origins[key] = OverrideOriginProfile
	}
	return origins
}

// ValidateOverrides 校验覆盖键树（D14）。返回空切片表示全部放行。该校验器是
// 单一权威：`profile validate`（Batch 2）与运行时解析（resolver）共用它，
// 因此不存在"文档约定"与"实际行为"两套口径。
func ValidateOverrides(overrides map[string]interface{}) []ProfileSpecIssue {
	if len(overrides) == 0 {
		return nil
	}
	issues := make([]ProfileSpecIssue, 0, 4)
	for _, leaf := range overrideLeaves(overrides) {
		key := leaf.path
		path, err := ParseOverridePath(key)
		if err != nil {
			issues = append(issues, ProfileSpecIssue{
				Severity: ProfileSpecIssueError,
				Path:     "runtime.overrides." + key,
				Message:  err.Error(),
			})
			continue
		}
		if empty, ok := leaf.value.(map[string]interface{}); ok && len(empty) == 0 {
			// `{}` 在 MergeConfigYAML 下是"无操作"，不是"清空"：给出 warning，
			// 避免用户以为它屏蔽了 base（要屏蔽请用 null）。
			issues = append(issues, ProfileSpecIssue{
				Severity: ProfileSpecIssueWarning,
				Path:     "runtime.overrides." + key,
				Message:  "空映射 {} 不会清空 base 值（等价于未声明）；如需屏蔽该子树请写 null",
			})
		}
		if rule := matchOverrideRule(overrideDenyRules, path); rule != nil {
			issues = append(issues, ProfileSpecIssue{
				Severity: ProfileSpecIssueError,
				Path:     "runtime.overrides." + key,
				Message: fmt.Sprintf("安全域键不可被 profile 覆盖（命中禁止规则 %q）：profile 只能收窄安全基线，不能改写密钥/端点/信任边界",
					strings.Join(rule, ".")),
			})
			continue
		}
		if !overridePathAllowed(path) {
			issues = append(issues, ProfileSpecIssue{
				Severity: ProfileSpecIssueError,
				Path:     "runtime.overrides." + key,
				Message:  "不在 profile 覆盖白名单内（D14）；允许的键域见 docs/aicli/profiles.md「配置覆盖」一节",
			})
		}
	}
	return issues
}

// ValidateOverridesError 返回聚合后的单个错误（合法时为 nil）。只有 error 级
// 问题会阻断（warning 仅供 `profile validate` 呈现，例如 `{}` 无操作提示）。
// 供 resolver 在解析期执行同一份白名单校验（双执行，D14）。
func ValidateOverridesError(overrides map[string]interface{}) error {
	issues := ValidateOverrides(overrides)
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Severity != ProfileSpecIssueError {
			continue
		}
		messages = append(messages, fmt.Sprintf("%s: %s", issue.Path, issue.Message))
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidProfileSpec, strings.Join(messages, "; "))
}

// CloneOverrides 深拷贝覆盖键树，避免解析结果与会话状态共享可变映射。
func CloneOverrides(overrides map[string]interface{}) map[string]interface{} {
	if len(overrides) == 0 {
		return nil
	}
	clone := make(map[string]interface{}, len(overrides))
	for key, value := range overrides {
		clone[key] = cloneOverrideValue(value)
	}
	return clone
}

func cloneOverrideValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return CloneOverrides(typed)
	case []interface{}:
		cloned := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			cloned = append(cloned, cloneOverrideValue(item))
		}
		return cloned
	default:
		return value
	}
}

// MergeOverridesIntoYAML 把覆盖键树合并进 base YAML 文档（D13 会话级 overlay
// 的合并步骤）。overrides 为空时**原样返回** baseYAML，保证"无覆盖 = 逐字节零
// 变化"（NFR-1）。
func MergeOverridesIntoYAML(baseYAML []byte, overrides map[string]interface{}) ([]byte, error) {
	if len(overrides) == 0 {
		return baseYAML, nil
	}
	if err := ValidateOverridesError(overrides); err != nil {
		return nil, err
	}
	overlayYAML, err := yaml.Marshal(overrides)
	if err != nil {
		return nil, fmt.Errorf("encode profile overrides: %w", err)
	}
	merged, err := config.MergeConfigYAML(baseYAML, overlayYAML)
	if err != nil {
		return nil, fmt.Errorf("merge profile overrides: %w", err)
	}
	return merged, nil
}

func overridePathAllowed(path []string) bool {
	if len(path) >= 4 && strings.EqualFold(path[0], "providers") && strings.EqualFold(path[1], "items") {
		_, ok := providerItemAllowFields[strings.ToLower(path[3])]
		return ok
	}
	if len(path) >= 2 && strings.EqualFold(path[0], "skills_runtime") {
		_, ok := skillsRuntimeAllowKeys[strings.ToLower(path[1])]
		return ok
	}
	return matchOverrideRule(overrideAllowRules, path) != nil
}

func matchOverrideRule(rules [][]string, path []string) []string {
	for _, rule := range rules {
		if overrideRuleMatches(rule, path) {
			return rule
		}
	}
	return nil
}

func overrideRuleMatches(rule []string, path []string) bool {
	if len(path) < len(rule) {
		return false
	}
	for index, segment := range rule {
		if segment == "*" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(path[index]), segment) {
			return false
		}
	}
	return true
}
