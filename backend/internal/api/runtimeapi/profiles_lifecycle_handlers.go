package runtimeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// ---------------------------------------------------------------------------
// 写端点：删除 / 设为默认 / 重命名 / 移动 + 引用检查（§23 G2/G3，D25/D26）
// ---------------------------------------------------------------------------

// runtimeProfileSessionScanLimit 是有界会话扫描上限：引用检查不能为了"完整"
// 把会话存储全量拉进内存（R22 的检查在 UI 交互路径上）。
const runtimeProfileSessionScanLimit = 500

// runtimeProfileReferenceSet 是四类引用的投影（D25）。
type runtimeProfileReferenceSet struct {
	Ref            string                   `json:"ref"`
	Root           string                   `json:"root"`
	DefaultProfile string                   `json:"default_profile,omitempty"`
	IsDefault      bool                     `json:"is_default"`
	Blocking       []string                 `json:"blocking"`
	Warnings       []string                 `json:"warnings"`
	ConfigItems    []map[string]interface{} `json:"config_items"`
	Sessions       []map[string]interface{} `json:"sessions"`
	SessionScan    map[string]interface{}   `json:"session_scan"`
	AgentRefs      []map[string]interface{} `json:"agent_references"`
	AgentNote      string                   `json:"agent_reference_note,omitempty"`
	Files          []string                 `json:"files"`
	FileCount      int                      `json:"file_count"`
}

// GetRuntimeProfileReferences 只读：列出可枚举的引用（删除/移动前展示）。
func (h *Handler) GetRuntimeProfileReferences(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.collectRuntimeProfileReferences(r, target))
}

// DeleteRuntimeProfile 硬删 + 引用检查（D25）：无 force 且被 default 引用 → 409。
func (h *Handler) DeleteRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	force := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("force")), "true")
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	if err := assertRuntimeProfileRootSafe(target.Root); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	refs := h.collectRuntimeProfileReferences(r, target)
	if refs.IsDefault && !force {
		h.writeJSON(w, http.StatusConflict, map[string]interface{}{
			"deleted":    false,
			"error":      "profile 被 config.profiles.default_profile 引用：先改默认 profile，或使用 force=true（同时清空 default）",
			"references": refs,
		})
		return
	}

	removedItems, defaultCleared, cleanupErr := h.cleanupRuntimeProfileConfig(target, refs, refs.IsDefault && force)
	if cleanupErr != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "cleanup profiles config failed", cleanupErr))
		return
	}
	if err := os.RemoveAll(target.Root); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "delete profile failed", err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted":              true,
		"ref":                  target.Ref,
		"root":                 target.Root,
		"removed_files":        refs.Files,
		"removed_file_count":   refs.FileCount,
		"config_items_removed": removedItems,
		"default_cleared":      defaultCleared,
		"references":           refs,
		// 硬删语义（Q20）：不做回收站，避免第二套状态源。
		"recoverable": false,
	})
}

// SetRuntimeProfileDefault 设为默认（写 config.profiles.default_profile，只影响
// **新会话**，D26）。profile 若不在 profiles.root 下则同时注册 items 项，保证
// default 这个名字可解析（否则会写出一个悬空的 default —— R22 类问题）。
func (h *Handler) SetRuntimeProfileDefault(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	configPath, err := h.profileConfigWritePath()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "resolve config path failed", err))
		return
	}
	previous := h.profileConfigSnapshot().DefaultProfile
	register := !h.runtimeProfileResolvableByName(target.Ref, target.Root)
	update := agentconfig.ProfilesConfigUpdate{DefaultProfile: target.Ref}
	if register {
		update.ItemName = target.Ref
		update.ItemRoot = target.Root
	}
	if err := agentconfig.UpdateProfilesConfig(configPath, update); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "write default profile failed", err))
		return
	}
	h.syncAICLIProfilesSnapshot(func(profiles *agentconfig.ProfilesConfig) {
		if register {
			if profiles.Items == nil {
				profiles.Items = map[string]agentconfig.ProfileConfig{}
			}
			profiles.Items[target.Ref] = agentconfig.ProfileConfig{Root: target.Root}
		}
		profiles.DefaultProfile = target.Ref
	})
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"default_profile":      target.Ref,
		"previous_default":     previous,
		"config_path":          configPath,
		"registered":           register,
		"affects":              "new_sessions_only",
		"current_session_note": "当前会话不受影响：要立即切换请用 apply（Batch 13）或 CLI `/profile use`",
	})
}

// RenameRuntimeProfile 重命名（同层；目录名 + 注册项 + default 一起改）。
func (h *Handler) RenameRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req struct {
		NewName string `json:"new_name"`
	}
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid rename request body"))
		return
	}
	newName := strings.TrimSpace(req.NewName)
	if err := profilesys.ValidateProfileName(newName); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	if newName == target.Ref {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "新名称与当前名称相同"))
		return
	}
	newRoot := filepath.Join(filepath.Dir(target.Root), newName)
	if err := assertRuntimeProfileRootSafe(newRoot); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if _, statErr := os.Stat(newRoot); statErr == nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrConfigInvalid, "目标目录已存在："+newRoot))
		return
	}
	refs := h.collectRuntimeProfileReferences(r, target)
	if err := os.Rename(target.Root, newRoot); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "rename profile dir failed", err))
		return
	}
	updatedItems, defaultUpdated, cfgErr := h.rewriteRuntimeProfileConfig(target.Ref, newName, target.Root, newRoot, refs)
	if cfgErr != nil {
		// 目录已改、配置未改：如实报告（可重试；不做回滚以免把目录改回去时丢文件）。
		h.writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"renamed":        true,
			"old_ref":        target.Ref,
			"new_ref":        newName,
			"root":           newRoot,
			"config_updated": false,
			"error":          "目录已重命名，但 profiles 配置更新失败：" + cfgErr.Error(),
			"references":     refs,
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"renamed":                 true,
		"old_ref":                 target.Ref,
		"new_ref":                 newName,
		"root":                    newRoot,
		"layer":                   target.Layer,
		"config_updated":          true,
		"config_items_updated":    updatedItems,
		"default_profile_updated": defaultUpdated,
		"references":              refs,
		"session_note":            "既有会话的 profile_ref 仍是旧路径（resume 时按 R18 降级警告，不静默改写会话存储）",
	})
}

// MoveRuntimeProfile 层级移动（user↔project；同层拒绝、跨层冲突拒绝）。
func (h *Handler) MoveRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	var req struct {
		Layer string `json:"layer"`
	}
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid move request body"))
		return
	}
	layer := strings.ToLower(strings.TrimSpace(req.Layer))
	layerRoot, err := runtimeProfileLayerRoot(layer)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	if strings.EqualFold(target.Layer, layer) {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"同层移动无意义（profile 已在 "+layer+" 层）：改名请用 rename"))
		return
	}
	newRoot := filepath.Join(layerRoot, target.Ref)
	if err := assertRuntimeProfileRootSafe(newRoot); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if _, statErr := os.Stat(newRoot); statErr == nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrConfigInvalid, "目标层已存在同名 profile："+newRoot))
		return
	}
	refs := h.collectRuntimeProfileReferences(r, target)
	if err := moveRuntimeProfileTree(target.Root, newRoot); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "move profile failed", err))
		return
	}
	updatedItems, cfgErr := h.repointRuntimeProfileConfig(target.Ref, newRoot, refs)
	if cfgErr != nil {
		h.writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"moved":          true,
			"ref":            target.Ref,
			"from":           target.Root,
			"to":             newRoot,
			"config_updated": false,
			"error":          "目录已移动，但 profiles 配置更新失败：" + cfgErr.Error(),
			"references":     refs,
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"moved":                true,
		"ref":                  target.Ref,
		"from":                 target.Root,
		"to":                   newRoot,
		"layer":                layer,
		"config_updated":       true,
		"config_items_updated": updatedItems,
		"references":           refs,
		"session_note":         "既有会话的 profile_ref 仍指向旧路径（resume 时按 R18 降级警告，不静默改写会话存储）",
	})
}

// ---------------------------------------------------------------------------
// 引用检查与配置生命周期辅助
// ---------------------------------------------------------------------------

// collectRuntimeProfileReferences 汇总四类引用（D25）：
//  1. config.profiles.default_profile（阻止删除，除非 force）
//  2. 会话 profile 绑定（有界扫描；允许删除，列出受影响会话）
//  3. 其他 profile 的 agent 引用（**当前 schema 无跨 profile 引用字段**：恒空 + 说明）
//  4. profile 目录文件清单（删除确认展示）
func (h *Handler) collectRuntimeProfileReferences(r *http.Request, target runtimeProfileTarget) *runtimeProfileReferenceSet {
	view := h.profileConfigSnapshot()
	refs := &runtimeProfileReferenceSet{
		Ref:            target.Ref,
		Root:           target.Root,
		DefaultProfile: view.DefaultProfile,
		Blocking:       []string{},
		Warnings:       []string{},
		ConfigItems:    []map[string]interface{}{},
		Sessions:       []map[string]interface{}{},
		SessionScan:    map[string]interface{}{"limit": runtimeProfileSessionScanLimit, "scanned": 0, "truncated": false},
		AgentRefs:      []map[string]interface{}{},
		AgentNote: "profile.yaml 的 agents 声明只有 provider/model/tools，无跨 profile 引用字段，" +
			"因此该项在 schema 引入引用字段前恒为空（不做猜测式扫描）",
	}
	refs.IsDefault = view.DefaultProfile != "" &&
		(view.DefaultProfile == target.Ref || h.defaultProfileResolvesToRoot(view.DefaultProfile, target.Root))
	if refs.IsDefault {
		refs.Blocking = append(refs.Blocking,
			"config.profiles.default_profile="+view.DefaultProfile+" 指向该 profile：无 force 时拒绝删除")
	}
	for _, item := range view.Items {
		if sameFilePath(item.Root, target.Root) {
			refs.ConfigItems = append(refs.ConfigItems, map[string]interface{}{
				"name":       item.Name,
				"root":       item.Root,
				"is_default": item.Name == view.DefaultProfile,
			})
		}
	}

	if h.sessionManager != nil {
		ctx, cancel := sessionStoreQueryContext(r)
		defer cancel()
		sessions, err := h.sessionManager.SearchSessions(ctx, &runtimechat.SessionSearchOptions{Limit: runtimeProfileSessionScanLimit})
		if err != nil {
			refs.Warnings = append(refs.Warnings, "会话存储查询失败（引用检查未覆盖会话）："+err.Error())
			refs.SessionScan["error"] = err.Error()
		} else {
			refs.SessionScan["scanned"] = len(sessions)
			refs.SessionScan["truncated"] = len(sessions) >= runtimeProfileSessionScanLimit
			if len(sessions) >= runtimeProfileSessionScanLimit {
				refs.Warnings = append(refs.Warnings,
					fmt.Sprintf("会话扫描达到上限 %d，可能有未列出的绑定会话", runtimeProfileSessionScanLimit))
			}
			for _, session := range sessions {
				if session == nil {
					continue
				}
				matched := make([]string, 0, 3)
				profileRef := sessionmeta.String(session.Metadata.Context, sessionmeta.ProfileRef)
				legacyRef := sessionmeta.String(session.Metadata.Context, sessionmeta.LegacyAPIProfileReference)
				profileRoot := sessionmeta.String(session.Metadata.Context, sessionmeta.ProfileRoot)
				if sameFilePath(profileRef, target.Root) {
					matched = append(matched, sessionmeta.ProfileRef)
				}
				if legacyRef != "" && (legacyRef == target.Ref || sameFilePath(legacyRef, target.Root)) {
					matched = append(matched, sessionmeta.LegacyAPIProfileReference)
				}
				if sameFilePath(profileRoot, target.Root) {
					matched = append(matched, sessionmeta.ProfileRoot)
				}
				if len(matched) == 0 {
					continue
				}
				refs.Sessions = append(refs.Sessions, map[string]interface{}{
					"session_id":   session.ID,
					"user_id":      session.UserID,
					"state":        string(session.State),
					"updated_at":   session.UpdatedAt.UTC().Format(time.RFC3339),
					"profile_ref":  profileRef,
					"profile_root": profileRoot,
					"matched_keys": matched,
				})
			}
		}
	}
	if len(refs.Sessions) > 0 {
		refs.Warnings = append(refs.Warnings,
			fmt.Sprintf("%d 个会话绑定该 profile：删除/移动后这些会话 resume 时按 R18 降级（警告 + 保持会话不崩）", len(refs.Sessions)))
	}

	refs.Files = listRuntimeProfileFiles(target.Root)
	refs.FileCount = len(refs.Files)
	return refs
}

// defaultProfileResolvesToRoot 判定 default_profile 这个名字当前解析到的 root
// 是否就是目标 root（items 优先、其次 profiles.root/name，与解析器同序）。
func (h *Handler) defaultProfileResolvesToRoot(name, root string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	view := h.profileConfigSnapshot()
	if itemRoot, ok := view.itemRoot(name); ok {
		return sameFilePath(itemRoot, root)
	}
	if strings.TrimSpace(view.Root) != "" {
		return sameFilePath(filepath.Join(view.Root, name), root)
	}
	return false
}

// cleanupRuntimeProfileConfig 删除 profile 后清理 config 残留（R9 dormant 条目）：
// 注册项指向该 root 的全部删除；force 删除 default 引用时清空 default。
func (h *Handler) cleanupRuntimeProfileConfig(target runtimeProfileTarget, refs *runtimeProfileReferenceSet, clearDefault bool) ([]string, bool, error) {
	removed := make([]string, 0, len(refs.ConfigItems))
	if len(refs.ConfigItems) == 0 && !clearDefault {
		return removed, false, nil
	}
	configPath, err := h.profileConfigWritePath()
	if err != nil {
		return removed, false, err
	}
	for _, item := range refs.ConfigItems {
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if err := agentconfig.RemoveProfilesConfigItem(configPath, name); err != nil {
			return removed, false, err
		}
		removed = append(removed, name)
	}
	defaultCleared := false
	if clearDefault {
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ClearDefaultProfile: true}); err != nil {
			return removed, false, err
		}
		defaultCleared = true
	}
	h.syncAICLIProfilesSnapshot(func(profiles *agentconfig.ProfilesConfig) {
		for _, name := range removed {
			delete(profiles.Items, name)
		}
		if defaultCleared {
			profiles.DefaultProfile = ""
		}
	})
	return removed, defaultCleared, nil
}

// rewriteRuntimeProfileConfig 重命名后的配置改写：先写新键（保证 default/查询在
// 写入过程中始终可解析），再清旧键或把同 root 的别名指向新路径。
func (h *Handler) rewriteRuntimeProfileConfig(oldRef, newRef, oldRoot, newRoot string, refs *runtimeProfileReferenceSet) ([]string, bool, error) {
	if len(refs.ConfigItems) == 0 && !refs.IsDefault {
		return []string{}, false, nil
	}
	configPath, err := h.profileConfigWritePath()
	if err != nil {
		return nil, false, err
	}
	updated := make([]string, 0, len(refs.ConfigItems)+1)
	update := agentconfig.ProfilesConfigUpdate{ItemName: newRef, ItemRoot: newRoot}
	defaultUpdated := false
	if refs.IsDefault {
		update.DefaultProfile = newRef
		defaultUpdated = true
	}
	if err := agentconfig.UpdateProfilesConfig(configPath, update); err != nil {
		return nil, false, err
	}
	updated = append(updated, newRef)
	for _, item := range refs.ConfigItems {
		name, _ := item["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		if name == oldRef {
			if err := agentconfig.RemoveProfilesConfigItem(configPath, oldRef); err != nil {
				return updated, defaultUpdated, err
			}
			continue
		}
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ItemName: name, ItemRoot: newRoot}); err != nil {
			return updated, defaultUpdated, err
		}
		updated = append(updated, name)
	}
	h.syncAICLIProfilesSnapshot(func(profiles *agentconfig.ProfilesConfig) {
		if profiles.Items == nil {
			profiles.Items = map[string]agentconfig.ProfileConfig{}
		}
		delete(profiles.Items, oldRef)
		profiles.Items[newRef] = agentconfig.ProfileConfig{Root: newRoot}
		for _, item := range refs.ConfigItems {
			name, _ := item["name"].(string)
			if strings.TrimSpace(name) == "" || name == oldRef {
				continue
			}
			profiles.Items[name] = agentconfig.ProfileConfig{Root: newRoot}
		}
		if refs.IsDefault {
			profiles.DefaultProfile = newRef
		}
	})
	return updated, defaultUpdated, nil
}

// repointRuntimeProfileConfig 层级移动后的配置改写：同名（ref 不变）只改 root；
// 没有注册项时补一条，否则移动后的 profile 只能靠 profiles.root 猜路径。
func (h *Handler) repointRuntimeProfileConfig(ref, newRoot string, refs *runtimeProfileReferenceSet) ([]string, error) {
	configPath, err := h.profileConfigWritePath()
	if err != nil {
		return nil, err
	}
	updated := make([]string, 0, len(refs.ConfigItems)+1)
	if len(refs.ConfigItems) == 0 {
		if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ItemName: ref, ItemRoot: newRoot}); err != nil {
			return nil, err
		}
		updated = append(updated, ref)
	} else {
		for _, item := range refs.ConfigItems {
			name, _ := item["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			if err := agentconfig.UpdateProfilesConfig(configPath, agentconfig.ProfilesConfigUpdate{ItemName: name, ItemRoot: newRoot}); err != nil {
				return updated, err
			}
			updated = append(updated, name)
		}
	}
	h.syncAICLIProfilesSnapshot(func(profiles *agentconfig.ProfilesConfig) {
		if profiles.Items == nil {
			profiles.Items = map[string]agentconfig.ProfileConfig{}
		}
		if len(updated) == 0 {
			profiles.Items[ref] = agentconfig.ProfileConfig{Root: newRoot}
			return
		}
		for _, name := range updated {
			profiles.Items[name] = agentconfig.ProfileConfig{Root: newRoot}
		}
	})
	return updated, nil
}

// moveRuntimeProfileTree 同盘 rename，跨盘（Windows 上跨卷 rename 会失败）回退为
// 复制 + 删除源目录。
func moveRuntimeProfileTree(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyRuntimeProfileTree(src, dst, false); err != nil {
		return err
	}
	return os.RemoveAll(src)
}
