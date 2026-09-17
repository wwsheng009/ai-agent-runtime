package runtimeserver

import (
	"path/filepath"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

// NewRuntimeConfigLayersProvider 返回 runtime.yaml 的层栈快照（P2），让设置页能展示
// 「配置可能来自哪些文件、哪一层只读、写入会落到哪一层」。
//
// 与 config.yaml 的 layers 字段同构：存在层给绝对路径，未创建的候选层保留相对路径
// （用户级候选即使不存在也会出现，因为首次写入会创建它）。
func NewRuntimeConfigLayersProvider() skillsapi.RuntimeConfigLayersProvider {
	return func() []skillsapi.ConfigDocumentLayer {
		layers := config.RuntimeConfigLayerStack()
		out := make([]skillsapi.ConfigDocumentLayer, 0, len(layers))
		for _, layer := range layers {
			path := layer.Path
			if layer.Present {
				if absolute, err := filepath.Abs(path); err == nil && absolute != "" {
					path = absolute
				}
			}
			out = append(out, skillsapi.ConfigDocumentLayer{
				Kind:     string(layer.Kind),
				Path:     filepath.Clean(path),
				Present:  layer.Present,
				ReadOnly: layer.ReadOnly,
			})
		}
		return out
	}
}
