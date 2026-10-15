// Package configaudit 审计多层配置（bootstrap config.yaml 分层 + 同目录
// connectors.secrets.yaml）之间同一连接器的「密钥分叉」，供宿主启动时告警。
//
// 背景（2026-10-15 真机 qq-bot1）：连接器配置随进程启动目录解析到不同层
// （./.aicli/config.yaml 项目层优先于 $HOME/.aicli/config.yaml 用户层），
// 同一 id 的密钥在各层各自维护——修改一层不会影响另一层；两层分叉时，
// 「重启后错误复现」且从单侧状态无法察觉。
//
// 本包只读不写：只报连接器 id 与层路径，绝不输出密钥取值。
package configaudit

import (
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
)

// Divergence 描述一个连接器 id 跨配置层的密钥分叉。
type Divergence struct {
	// ConnectorID 是同时出现在多个层且密钥不一致的连接器 id。
	ConnectorID string
	// Layers 是承载该 id 的层配置文件路径（去重，按传入顺序）；仅用于告警展示。
	Layers []string
	// DistinctSecrets 是涉及的密钥取值指纹数量（>=2；不携带取值本身）。
	DistinctSecrets int
}

// DetectSecretsDivergence 检查候选配置文件列表，返回存在密钥分叉的连接器。
// 语义：
//   - 不存在的层路径跳过（与配置解析「只认已存在文件」一致）；
//   - 同一路径重复出现去重；层读取失败（坏 YAML 等）按不提供配置处理；
//   - 同 id 只出现在一层 → 不算分叉；
//   - 同 id 出现在多层且密钥指纹有 ≥2 种 → 分叉（含「一层有密钥、另一层为空」）；
//   - 只比较密钥集合，不比较 settings/policy（后者按层覆盖是分层设计特性）。
func DetectSecretsDivergence(configPaths []string) []Divergence {
	type layerHit struct {
		path        string
		fingerprint string
	}
	seen := make(map[string]struct{})
	hits := make(map[string][]layerHit)
	order := make([]string, 0)
	for _, raw := range configPaths {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		configs := storefile.NewConfigStore(path, "").ListConfigs()
		for _, cfg := range configs {
			id := strings.TrimSpace(cfg.ID)
			if id == "" {
				continue
			}
			if _, ok := hits[id]; !ok {
				order = append(order, id)
			}
			hits[id] = append(hits[id], layerHit{path: path, fingerprint: secretsFingerprint(cfg.Secrets)})
		}
	}
	out := make([]Divergence, 0)
	for _, id := range order {
		entries := hits[id]
		if len(entries) < 2 {
			continue
		}
		distinct := make(map[string]struct{}, len(entries))
		layers := make([]string, 0, len(entries))
		for _, entry := range entries {
			distinct[entry.fingerprint] = struct{}{}
			layers = append(layers, entry.path)
		}
		if len(distinct) < 2 {
			continue
		}
		out = append(out, Divergence{ConnectorID: id, Layers: layers, DistinctSecrets: len(distinct)})
	}
	return out
}

// WarnSecretsDivergence 把分叉结果写入启动告警日志（logf 为 nil 时只计算不输出）。
func WarnSecretsDivergence(configPaths []string, logf func(format string, args ...any)) {
	if logf == nil {
		return
	}
	for _, d := range DetectSecretsDivergence(configPaths) {
		logf("connectors: 密钥分叉告警：连接器 %s 在 %d 个配置层同时存在且密钥不一致；"+
			"不同启动目录的进程会读取不同凭据、修改一层不会影响另一层，请统一（层：%s）",
			d.ConnectorID, len(d.Layers), strings.Join(d.Layers, "、"))
	}
}

// secretsFingerprint 生成密钥集合的稳定指纹（供跨层比较；只存在于内存，不落日志）。
func secretsFingerprint(secrets map[string]string) string {
	if len(secrets) == 0 {
		return ""
	}
	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(secrets[key])
		builder.WriteString("\x00")
	}
	return builder.String()
}
