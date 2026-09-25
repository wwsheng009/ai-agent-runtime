package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
)

// mcpConfigOverride 返回 MCP 配置的「显式覆盖值」：
// --config-file > MCP_CONFIG_FILE / aicli.mcp.config_file。
//
// 为空表示走发现链的分层合并（用户级基础 + 项目级增量，§4.5 Step 1）；
// 非空时 aiclipaths 会区分「真实覆盖」与「约定路径」——后者仍按发现链处理。
func mcpConfigOverride() string {
	if trimmed := strings.TrimSpace(mcpConfigFile); trimmed != "" {
		return trimmed
	}
	return agentconfig.EffectiveAICLIMCPConfigFile(agentconfig.GetGlobalConfig())
}

// loadMCPConfigLayered 按发现链分层加载配置（同名高优先级整体覆盖）。
func loadMCPConfigLayered() (*mcpconfig.LayeredResult, error) {
	result, err := mcpconfig.LoadEffective(mcpConfigOverride())
	if err != nil {
		if errors.Is(err, mcpconfig.ErrNoConfigFiles) {
			return nil, fmt.Errorf("找不到 MCP 配置文件\n请创建配置文件或使用 --config 指定")
		}
		return nil, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	return result, nil
}

// loadMCPManagerLayered 让 manager 使用分层合并后的配置。
//
// 实现 LayeredConfigLoader 的 manager 走合并路径；Win7 兼容等禁用实现没有
// 该能力，直接返回 nil（与它们「MCP 整体禁用」的语义一致）。
func loadMCPManagerLayered(mgr manager.Manager) error {
	if mgr == nil {
		return fmt.Errorf("MCP 管理器为空")
	}
	loader, ok := mgr.(manager.LayeredConfigLoader)
	if !ok || loader == nil {
		return nil
	}
	if err := loader.LoadConfigEffective(mcpConfigOverride()); err != nil {
		if errors.Is(err, mcpconfig.ErrNoConfigFiles) {
			return fmt.Errorf("找不到 MCP 配置文件\n请创建配置文件或使用 --config 指定")
		}
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	return nil
}

// emitMCPConfigWarnings 把分层加载的非致命告警提示给用户（如低优先级文件损坏被跳过）。
func emitMCPConfigWarnings(mgr manager.Manager) {
	reporter, ok := mgr.(manager.ConfigOriginReporter)
	if !ok || reporter == nil {
		return
	}
	for _, warning := range reporter.MCPConfigWarnings() {
		fmt.Fprintf(os.Stderr, "警告: %s\n", warning)
	}
}

// mcpConfigSourceLabel 把来源层级渲染为可读标签，用于 list/status 的「来源」列。
func mcpConfigSourceLabel(source, path string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if path == "" {
		return source
	}
	return fmt.Sprintf("%s (%s)", source, path)
}

// ---- M3 Step 2：--scope 写入目标与项目级秘密剥离 ----

// MCP 写入 scope（§4.5 Step 2）：
//   - user    → ~/.aicli/mcp.yaml（个人全局，默认落点）
//   - local   → ~/.aicli/projects/<slug>/mcp.yaml（项目私有，不进版本库）
//   - project → <项目根>/.aicli/mcp.yaml（可提交共享，禁止明文凭证）
const (
	mcpWriteScopeUser    = "user"
	mcpWriteScopeLocal   = "local"
	mcpWriteScopeProject = "project"
)

// validMCPWriteScopes 供 cobra 帮助与校验共用。
var validMCPWriteScopes = []string{mcpWriteScopeUser, mcpWriteScopeLocal, mcpWriteScopeProject}

// parseMCPWriteScope 归一化 --scope 取值；空串表示沿用既有写入路径解析。
func parseMCPWriteScope(scope string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	if normalized == "" {
		return "", nil
	}
	for _, candidate := range validMCPWriteScopes {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("不支持的 --scope '%s'，可选: user, local, project", scope)
}

// resolveMCPWritePathForScope 计算写入目标文件；scope 为空时保持既有语义
// （优先已存在的配置文件，否则用户级）。
func resolveMCPWritePathForScope(scope string) (string, error) {
	normalized, err := parseMCPWriteScope(scope)
	if err != nil {
		return "", err
	}
	switch normalized {
	case "":
		return resolveMCPConfigPathForWrite(), nil
	case mcpWriteScopeUser:
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, ".aicli", "mcp.yaml"), nil
		}
		return resolveMCPConfigPathForWrite(), nil
	case mcpWriteScopeLocal:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("解析当前目录失败: %w", err)
		}
		return aiclipaths.LocalMCPConfigPath(cwd)
	case mcpWriteScopeProject:
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("解析当前目录失败: %w", err)
		}
		return filepath.Join(cwd, ".aicli", "mcp.yaml"), nil
	default:
		return "", fmt.Errorf("不支持的 --scope '%s'", scope)
	}
}

// sensitiveHeaderKeys 命中这些关键字的 header/env 名视为凭证载体。
var sensitiveKeyFragments = []string{
	"authorization", "auth-token", "api-key", "apikey", "api_key",
	"access-key", "secret", "password", "passwd", "token", "credential", "cookie",
}

// looksSensitiveKey 判断 header/env 名是否像凭证（大小写与分隔符不敏感）。
func looksSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("_", "-", ".", "-").Replace(normalized)
	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// hasEnvReference 判断取值是否已经写成运行时插值引用（`${VAR}` / `${VAR:-默认值}` / `$${VAR}`）。
func hasEnvReference(value string) bool {
	return strings.Contains(value, "${")
}

// sensitiveWriteFinding 描述一条「会落盘的明文凭证」。
type sensitiveWriteFinding struct {
	Field string
	Key   string
}

// scanSensitiveFindings 检查请求里是否有明文凭证（headers/env/auth.clientSecret）。
//
// 判定规则：key 像凭证 且 取值非空且不含 `${...}` 引用 → 视为明文。
// `Bearer ${MY_TOKEN}` 这类「前缀 + 引用」是允许的。
func scanSensitiveFindings(req mcpadmin.UpsertRequest) []sensitiveWriteFinding {
	findings := make([]sensitiveWriteFinding, 0)
	scan := func(field string, values map[string]string) {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !looksSensitiveKey(key) {
				continue
			}
			value := strings.TrimSpace(values[key])
			if value == "" || hasEnvReference(value) {
				continue
			}
			findings = append(findings, sensitiveWriteFinding{Field: field, Key: key})
		}
	}
	scan("header", req.Headers)
	scan("env", req.Env)
	if req.Auth != nil && strings.TrimSpace(req.Auth.ClientSecret) != "" {
		findings = append(findings, sensitiveWriteFinding{Field: "auth.clientSecret", Key: "clientSecret"})
	}
	return findings
}

// validateProjectScopeSecrets 拒绝把明文凭证写入可提交的项目级配置。
//
// 选择「拒绝 + 可行动提示」而不是自动改写：自动改写需要猜测变量名，
// 反而容易生成 `MY_API_KEY` 与既有 env 引用冲突的半成品。
func validateProjectScopeSecrets(req mcpadmin.UpsertRequest, targetPath string) error {
	findings := scanSensitiveFindings(req)
	if len(findings) == 0 {
		return nil
	}
	details := make([]string, 0, len(findings))
	for _, finding := range findings {
		details = append(details, fmt.Sprintf("%s %s", finding.Field, finding.Key))
	}
	return fmt.Errorf(
		"项目级配置（%s）不接受明文凭证：%s\n可改用：\n"+
			"  1) 写成 ${VAR} 引用，例如 --header \"Authorization=Bearer ${MY_TOKEN}\"；\n"+
			"  2) 保存到个人层：--scope user（或 --scope local，项目私有且不进版本库）",
		targetPath, strings.Join(details, "、"))
}

// emitMCPProjectScopeNotice 提示项目级配置的提交与 gitignore 注意事项。
func emitMCPProjectScopeNotice(targetPath string) {
	fmt.Fprintf(os.Stderr,
		"提示: 已写入项目级配置 %s（可提交共享）。若仓库 .gitignore 忽略了 .aicli/，请追加豁免：!.aicli/mcp.yaml\n",
		targetPath)
}

// emitMCPShadowNotice 提示同名 server 已存在于其它层级，避免静默覆盖。
func emitMCPShadowNotice(name, targetPath string) {
	result, err := loadMCPConfigLayered()
	if err != nil || result == nil {
		return
	}
	origin, ok := result.Origin(name)
	if !ok || filepath.Clean(origin.Path) == filepath.Clean(targetPath) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"注意: MCP '%s' 已定义于 %s 层（%s）；本次写入 %s 将在其之上覆盖（%s > %s）\n",
		name, origin.Source, origin.Path, targetPath, targetPath, origin.Path)
}

// locateMCPServerConfigFile 在发现链中定位定义 name 的最高优先级文件；
// 找不到时返回空串（调用方回退到常规写入路径）。
func locateMCPServerConfigFile(name string) string {
	result, err := loadMCPConfigLayered()
	if err != nil || result == nil {
		return ""
	}
	if origin, ok := result.Origin(strings.TrimSpace(name)); ok {
		return origin.Path
	}
	return ""
}

// newMCPAdminServiceForServer 让启停/删除作用在「实际定义该 server 的文件」上：
// 分层合并下，用户级定义的 server 不应因为项目文件存在而被写错层级。
func newMCPAdminServiceForServer(name string, apply bool) *mcpadmin.Service {
	if path := locateMCPServerConfigFile(name); path != "" {
		return mcpadmin.NewService(path, mcpadmin.WithApplyOnMutate(apply))
	}
	return newMCPAdminService(apply)
}
