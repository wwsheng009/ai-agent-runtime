// QQ 群管理接口（官方 2026-08/09 新增，均为「内邀接入」白名单能力）。
//
// 契约（bot.q.qq.com/wiki/develop/api-v2/autogen/api/…）：
//
//	GET  /v2/groups/{group_openid}/members                              成员列表（≤30/页）
//	GET  /v2/groups/{group_openid}/members/{member_openid}             成员信息
//	POST /v2/groups/{group_openid}/batch_remove_members                批量移除（≤20）
//	GET  /v2/groups/{group_openid}/member_blacklist                    黑名单查询
//	POST /v2/groups/{group_openid}/member_blacklist                    黑名单 add/del
//	GET  /v2/groups/{group_openid}/restrict_chat_setting               禁言状态
//	POST /v2/groups/{group_openid}/restrict_chat_setting               成员禁言 add/update/del
//	GET  /v2/groups/{group_openid}/join_request_list                   入群申请列表
//	POST /v2/groups/{group_openid}/approval_join_request/{member}      入群审批
//
// 权限：成员/黑名单类需 11253 白名单；禁言与入群审批需机器人为群管理员。
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

// GroupMember 是群成员信息（官方 Member / 成员详情共用）。
type GroupMember struct {
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	MemberRole   string `json:"member_role"` // member | owner | admin
	Bot          bool   `json:"bot"`
	JoinedAt     string `json:"joined_at"`
	UnionOpenID  string `json:"union_openid"`
}

// GroupMemberPage 是群成员列表的一页（官方：每次最多 30 条）。
type GroupMemberPage struct {
	Members    []GroupMember `json:"members"`
	NextCursor string        `json:"next_cursor"`
}

// GroupBlacklistUser 是群黑名单用户。
type GroupBlacklistUser struct {
	UnionOpenID  string `json:"union_openid"`
	MemberOpenID string `json:"member_openid"`
	Username     string `json:"username"`
	BannedAt     string `json:"banned_at"`
	Bot          bool   `json:"bot"`
}

// GroupBlacklistPage 是群黑名单的一页。
type GroupBlacklistPage struct {
	Users      []GroupBlacklistUser `json:"users"`
	NextCursor string               `json:"next_cursor"`
}

// GroupMuteState 是群禁言状态（官方 restrict_chat_setting GET 响应）。
type GroupMuteState struct {
	GlobalRule GroupGlobalMuteRule `json:"global_rule"`
	Members    []GroupMemberMute   `json:"members"`
}

type GroupGlobalMuteRule struct {
	Mode           string                   `json:"mode"` // none | always | schedule
	ScheduleRules  []GroupMuteScheduleRule  `json:"schedule_rules"`
	RecurringRules []GroupMuteRecurringRule `json:"recurring_rules"`
}

type GroupMuteScheduleRule struct {
	TaskID  string `json:"task_id"`
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
	Enabled bool   `json:"enabled"`
}

type GroupMuteRecurringRule struct {
	TaskID    string `json:"task_id"`
	Weekdays  []int  `json:"weekdays"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Enabled   bool   `json:"enabled"`
}

type GroupMemberMute struct {
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at"`
	Username     string `json:"username"`
	UnionOpenID  string `json:"union_openid"`
}

// GroupJoinRequest 是入群申请。
type GroupJoinRequest struct {
	JoinRequestID string `json:"join_request_id"`
	RiskTips      string `json:"risk_tips"`
	UnionOpenID   string `json:"union_openid"`
	MemberOpenID  string `json:"member_openid"`
	Username      string `json:"username"`
	ApplyAt       string `json:"apply_at"`
	ApplySource   string `json:"apply_source"` // self_apply | invited
	InvitedBy     string `json:"invited_by"`
	Bot           bool   `json:"bot"`
	VerifyInfo    struct {
		Method        string `json:"method"`
		VerifyMessage string `json:"verify_message"`
		ReviewQA      []struct {
			Question string `json:"question"`
			Answer   string `json:"answer"`
		} `json:"review_qa_list"`
	} `json:"verify_info"`
}

// GroupJoinRequestPage 是入群申请的一页。
type GroupJoinRequestPage struct {
	List       []GroupJoinRequest `json:"list"`
	NextCursor string             `json:"next_cursor"`
}

// groupOpenID 从 chat 键提取群 openid（要求 group: 前缀）。
func groupOpenID(chat string) (string, error) {
	target, err := parseChat(chat)
	if err != nil {
		return "", err
	}
	if target.kind != chatKindGroup {
		return "", fmt.Errorf("qqbot: 群管理仅支持群聊：%s", chat)
	}
	return target.openID, nil
}

// groupEndpoint 拼群管理端点（base + /v2/groups/{openid} + suffix）。
func (p *Provider) groupEndpoint(cfg connectors.Config, chat, suffix string) (string, error) {
	openID, err := groupOpenID(chat)
	if err != nil {
		return "", err
	}
	return p.baseFor(cfg) + "/v2/groups/" + url.PathEscape(openID) + suffix, nil
}

// ListGroupMembers 拉取群成员列表（官方：每次最多 30 条，cursor 空串=首页）。
func (p *Provider) ListGroupMembers(ctx context.Context, cfg connectors.Config, chat, cursor string) (GroupMemberPage, error) {
	endpoint, err := p.groupEndpoint(cfg, chat, "/members")
	if err != nil {
		return GroupMemberPage{}, err
	}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		endpoint += "?cursor=" + url.QueryEscape(cursor)
	}
	var page GroupMemberPage
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &page); err != nil {
		return GroupMemberPage{}, err
	}
	return page, nil
}

// GetGroupMember 获取指定群成员信息。
func (p *Provider) GetGroupMember(ctx context.Context, cfg connectors.Config, chat, memberOpenID string) (GroupMember, error) {
	memberOpenID = strings.TrimSpace(memberOpenID)
	if memberOpenID == "" {
		return GroupMember{}, fmt.Errorf("qqbot: member openid is required")
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/members/"+url.PathEscape(memberOpenID))
	if err != nil {
		return GroupMember{}, err
	}
	var member GroupMember
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &member); err != nil {
		return GroupMember{}, err
	}
	return member, nil
}

// 官方单次批量上限。
const (
	groupBatchRemoveLimit = 20
	groupBlacklistLimit   = 20
	groupMuteLimit        = 20
)

// BatchRemoveGroupMembers 批量移除群成员（官方：单次最多 20 个）。
// addToBlacklist=true 时同时加入群黑名单。返回拉黑失败的 openid。
func (p *Provider) BatchRemoveGroupMembers(ctx context.Context, cfg connectors.Config, chat string, memberOpenIDs []string, addToBlacklist bool) ([]string, error) {
	ids, err := normalizeOpenIDs(memberOpenIDs, groupBatchRemoveLimit)
	if err != nil {
		return nil, err
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/batch_remove_members")
	if err != nil {
		return nil, err
	}
	var response struct {
		Result        string   `json:"remove_members_result"`
		FailedOpenIDs []string `json:"add_to_member_blacklist_fail_openids"`
	}
	payload := map[string]any{"member_openids": ids}
	if addToBlacklist {
		payload["add_to_member_blacklist"] = true
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, endpoint, payload, &response); err != nil {
		return nil, err
	}
	if result := strings.TrimSpace(response.Result); result != "" && result != "success" {
		return response.FailedOpenIDs, fmt.Errorf("qqbot: batch remove returned %q", result)
	}
	return response.FailedOpenIDs, nil
}

// ListGroupBlacklist 查询群黑名单（官方：limit 默认 20、最大 100）。
func (p *Provider) ListGroupBlacklist(ctx context.Context, cfg connectors.Config, chat, cursor string, limit int) (GroupBlacklistPage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/member_blacklist")
	if err != nil {
		return GroupBlacklistPage{}, err
	}
	query := url.Values{}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	query.Set("limit", strconv.Itoa(limit))
	endpoint += "?" + query.Encode()
	var page GroupBlacklistPage
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &page); err != nil {
		return GroupBlacklistPage{}, err
	}
	return page, nil
}

// UpdateGroupBlacklist 群黑名单操作（op=add|del，单次最多 20 个）。
// 官方：目标用户仍在群中时无法加入黑名单。返回失败 openid。
func (p *Provider) UpdateGroupBlacklist(ctx context.Context, cfg connectors.Config, chat, op string, memberOpenIDs []string) ([]string, error) {
	op = strings.ToLower(strings.TrimSpace(op))
	if op != "add" && op != "del" {
		return nil, fmt.Errorf("qqbot: blacklist op must be add or del")
	}
	ids, err := normalizeOpenIDs(memberOpenIDs, groupBlacklistLimit)
	if err != nil {
		return nil, err
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/member_blacklist")
	if err != nil {
		return nil, err
	}
	var response struct {
		FailedOpenIDs []string `json:"fail_openids"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, endpoint,
		map[string]any{"op": op, "member_openids": ids}, &response); err != nil {
		return nil, err
	}
	return response.FailedOpenIDs, nil
}

// GetGroupMuteState 查询群禁言状态（全员规则 + 成员级禁言列表）。
func (p *Provider) GetGroupMuteState(ctx context.Context, cfg connectors.Config, chat string) (GroupMuteState, error) {
	endpoint, err := p.groupEndpoint(cfg, chat, "/restrict_chat_setting")
	if err != nil {
		return GroupMuteState{}, err
	}
	var state GroupMuteState
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &state); err != nil {
		return GroupMuteState{}, err
	}
	return state, nil
}

// GroupMuteChange 是一次成员禁言变更（官方 SetMemberMuteState）。
type GroupMuteChange struct {
	Op           string `json:"op"` // add | update | del
	MemberOpenID string `json:"member_openid"`
	MuteExpireAt string `json:"mute_expire_at,omitempty"`
}

// SetGroupMemberMute 设置成员禁言（单次最多 20 个；最大 30 天，由平台校验）。
func (p *Provider) SetGroupMemberMute(ctx context.Context, cfg connectors.Config, chat string, changes []GroupMuteChange) error {
	if len(changes) == 0 {
		return fmt.Errorf("qqbot: at least one mute change is required")
	}
	if len(changes) > groupMuteLimit {
		return fmt.Errorf("qqbot: mute changes exceed limit %d", groupMuteLimit)
	}
	normalized := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		op := strings.ToLower(strings.TrimSpace(change.Op))
		if op != "add" && op != "update" && op != "del" {
			return fmt.Errorf("qqbot: mute op must be add/update/del, got %q", change.Op)
		}
		member := strings.TrimSpace(change.MemberOpenID)
		if member == "" {
			return fmt.Errorf("qqbot: mute change missing member_openid")
		}
		item := map[string]any{"op": op, "member_openid": member}
		if expire := strings.TrimSpace(change.MuteExpireAt); expire != "" {
			item["mute_expire_at"] = expire
		}
		normalized = append(normalized, item)
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/restrict_chat_setting")
	if err != nil {
		return err
	}
	return p.apiJSON(ctx, cfg, http.MethodPost, endpoint, map[string]any{"members": normalized}, nil)
}

// ListGroupJoinRequests 拉取入群申请列表（官方：limit 默认 20、最大 50）。
func (p *Provider) ListGroupJoinRequests(ctx context.Context, cfg connectors.Config, chat, cursor string, limit int) (GroupJoinRequestPage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/join_request_list")
	if err != nil {
		return GroupJoinRequestPage{}, err
	}
	query := url.Values{}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		query.Set("cursor", cursor)
	}
	query.Set("limit", strconv.Itoa(limit))
	endpoint += "?" + query.Encode()
	var page GroupJoinRequestPage
	if err := p.apiJSON(ctx, cfg, http.MethodGet, endpoint, nil, &page); err != nil {
		return GroupJoinRequestPage{}, err
	}
	return page, nil
}

// ApproveGroupJoinRequest 审批入群申请：op=approve|decline。
// decline 时可填 rejectReason 与 addToBlacklist。
func (p *Provider) ApproveGroupJoinRequest(ctx context.Context, cfg connectors.Config, chat, memberOpenID, op, joinRequestID, rejectReason string, addToBlacklist bool) error {
	memberOpenID = strings.TrimSpace(memberOpenID)
	if memberOpenID == "" {
		return fmt.Errorf("qqbot: member openid is required")
	}
	op = strings.ToLower(strings.TrimSpace(op))
	if op != "approve" && op != "decline" {
		return fmt.Errorf("qqbot: approval op must be approve or decline")
	}
	endpoint, err := p.groupEndpoint(cfg, chat, "/approval_join_request/"+url.PathEscape(memberOpenID))
	if err != nil {
		return err
	}
	payload := map[string]any{"op": op}
	if id := strings.TrimSpace(joinRequestID); id != "" {
		payload["join_request_id"] = id
	}
	if op == "decline" {
		if reason := strings.TrimSpace(rejectReason); reason != "" {
			payload["reject_reason"] = reason
		}
		if addToBlacklist {
			payload["add_to_member_blacklist"] = true
		}
	}
	return p.apiJSON(ctx, cfg, http.MethodPost, endpoint, payload, nil)
}

// normalizeOpenIDs 去空、去重并校验批量上限。
func normalizeOpenIDs(ids []string, limit int) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("qqbot: at least one openid is required")
	}
	if len(out) > limit {
		return nil, fmt.Errorf("qqbot: openid count %d exceeds limit %d", len(out), limit)
	}
	return out, nil
}
