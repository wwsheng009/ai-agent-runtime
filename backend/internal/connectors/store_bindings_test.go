package connectors

import "testing"

func TestMemoryStoreListBindings(t *testing.T) {
	store := NewMemoryStore()
	seed := []Binding{
		{ConnectorID: "c2", Chat: "chat-b", Session: SessionRef{SessionID: "s2"}},
		{ConnectorID: "c1", Chat: "chat-b", Session: SessionRef{SessionID: "s1"}},
		{ConnectorID: "c1", Chat: "chat-a", Session: SessionRef{SessionID: "s1"}},
	}
	for _, binding := range seed {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}

	all := store.ListBindings("")
	if len(all) != 3 {
		t.Fatalf("ListBindings(全部) = %d 条, want 3", len(all))
	}
	// 输出按 connector+chat 排序（map 无序，排序保证确定性）。
	want := []struct{ connector, chat string }{{"c1", "chat-a"}, {"c1", "chat-b"}, {"c2", "chat-b"}}
	for i, expect := range want {
		if all[i].ConnectorID != expect.connector || all[i].Chat != expect.chat {
			t.Fatalf("all[%d] = %s/%s, want %s/%s", i, all[i].ConnectorID, all[i].Chat, expect.connector, expect.chat)
		}
	}

	c1 := store.ListBindings("c1")
	if len(c1) != 2 || c1[0].Chat != "chat-a" || c1[1].Chat != "chat-b" {
		t.Fatalf("ListBindings(c1) = %+v, want chat-a/chat-b", c1)
	}
	if c1[0].Session != (SessionRef{SessionID: "s1"}) {
		t.Fatalf("session = %+v, want s1", c1[0].Session)
	}
	if missing := store.ListBindings("nope"); len(missing) != 0 {
		t.Fatalf("未知连接器应返回空列表: %+v", missing)
	}

	// 删除配置级联清理绑定。
	if err := store.DeleteConfig("c1"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if got := store.ListBindings("c1"); len(got) != 0 {
		t.Fatalf("DeleteConfig 后应无绑定: %+v", got)
	}
	if got := store.ListBindings(""); len(got) != 1 || got[0].ConnectorID != "c2" {
		t.Fatalf("剩余绑定 = %+v, want c2/chat-b", got)
	}
}
