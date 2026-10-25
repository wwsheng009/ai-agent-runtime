package main

// notify_api.go 暴露 owner 会话保护的通知策略 API（docs/gateway/08-
// notifications.md §10、控制台「通知设置」页）：
//
//	GET /api/notify/policy  → {"policy": {...}, "meta": {...}}
//	PUT /api/notify/policy  ← {"version":1,"global":{...},"sessions":{...},"channels":{...}}
//
// GET 同时返回 kinds/channels/defaults 元数据，控制台据此渲染开关而无需
// 在前端硬编码词表；PUT 校验失败返回 400 且不落盘、不改变生效策略。

import (
	"encoding/json"
	"net/http"
	"strings"
)

type notifyKindMeta struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Priority string `json:"priority"`
}

type notifyChannelMeta struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
}

type notifyPolicyMeta struct {
	Source     string              `json:"source"`
	Path       string              `json:"path"`
	LegacyPath string              `json:"legacy_path,omitempty"`
	Kinds      []notifyKindMeta    `json:"kinds"`
	Channels   []notifyChannelMeta `json:"channels"`
	Defaults   notifyPolicyScope   `json:"defaults"`
}

// notifyPolicyKinds 返回控制台可开关的 kind 词表（与 notify.KnownKinds 对齐，
// 过滤 gateway 兼容旧词 completion 以免 UI 出现重复项）。
func notifyPolicyKinds() []notifyKindMeta {
	return []notifyKindMeta{
		{Kind: "approval", Label: "待审批", Priority: "high"},
		{Kind: "question", Label: "待回答", Priority: "high"},
		{Kind: "error", Label: "出错", Priority: "high"},
		{Kind: "completed", Label: "已完成", Priority: "normal"},
		{Kind: "waiting", Label: "等待中", Priority: "normal"},
	}
}

// notifyPolicyDefaults 是"恢复默认值"的基线：允许全部 kind、门槛 normal、
// 不静音、免打扰为空（空数组仍是显式设置，可覆盖 --notify-quiet-hours）。
func notifyPolicyDefaults() notifyPolicyScope {
	kinds := []string{"approval", "question", "error", "completed", "waiting"}
	return notifyPolicyScope{
		MinPriority: "normal",
		AllowKinds:  &kinds,
	}
}

// handleNotifyPolicy 返回 GET/PUT 处理器。onSaved 在 PUT 成功后触发（如让
// pushNotifier 立即应用新策略）；nil 允许。
func handleNotifyPolicy(source *notifyPolicySource, meta func() notifyPolicyMeta, onSaved func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			view, err := source.CurrentView()
			resp := map[string]any{
				"policy": view,
				"meta":   buildNotifyPolicyMeta(source, meta),
			}
			if err != nil {
				resp["warning"] = err.Error()
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodPut:
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			var view notifyPolicyFile
			if err := json.NewDecoder(r.Body).Decode(&view); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid policy body: " + err.Error()})
				return
			}
			if err := source.Apply(view); err != nil {
				if isNotifyPolicyValidationError(err) {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to save policy: " + err.Error()})
				return
			}
			if onSaved != nil {
				onSaved()
			}
			saved, err := source.CurrentView()
			resp := map[string]any{
				"policy": saved,
				"meta":   buildNotifyPolicyMeta(source, meta),
			}
			if err != nil {
				resp["warning"] = err.Error()
			}
			writeJSON(w, http.StatusOK, resp)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func buildNotifyPolicyMeta(source *notifyPolicySource, meta func() notifyPolicyMeta) notifyPolicyMeta {
	out := notifyPolicyMeta{Kinds: notifyPolicyKinds(), Defaults: notifyPolicyDefaults()}
	if meta != nil {
		out = meta()
		if len(out.Kinds) == 0 {
			out.Kinds = notifyPolicyKinds()
		}
		if out.Defaults.AllowKinds == nil {
			out.Defaults = notifyPolicyDefaults()
		}
	}
	if source != nil {
		out.Source = source.SourceLabel()
		out.Path = source.Path()
		out.LegacyPath = source.LegacyPath()
	}
	if strings.TrimSpace(out.Source) == "" {
		out.Source = "default"
	}
	return out
}
