package commands

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 会话列表 / 屏幕快照的缓存键与版本指纹。
//
// 这两个端点由 SSE 驱动、秒级刷新且多 tab 并存，服务端必须能回答两个问题：
//  1. 「这次请求和上次相比，响应真的变了吗？」→ 版本指纹 + ETag（304 短路）；
//  2. 「多个客户端同时问同一件事，能不能只算一次？」→ 微缓存 + 单飞。
//
// 指纹必须覆盖响应的全部输入，否则会把过期内容 304 给客户端：
// 会话列表的输入 = 元数据行（标题/摘要/计数/时间/工作区）+ 当前会话 id +
// 排序/范围参数 + 网格视图（claimants / peer 节点）+ binding 文件状态；
// 屏幕快照的输入 = 端点参数 + 快照正文本身（用内容哈希，天然精确）。
// ---------------------------------------------------------------------------

var (
	// chatWebSessionsCache 缓存 /web/api/sessions 的响应字节（TTL 内多 tab 合并）。
	chatWebSessionsCache = newWebHTTPResponseCache(webHTTPResponseCacheTTL)
)

// chatWebSessionsVersion 计算会话列表的版本指纹（覆盖响应全部输入）。
// 必须在任何「渲染回退完整加载」之前调用：指纹命中的请求直接 304，
// 不产生任何完整加载。
func chatWebSessionsVersion(items []chatWebSessionListItem, currentID, sortBy, scope string, index chatWebMeshSessionIndex) string {
	var builder strings.Builder
	builder.WriteString("current=")
	builder.WriteString(currentID)
	builder.WriteString("\x00sort=")
	builder.WriteString(sortBy)
	builder.WriteString("\x00scope=")
	builder.WriteString(scope)
	for _, item := range items {
		fmt.Fprintf(&builder, "\x00%s|%s|%s|%d|%s|%s|%t|%s|%s",
			item.ID, item.Title, item.Summary, item.MessageCount,
			item.CreatedAt.UTC().Format(time.RFC3339Nano),
			item.UpdatedAt.UTC().Format(time.RFC3339Nano),
			item.Current, item.WorkspacePath, item.WorkspaceName)
	}
	builder.WriteString("\x00mesh=")
	builder.WriteString(chatWebMeshViewDigest(index))
	builder.WriteString("\x00bindings=")
	builder.WriteString(chatWebBindingsDigest(index, items))
	return builder.String()
}

// chatWebMeshViewDigest 摘要网格视图中会影响会话列表响应的字段。
// 只取标量字段，避免 map/指针顺序带来的不稳定。
func chatWebMeshViewDigest(index chatWebMeshSessionIndex) string {
	if !index.enabled {
		return "off"
	}
	var builder strings.Builder
	builder.WriteString(index.selfNodeID)
	fmt.Fprintf(&builder, "|%s|%v", index.view.Root, index.view.Counts)
	sessionIDs := make([]string, 0, len(index.claimants))
	for sessionID := range index.claimants {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	for _, sessionID := range sessionIDs {
		for _, node := range index.claimants[sessionID] {
			fmt.Fprintf(&builder, "\x00c:%s|%s|%s", sessionID, node.NodeID, node.State)
			if node.Session != nil {
				fmt.Fprintf(&builder, "|%s|%s", node.Session.ID, node.Session.State)
			}
			if endpoint := chatWebSessionEndpointFromNode(node); endpoint != nil {
				fmt.Fprintf(&builder, "|%s|%s|%t", endpoint.BaseURL, endpoint.Reachability, endpoint.Busy)
			}
		}
	}
	for _, node := range index.view.Nodes {
		fmt.Fprintf(&builder, "\x00n:%s|%s|%s", node.NodeID, node.State, node.Reachability)
		if node.Session != nil {
			fmt.Fprintf(&builder, "|%s|%s|%s", node.Session.ID, node.Session.Title, node.Session.State)
		}
		if node.Workspace != nil {
			fmt.Fprintf(&builder, "|%s", node.Workspace.Path)
		}
	}
	return builder.String()
}

// chatWebBindingsDigest 摘要 binding 目录中与会话列表相关的文件状态。
// 一次 ReadDir 拿到全部文件的大小与 mtime（会话切换会重写 binding，mtime 必变），
// 避免为 100 个会话做 100 次 stat。
func chatWebBindingsDigest(index chatWebMeshSessionIndex, items []chatWebSessionListItem) string {
	if !index.enabled || strings.TrimSpace(index.paths.Bindings) == "" {
		return "off"
	}
	entries, err := os.ReadDir(index.paths.Bindings)
	if err != nil {
		// 目录不可读时退回「不摘要」：宁可让 ETag 更保守（每次重建），
		// 也不能让客户端拿着无法验证的指纹一直命中 304。
		return "unreadable"
	}
	stats := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		stats[entry.Name()] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
	}
	var builder strings.Builder
	for _, item := range items {
		name := filepath.Base(index.paths.BindingPath(item.ID))
		builder.WriteString("\x00")
		builder.WriteString(name)
		builder.WriteString("=")
		builder.WriteString(stats[name])
	}
	return builder.String()
}

// chatWebScreenParamsKey 是屏幕快照的端点参数指纹（视图/窗口/过滤/尾裁/格式）。
func chatWebScreenParamsKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	query := r.URL.Query()
	view := strings.ToLower(strings.TrimSpace(query.Get("view")))
	format := strings.ToLower(strings.TrimSpace(query.Get("format")))
	return strings.Join([]string{
		"view=" + view,
		"format=" + format,
		"msg_limit=" + strings.TrimSpace(query.Get("msg_limit")),
		"msg_before=" + strings.TrimSpace(query.Get("msg_before")),
		"roles=" + strings.TrimSpace(query.Get("roles")),
		"q=" + strings.TrimSpace(query.Get("q")),
		"tail=" + strings.TrimSpace(query.Get("tail")),
	}, "&")
}
