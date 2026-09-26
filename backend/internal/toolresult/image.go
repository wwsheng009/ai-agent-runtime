package toolresult

import "strings"

// 图片直通元数据契约：读工具（view）命中图片时声明以下键，agent 在写完该批
// tool_result 后把图片作为一条附带 input_images 的 user 消息注入下一次 provider
// 请求——provider 协议只在 user 角色渲染 image block，tool 消息只能携带文本。
const (
	// MetadataImagePassthroughKey=true 表示该结果附带可发送的图片。
	MetadataImagePassthroughKey = "image_passthrough"
	// MetadataImagePathKey 是实际发送的图片路径（可能已缩放/转码）。
	MetadataImagePathKey = "image_path"
	// MetadataImageMimeTypeKey 是发送图片的 MIME 类型。
	MetadataImageMimeTypeKey = "image_mime_type"
	// MetadataImageWidthKey / MetadataImageHeightKey 是发送图片的像素尺寸。
	MetadataImageWidthKey  = "image_width"
	MetadataImageHeightKey = "image_height"
	// MetadataImageBytesKey 是发送图片的字节数。
	MetadataImageBytesKey = "image_bytes"
	// MetadataImageNoteKey 是一行可展示说明（缩放比例、体积变化等）。
	MetadataImageNoteKey = "image_note"
	// MetadataImagePathsKey 列出一个结果携带的额外图片路径（notebook 的多个
	// 输出、批量 items 提升等）。MetadataImagePathKey 仍是主附件，本键是可加
	// 的补充清单；两个键的路径都会被去重后附加。
	MetadataImagePathsKey = "image_paths"
)

// ImagePassthroughFromMetadata reports the prepared image path declared by a
// tool result, plus its one-line note. Both a flat key layout and the nested
// "tool_metadata" layout are honored (same convention as KindFromMetadata).
func ImagePassthroughFromMetadata(metadata map[string]interface{}) (string, string, bool) {
	scopes := []map[string]interface{}{metadata}
	if nested := nestedToolMetadata(metadata); len(nested) > 0 {
		scopes = append(scopes, nested)
	}
	for _, scope := range scopes {
		if len(scope) == 0 || !truthyMetadataFlag(scope, MetadataImagePassthroughKey) {
			continue
		}
		path, _ := scope[MetadataImagePathKey].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		note, _ := scope[MetadataImageNoteKey].(string)
		return path, strings.TrimSpace(note), true
	}
	return "", "", false
}

// ImagePassthroughPathsFromMetadata returns the additional image paths
// declared by a result (MetadataImagePathsKey), honoring both the flat and the
// nested "tool_metadata" layout and both the []string and []interface{} JSON
// shapes. Missing or blank entries are skipped; order follows the declaration.
func ImagePassthroughPathsFromMetadata(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	scopes := []map[string]interface{}{metadata}
	if nested := nestedToolMetadata(metadata); len(nested) > 0 {
		scopes = append(scopes, nested)
	}
	var paths []string
	seen := make(map[string]struct{})
	appendPath := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, duplicate := seen[candidate]; duplicate {
			return
		}
		seen[candidate] = struct{}{}
		paths = append(paths, candidate)
	}
	for _, scope := range scopes {
		// 补充清单同样受 passthrough 标志约束：没声明"可发送"的结果不应被
		// 顺手附加图片。
		if len(scope) == 0 || !truthyMetadataFlag(scope, MetadataImagePassthroughKey) {
			continue
		}
		switch raw := scope[MetadataImagePathsKey].(type) {
		case []string:
			for _, item := range raw {
				appendPath(item)
			}
		case []interface{}:
			for _, item := range raw {
				if text, ok := item.(string); ok {
					appendPath(text)
				}
			}
		}
	}
	return paths
}

func nestedToolMetadata(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	nested, _ := metadata["tool_metadata"].(map[string]interface{})
	return nested
}
