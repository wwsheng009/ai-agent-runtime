// 指令面板（官方 2026-08 新增）：
//
//	GET    /v2/panels                        列表（30 QPM，必须带 scope）
//	POST   /v2/panels                        创建（10 QPM）
//	GET    /v2/panels/{panel_id}             详情
//	PUT    /v2/panels/{panel_id}             修改（10 QPM）
//	DELETE /v2/panels/{panel_id}             删除（10 QPM）
//	PUT    /v2/panels/{panel_id}/target      修改关联对象（60 QPM）
//
// scope：c2c / group / channel / dm；target_type：all / specific（仅 c2c/group）。
// 一个机器人最多 20 个面板，每个面板最多 20 个元素。
package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	panelMaxItems   = 20
	panelMaxTargets = 20
)

// QQPanel 是面板配置。
type QQPanel struct {
	Items   []QQPanelItem `json:"items"`
	Remark  string        `json:"remark,omitempty"`
	Version int           `json:"version,omitempty"`
}

// QQPanelItem 是面板元素。
type QQPanelItem struct {
	Name      string `json:"name"`
	Desc      string `json:"desc,omitempty"`
	Type      string `json:"type"` // command | link
	OnlyAdmin bool   `json:"only_admin,omitempty"`
	Link      string `json:"link,omitempty"`
}

// QQPanelRecord 是面板记录。
type QQPanelRecord struct {
	PanelID      string   `json:"panel_id"`
	Scope        string   `json:"scope"`
	TargetType   string   `json:"target_type"`
	Panel        QQPanel  `json:"panel"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	Version      int      `json:"version"`
	UserOpenIDs  []string `json:"user_openids,omitempty"`
	GroupOpenIDs []string `json:"group_openids,omitempty"`
}

// QQPanelPage 是面板列表的一页。
type QQPanelPage struct {
	Records    []QQPanelRecord `json:"records"`
	NextCursor string          `json:"next_cursor"`
	IsEnd      bool            `json:"is_end"`
}

// QQPanelCreateRequest 是创建面板请求。
type QQPanelCreateRequest struct {
	Scope        string   `json:"scope"`
	TargetType   string   `json:"target_type,omitempty"`
	UserOpenIDs  []string `json:"user_openids,omitempty"`
	GroupOpenIDs []string `json:"group_openids,omitempty"`
	Panel        QQPanel  `json:"panel"`
}

// ListPanels 拉取指定场景的面板列表（scope 必填）。
func (p *Provider) ListPanels(ctx context.Context, cfg connectors.Config, scope, cursor string, limit int) (QQPanelPage, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if err := validatePanelScope(scope); err != nil {
		return QQPanelPage{}, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	query := url.Values{}
	query.Set("scope", scope)
	query.Set("limit", strconv.Itoa(limit))
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	var page QQPanelPage
	endpoint := p.baseFor(cfg) + "/v2/panels?" + query.Encode()
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &page); err != nil {
		return QQPanelPage{}, err
	}
	return page, nil
}

// CreatePanel 创建指令面板。
func (p *Provider) CreatePanel(ctx context.Context, cfg connectors.Config, req QQPanelCreateRequest) (QQPanelRecord, error) {
	if err := validatePanelCreate(req); err != nil {
		return QQPanelRecord{}, err
	}
	var record QQPanelRecord
	if err := p.apiJSON(ctx, cfg, http.MethodPost, p.baseFor(cfg)+"/v2/panels", req, &record); err != nil {
		return QQPanelRecord{}, err
	}
	return record, nil
}

// GetPanel 查询面板详情。
func (p *Provider) GetPanel(ctx context.Context, cfg connectors.Config, panelID string) (QQPanelRecord, error) {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return QQPanelRecord{}, fmt.Errorf("qqbot: panel id is required")
	}
	var record QQPanelRecord
	endpoint := p.baseFor(cfg) + "/v2/panels/" + url.PathEscape(panelID)
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &record); err != nil {
		return QQPanelRecord{}, err
	}
	return record, nil
}

// UpdatePanel 修改面板配置（覆盖元素与备注，不影响关联对象），返回新版本号。
func (p *Provider) UpdatePanel(ctx context.Context, cfg connectors.Config, panelID string, panel QQPanel) (int, error) {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return 0, fmt.Errorf("qqbot: panel id is required")
	}
	if err := validatePanelItems(panel.Items); err != nil {
		return 0, err
	}
	var response struct {
		Version int `json:"version"`
	}
	endpoint := p.baseFor(cfg) + "/v2/panels/" + url.PathEscape(panelID)
	if err := p.apiJSON(ctx, cfg, http.MethodPut, endpoint, map[string]any{"panel": panel}, &response); err != nil {
		return 0, err
	}
	return response.Version, nil
}

// DeletePanel 删除指令面板。
func (p *Provider) DeletePanel(ctx context.Context, cfg connectors.Config, panelID string) error {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return fmt.Errorf("qqbot: panel id is required")
	}
	endpoint := p.baseFor(cfg) + "/v2/panels/" + url.PathEscape(panelID)
	return p.apiJSON(ctx, cfg, http.MethodDelete, endpoint, nil, nil)
}

// UpdatePanelTargets 修改面板关联对象（op=add|del，单次最多 20 个）。
func (p *Provider) UpdatePanelTargets(ctx context.Context, cfg connectors.Config, panelID, op string, userOpenIDs, groupOpenIDs []string) error {
	panelID = strings.TrimSpace(panelID)
	if panelID == "" {
		return fmt.Errorf("qqbot: panel id is required")
	}
	op = strings.ToLower(strings.TrimSpace(op))
	if op != "add" && op != "del" {
		return fmt.Errorf("qqbot: panel target op must be add or del")
	}
	users, err := normalizeOpenIDsOptional(userOpenIDs, panelMaxTargets)
	if err != nil {
		return err
	}
	groups, err := normalizeOpenIDsOptional(groupOpenIDs, panelMaxTargets)
	if err != nil {
		return err
	}
	if len(users) == 0 && len(groups) == 0 {
		return fmt.Errorf("qqbot: at least one target openid is required")
	}
	payload := map[string]any{"op": op}
	if len(users) > 0 {
		payload["user_openids"] = users
	}
	if len(groups) > 0 {
		payload["group_openids"] = groups
	}
	endpoint := p.baseFor(cfg) + "/v2/panels/" + url.PathEscape(panelID) + "/target"
	return p.apiJSON(ctx, cfg, http.MethodPut, endpoint, payload, nil)
}

func validatePanelScope(scope string) error {
	switch scope {
	case "c2c", "group", "channel", "dm":
		return nil
	default:
		return fmt.Errorf("qqbot: panel scope must be c2c/group/channel/dm, got %q", scope)
	}
}

func validatePanelCreate(req QQPanelCreateRequest) error {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if err := validatePanelScope(scope); err != nil {
		return err
	}
	targetType := strings.ToLower(strings.TrimSpace(req.TargetType))
	if targetType == "" {
		targetType = "all"
	}
	if targetType != "all" && targetType != "specific" {
		return fmt.Errorf("qqbot: panel target_type must be all or specific")
	}
	if (scope == "channel" || scope == "dm") && targetType != "all" {
		return fmt.Errorf("qqbot: panel scope %s only supports target_type=all", scope)
	}
	if targetType == "specific" {
		if scope == "c2c" && len(req.UserOpenIDs) == 0 {
			return fmt.Errorf("qqbot: c2c specific panel requires user_openids")
		}
		if scope == "group" && len(req.GroupOpenIDs) == 0 {
			return fmt.Errorf("qqbot: group specific panel requires group_openids")
		}
	}
	if _, err := normalizeOpenIDsOptional(req.UserOpenIDs, panelMaxTargets); err != nil {
		return err
	}
	if _, err := normalizeOpenIDsOptional(req.GroupOpenIDs, panelMaxTargets); err != nil {
		return err
	}
	return validatePanelItems(req.Panel.Items)
}

func validatePanelItems(items []QQPanelItem) error {
	if len(items) > panelMaxItems {
		return fmt.Errorf("qqbot: panel items exceed limit %d", panelMaxItems)
	}
	for i, item := range items {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		if itemType != "command" && itemType != "link" {
			return fmt.Errorf("qqbot: panel item %d type must be command or link", i)
		}
		if itemType == "link" && !strings.HasPrefix(strings.TrimSpace(item.Link), "https://") {
			return fmt.Errorf("qqbot: panel item %d link must start with https://", i)
		}
	}
	return nil
}

// normalizeOpenIDsOptional 同 normalizeOpenIDs，但允许空列表（返回 nil）。
func normalizeOpenIDsOptional(ids []string, limit int) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return normalizeOpenIDs(ids, limit)
}
