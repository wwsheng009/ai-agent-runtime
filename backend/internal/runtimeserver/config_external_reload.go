package runtimeserver

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"gopkg.in/yaml.v3"
)

// ConfigExternalReloadPollInterval 是外部改动感知的默认轮询间隔。
//
// 每次轮询只对分层配置来源文件做几次 os.Stat（不读文件内容、不解析 YAML），代价远低于
// 一次 HTTP 请求；把「外部进程写入 / 手工编辑配置文件后必须重启 runtime-server 才生效」
// 的陈旧窗口从「直到重启」收敛到最多一个间隔。
const ConfigExternalReloadPollInterval = 2 * time.Second

// ConfigExternalReloader 感知「不经 config document API」的配置来源改动，并把新配置
// 走**同一条**热重载路径应用到进程内（RuntimeConfigHotReloader.Apply + 变更路径分类）。
//
// 背景：config document API 的写入会通过 SetHotReloader 立即刷新进程内快照，而另一个
// aicli 进程、外部工具或手工编辑配置文件都不会经过那条路径，于是 GET/PATCH 投影、主
// Agent 接线会一直用旧快照（需重启）。本类型只补「检测 + 复用同一条应用路径」，不新建
// 第二套生效语义：变更路径仍由 analyze/classify 决定哪些可热重载、哪些要重启，warning
// 文案与 API 写入一致。
//
// 容错：外部写入可能是半写入的、非法的，或有并发写入者：此时保持旧快照并给出 warning，
// 绝不 panic、绝不让服务不可用；下一次签名变化会重新尝试。
type ConfigExternalReloader struct {
	reloader  *RuntimeConfigHotReloader
	load      func() (*agentconfig.Config, error)
	signature func() string
	interval  time.Duration

	mu   sync.Mutex
	last string
	seen bool
}

// NewConfigExternalReloader 构造外部改动感知器：
//   - reloader：与 config document API 共用的热重载器（nil 时返回 nil，调用方无需分支）；
//   - load：重新加载生效配置的函数（应与进程启动时同一加载器，例如
//     LoadRuntimeAgentConfig；注入以便测试）；
//   - signature：来源文件签名函数；nil 时用 ConfigSourceSignature；
//   - interval：轮询间隔；<=0 时用 ConfigExternalReloadPollInterval。
func NewConfigExternalReloader(
	reloader *RuntimeConfigHotReloader,
	load func() (*agentconfig.Config, error),
	signature func() string,
	interval time.Duration,
) *ConfigExternalReloader {
	if reloader == nil || load == nil {
		return nil
	}
	if signature == nil {
		signature = ConfigSourceSignature
	}
	if interval <= 0 {
		interval = ConfigExternalReloadPollInterval
	}
	return &ConfigExternalReloader{
		reloader:  reloader,
		load:      load,
		signature: signature,
		interval:  interval,
	}
}

// Run 在 ctx 生命周期内轮询来源签名，直到 ctx 取消（阻塞；调用方自行起 goroutine）。
//
// 启动时先取一次基线：进程启动时配置已由 LoadRuntimeAgentConfig 加载并注入，这里只
// 记录签名，不重复应用一次。
func (r *ConfigExternalReloader) Run(ctx context.Context) {
	if r == nil || ctx == nil {
		return
	}
	r.PollOnce()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			applied, result := r.PollOnce()
			for _, warning := range result.Warnings {
				logger.Warn("config external change: "+warning,
					logger.String("source", "config_source_poll"))
			}
			if applied {
				logger.Info("config external change applied",
					logger.String("applied_paths", strings.Join(result.AppliedPaths, ",")))
			}
		}
	}
}

// PollOnce 检查一次来源签名，返回 (是否有外部改动被应用, 热重载结果)。
//
// 语义：
//   - 首次调用只建立基线（返回 false，不加载、不应用）；
//   - 签名未变化：直接返回 false（零成本，不读文件）；
//   - 签名变化且加载成功：用变更路径调 Apply（与 API 写入同一条路径），返回 true；
//   - 签名变化但加载失败/返回 nil：保持旧快照，返回 false + warning（不中断服务）。
func (r *ConfigExternalReloader) PollOnce() (bool, ConfigDocumentHotReloadResult) {
	if r == nil || r.reloader == nil || r.load == nil || r.signature == nil {
		return false, ConfigDocumentHotReloadResult{}
	}
	signature := r.signature()
	r.mu.Lock()
	previous, seen := r.last, r.seen
	r.last, r.seen = signature, true
	r.mu.Unlock()
	if !seen || signature == previous {
		return false, ConfigDocumentHotReloadResult{}
	}

	next, err := r.load()
	if err != nil || next == nil {
		warning := "外部配置改动未能重载，继续使用进程内快照"
		if err != nil {
			warning += "：" + err.Error()
		}
		return false, ConfigDocumentHotReloadResult{Warnings: []string{warning}}
	}

	impact, err := configDocumentRuntimeImpactForConfigs(r.reloader.currentCfg, next)
	if err != nil {
		return false, ConfigDocumentHotReloadResult{Warnings: []string{
			"外部配置改动已加载，但无法与进程内快照比对变更路径，继续使用进程内快照：" + err.Error(),
		}}
	}

	// 与 config document API 保存流程同形：只把「可热重载」路径交给 Apply，
	// 需重启 / 当前进程不影响的路径由同一套 buildConfigDocumentWarnings 生成 warning。
	hotPaths := []string(nil)
	if impact != nil {
		hotPaths = impact.HotReloadPaths
	}
	result := r.reloader.Apply(next, hotPaths)
	if impact != nil {
		result.Warnings = buildConfigDocumentWarnings(impact, result.AppliedPaths, result.Warnings)
	}
	return true, result
}

// configDocumentRuntimeImpactForConfigs 复用 config document API 写入时的同一套判据：
// 把两份 typed config 归一化成与磁盘 YAML 同键空间的文档，再交给
// analyzeConfigDocumentRuntimeImpact 分类（即时热重载 / 需重启 / 当前进程不影响）。
//
// 为什么不直接 diff struct：变更路径必须落在 API 写入用的同一路径空间
// （aicli.teams.routing.levels.expert.model 这类 YAML 键路径），否则热/冷判据会退化成
// 整份配置级的粗判，两边「什么能热重载」就不再是同一套结论。两侧配置都来自同一个加载器
// （启动时 LoadRuntimeAgentConfig、改动后同一个 load），派生字段一致，yaml:"-" 的诊断
// 字段也不参与归一化，因此不会产生虚假差异。
func configDocumentRuntimeImpactForConfigs(
	current *agentconfig.Config,
	next *agentconfig.Config,
) (*skillsapi.ConfigDocumentRuntimeImpact, error) {
	if current == nil || next == nil {
		return nil, nil
	}
	currentDocument, err := configDocumentValueFromConfig(current)
	if err != nil {
		return nil, err
	}
	nextDocument, err := configDocumentValueFromConfig(next)
	if err != nil {
		return nil, err
	}
	return analyzeConfigDocumentRuntimeImpact(currentDocument, nextDocument), nil
}

// configDocumentValueFromConfig 把 typed config 归一化成文档值（与 config document API
// 解析磁盘 YAML 得到的 map[string]interface{} 同形）。
func configDocumentValueFromConfig(cfg *agentconfig.Config) (map[string]interface{}, error) {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	return document, nil
}

// ConfigSourceSignature 返回默认来源集合的签名串（顺序稳定）：分层搜索栈 + 预设层。
// 进程若用 `--config` 指定了显式路径，用 ConfigSourceSignatureFor 把该路径一并纳入。
func ConfigSourceSignature() string {
	return ConfigSourceSignatureFor("")
}

// ConfigSourceSignatureFor 返回「这份运行实例实际读取的全部来源」的签名串：
// 显式配置路径（runtime-server --config 解析后的路径）+ 分层搜索栈
// （agentconfig.ConfigLayerStack）+ 预设层（用户预设文件、系统预设目录下的文件）。
//
// 来源清单与加载器的输入闭包同源（LoadRuntimeAgentConfig：分层合并 + MergeWithPresets），
// 不另立第二套搜索顺序：任何一层的新增、删除、大小或修改时间变化都会改变签名。内置预设
// 编译在二进制内（preset.go 的 go:embed），没有可观察的文件，因此只能靠版本升级改变。
// 签名只用于「是否变了」的判定，不承载内容语义——真正的内容校验由加载器负责
// （解析失败会走保持旧快照的降级路径）。
func ConfigSourceSignatureFor(configPath string) string {
	return configSourceSignatureForPaths(configSourceWatchPaths(configPath))
}

// configSourceWatchPaths 汇总运行实例的全部配置来源路径（去重、去空）。
//
// 显式路径排在最前：它可能与搜索栈里的某一层是同一个文件（例如默认的
// `configs/aicli.yaml`），去重后不影响集合内容，只保证不重复 stat。
// 相对路径（搜索栈候选）按进程 cwd 解析——与加载器一致：runtime-server 启动后不再
// chdir，因此轮询期与加载期看到的是同一组文件。
func configSourceWatchPaths(configPath string) []string {
	candidates := make([]string, 0, 12)
	if path := strings.TrimSpace(configPath); path != "" {
		candidates = append(candidates, path)
	}
	candidates = append(candidates, configSourceLayerPaths()...)
	if path := strings.TrimSpace(agentconfig.UserPresetsPath()); path != "" {
		candidates = append(candidates, path)
	}
	// 系统预设目录按文件参与合并（preset.go 的 LoadSystemPresets + MergeWithPresets），
	// 因此目录里任何一个文件的增删改都算来源变化；目录不存在时跳过（不算改动）。
	if dir := strings.TrimSpace(agentconfig.SystemPresetDir()); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				candidates = append(candidates, filepath.Join(dir, entry.Name()))
			}
		}
	}

	seen := make(map[string]struct{}, len(candidates))
	paths := make([]string, 0, len(candidates))
	for _, path := range candidates {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

// configSourceLayerPaths 返回分层配置的来源文件路径（去空、保持层栈顺序）。
func configSourceLayerPaths() []string {
	layers := agentconfig.ConfigLayerStack()
	paths := make([]string, 0, len(layers))
	for _, layer := range layers {
		if path := strings.TrimSpace(layer.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// configSourceSignatureForPaths 对给定路径集合生成稳定签名：按路径排序后逐文件取
// 「存在性 / 是否目录 / 大小 / 修改时间」。文件不存在也进签名（新建一层同样是改动）。
func configSourceSignatureForPaths(paths []string) string {
	sorted := make([]string, 0, len(paths))
	for _, path := range paths {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			sorted = append(sorted, trimmed)
		}
	}
	sort.Strings(sorted)

	var builder strings.Builder
	for _, path := range sorted {
		info, err := os.Stat(path)
		switch {
		case err != nil:
			builder.WriteString(path + "|absent;")
		case info.IsDir():
			builder.WriteString(path + "|dir;")
		default:
			builder.WriteString(path + "|")
			builder.WriteString(strconv.FormatInt(info.Size(), 10))
			builder.WriteString("|")
			builder.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
			builder.WriteString(";")
		}
	}
	return builder.String()
}
