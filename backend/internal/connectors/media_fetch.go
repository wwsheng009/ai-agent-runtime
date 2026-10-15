package connectors

// 入站媒体物化（能力接口 + Service 侧编排）。
//
// 背景（docs/bots/04 「QQ 官方能力对照」缺口 P1）：Provider 解析出入站媒体
// 元数据（含下载 URL）后，由 Service 在**工作协程**统一物化到本地——QQ 的
// WS 心跳/事件循环与微信长轮询循环都不能被下载阻塞；下载完成后把 local_path
// 摘要注入文本（字段名与微信媒体实现对齐：file_name/content_type/local_path/
// source_url）。
//
// 约定：
//   - Provider 实现 MediaFetcher 才参与（未实现零变化）；
//   - 下载失败只记录日志并保留元数据/占位文本，不阻塞对话；
//   - Provider 若已在内部完成下载（media.Path 非空），Service 只补摘要。

import (
	"context"
	"strings"
	"time"
)

// inboundMediaFetchTimeout 是单条入站媒体下载的上限（Service 侧兜底）。
const inboundMediaFetchTimeout = 60 * time.Second

// MediaFetcher 是 Provider 的可选能力：把一条入站媒体下载到本地并返回
// 本地绝对路径。实现需自行做宿主安全校验（如 URL 出口白名单/大小上限）。
type MediaFetcher interface {
	Provider
	// FetchInboundMedia 下载 media.URL 指向的内容并落盘；返回绝对路径。
	FetchInboundMedia(ctx context.Context, cfg Config, media Media) (string, error)
}

// materializeInboundMedia 逐条物化入站媒体并把摘要追加到 msg.Text
// （在本地执行路径、Backend.Handle 之前调用；远程派发路径跳过——本地路径
// 对远端 runner 不可达）。
func (s *Service) materializeInboundMedia(ctx context.Context, cfg Config, provider Provider, msg *InboundMessage) {
	if msg == nil || len(msg.Media) == 0 {
		return
	}
	fetcher, ok := provider.(MediaFetcher)
	if !ok {
		return
	}
	blocks := make([]string, 0, len(msg.Media))
	for i := range msg.Media {
		media := &msg.Media[i]
		if strings.TrimSpace(media.Path) == "" {
			fetchCtx, cancel := context.WithTimeout(ctx, inboundMediaFetchTimeout)
			path, err := fetcher.FetchInboundMedia(fetchCtx, cfg, *media)
			cancel()
			if err != nil {
				s.recordLog(LogEntry{
					Level:       LogLevelWarn,
					Direction:   LogDirectionIn,
					ConnectorID: cfg.ID,
					Chat:        msg.Chat,
					Summary:     "入站媒体下载失败：" + err.Error(),
				}, logCounters{})
				// 失败也保留元数据摘要（无 local_path）：agent 仍可用 source_url
				// 自行获取，媒体不因下载失败而静默消失。
				if block := mediaSummaryBlock(*media); block != "" {
					blocks = append(blocks, block)
				}
				continue
			}
			media.Path = strings.TrimSpace(path)
		}
		if media.Path == "" {
			continue
		}
		if block := mediaSummaryBlock(*media); block != "" {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return
	}
	summary := strings.Join(blocks, "\n\n")
	if strings.TrimSpace(msg.Text) == "" {
		msg.Text = summary
		return
	}
	msg.Text = strings.TrimSpace(msg.Text) + "\n\n" + summary
}

// mediaSummaryBlock 渲染一条媒体摘要块（字段名与微信媒体实现对齐）。
func mediaSummaryBlock(item Media) string {
	kind := strings.TrimSpace(item.Kind)
	if kind == "" {
		kind = "file"
	}
	lines := []string{"[" + humanizeMediaKind(kind) + " attachment]"}
	if name := strings.TrimSpace(item.FileName); name != "" {
		lines = append(lines, "file_name: "+name)
	}
	if contentType := strings.TrimSpace(item.ContentType); contentType != "" {
		lines = append(lines, "content_type: "+contentType)
	}
	if mediaPath := strings.TrimSpace(item.Path); mediaPath != "" {
		lines = append(lines, "local_path: "+mediaPath)
	}
	if mediaURL := strings.TrimSpace(item.URL); mediaURL != "" {
		lines = append(lines, "source_url: "+mediaURL)
	}
	return strings.Join(lines, "\n")
}

// injectOutboundMediaNote 在入站文本尾部追加「如何回发媒体」的通道提示
// （OutboundMediaNoteProvider 能力；已包含则不重复）。调用点在入站媒体物化
// 之后（本地执行与远程派发两条路径），保证提示是文本最后一段。
func (s *Service) injectOutboundMediaNote(provider Provider, msg *InboundMessage) {
	if msg == nil {
		return
	}
	noted, ok := provider.(OutboundMediaNoteProvider)
	if !ok {
		return
	}
	note := strings.TrimSpace(noted.OutboundMediaNote())
	if note == "" || strings.Contains(msg.Text, note) {
		return
	}
	if strings.TrimSpace(msg.Text) == "" {
		msg.Text = note
		return
	}
	msg.Text = strings.TrimSpace(msg.Text) + "\n\n" + note
}

func humanizeMediaKind(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		return "File"
	}
	if len(trimmed) == 1 {
		return strings.ToUpper(trimmed)
	}
	return strings.ToUpper(trimmed[:1]) + strings.ToLower(trimmed[1:])
}
