package qqbot

// 群管理接口（官方 2026-08/09）契约回归：端点、分页参数、批量上限与校验。

import (
	"context"
	"strings"
	"testing"
)

func TestGroupMemberQueries(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	page, err := provider.ListGroupMembers(context.Background(), cfg, "group:g1", "cursor-1")
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(page.Members) != 1 || page.Members[0].MemberOpenID != "m1" || page.NextCursor != "next-1" {
		t.Fatalf("page = %+v", page)
	}
	member, err := provider.GetGroupMember(context.Background(), cfg, "group:g1", "m1")
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if member.MemberRole != "admin" {
		t.Fatalf("member = %+v", member)
	}

	sent := fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %+v", sent)
	}
	if sent[0].path != "/v2/groups/g1/members?cursor=cursor-1" {
		t.Fatalf("list path = %s", sent[0].path)
	}
	if sent[1].path != "/v2/groups/g1/members/m1?" {
		t.Fatalf("member path = %s", sent[1].path)
	}
}

func TestGroupBlacklistQueries(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	page, err := provider.ListGroupBlacklist(context.Background(), cfg, "group:g1", "", 500)
	if err != nil {
		t.Fatalf("list blacklist: %v", err)
	}
	if len(page.Users) != 1 || page.Users[0].MemberOpenID != "b1" {
		t.Fatalf("page = %+v", page)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].path, "limit=100") {
		t.Fatalf("limit must clamp to 100: %+v", sent)
	}
}

func TestGroupMuteQueries(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	state, err := provider.GetGroupMuteState(context.Background(), cfg, "group:g1")
	if err != nil {
		t.Fatalf("get mute state: %v", err)
	}
	if state.GlobalRule.Mode != "none" {
		t.Fatalf("state = %+v", state)
	}
}

func TestGroupJoinRequestQueries(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	page, err := provider.ListGroupJoinRequests(context.Background(), cfg, "group:g1", "", 0)
	if err != nil {
		t.Fatalf("list join requests: %v", err)
	}
	if len(page.List) != 1 || page.List[0].JoinRequestID != "jr-1" {
		t.Fatalf("page = %+v", page)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || !strings.Contains(sent[0].path, "limit=20") {
		t.Fatalf("default limit must be 20: %+v", sent)
	}
}

func TestGroupManagementRejectsNonGroupChat(t *testing.T) {
	provider := New()
	cfg := testConfig("qq-1", "app-1", "secret-1")
	if _, err := provider.ListGroupMembers(context.Background(), cfg, "user:u1", ""); err == nil {
		t.Fatal("user chat must be rejected")
	}
	if _, err := provider.GetGroupMuteState(context.Background(), cfg, "channel:c1"); err == nil {
		t.Fatal("channel chat must be rejected")
	}
}

func TestGroupBatchLimitsAndValidation(t *testing.T) {
	provider := New()
	cfg := testConfig("qq-1", "app-1", "secret-1")

	many := make([]string, 21)
	for i := range many {
		many[i] = "m" + string(rune('a'+i))
	}
	if _, err := provider.BatchRemoveGroupMembers(context.Background(), cfg, "group:g1", many, false); err == nil {
		t.Fatal("batch remove over 20 must be rejected")
	}
	if _, err := provider.UpdateGroupBlacklist(context.Background(), cfg, "group:g1", "add", many); err == nil {
		t.Fatal("blacklist over 20 must be rejected")
	}
	if err := provider.SetGroupMemberMute(context.Background(), cfg, "group:g1", make([]GroupMuteChange, 21)); err == nil {
		t.Fatal("mute over 20 must be rejected")
	}
	if err := provider.SetGroupMemberMute(context.Background(), cfg, "group:g1",
		[]GroupMuteChange{{Op: "bad", MemberOpenID: "m1"}}); err == nil {
		t.Fatal("invalid mute op must be rejected")
	}
	if _, err := provider.UpdateGroupBlacklist(context.Background(), cfg, "group:g1", "bad", []string{"m1"}); err == nil {
		t.Fatal("invalid blacklist op must be rejected")
	}
	if err := provider.ApproveGroupJoinRequest(context.Background(), cfg, "group:g1", "m1", "bad", "", "", false); err == nil {
		t.Fatal("invalid approval op must be rejected")
	}
}

func TestNormalizeOpenIDs(t *testing.T) {
	ids, err := normalizeOpenIDs([]string{" a ", "a", "", "b"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	if _, err := normalizeOpenIDs(nil, 20); err == nil {
		t.Fatal("empty ids must be rejected")
	}
}

// 写操作端点与载荷（假平台记录 POST /v2/ 路径）。
func TestGroupWriteEndpoints(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if _, err := provider.BatchRemoveGroupMembers(context.Background(), cfg, "group:g1",
		[]string{"m1"}, true); err != nil {
		t.Fatalf("batch remove: %v", err)
	}
	if _, err := provider.UpdateGroupBlacklist(context.Background(), cfg, "group:g1",
		"add", []string{"m1"}); err != nil {
		t.Fatalf("blacklist: %v", err)
	}
	if err := provider.SetGroupMemberMute(context.Background(), cfg, "group:g1",
		[]GroupMuteChange{{Op: "add", MemberOpenID: "m1", MuteExpireAt: "2026-08-05T11:23:05+08:00"}}); err != nil {
		t.Fatalf("mute: %v", err)
	}
	if err := provider.ApproveGroupJoinRequest(context.Background(), cfg, "group:g1",
		"m2", "decline", "jr-1", "理由", true); err != nil {
		t.Fatalf("approval: %v", err)
	}

	sent := fake.sentMessages()
	paths := make(map[string]bool, len(sent))
	for _, item := range sent {
		paths[item.path] = true
	}
	for _, want := range []string{
		"/v2/groups/g1/batch_remove_members",
		"/v2/groups/g1/member_blacklist",
		"/v2/groups/g1/restrict_chat_setting",
		"/v2/groups/g1/approval_join_request/m2",
	} {
		if !paths[want] {
			t.Fatalf("missing endpoint %s in %+v", want, sent)
		}
	}
}
