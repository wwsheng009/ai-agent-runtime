package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// PreviewNeedsHistory 的判定必须与 BuildPreview/effectiveTitle 的实际取值路径
// 一致：返回 false 的行不需要去存储读历史，可以用元数据零 Load 渲染出与完整
// 加载相同的标题/摘要。
func TestSessionPreviewNeedsHistory(t *testing.T) {
	// metadataRow 模拟 ListMetadataPage 返回的元数据行（历史未装载）。
	metadataRow := func() *Session {
		session := NewSession("preview-user")
		session.HistoryLoaded = false
		session.History = nil
		return session
	}

	cases := []struct {
		name    string
		session *Session
		want    bool
	}{
		{name: "nil", session: nil, want: true},
		{
			name: "loaded history never needs a read",
			session: func() *Session {
				s := NewSession("preview-user")
				s.AddMessage(*types.NewUserMessage("第一条用户消息"))
				if !s.HistoryLoaded {
					t.Fatal("new session must report loaded history")
				}
				return s
			}(),
			want: false,
		},
		{
			name: "metadata title and summary render without history",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "已有标题"
				s.Metadata.TitleSource = sessionTitleSourceDerived
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: false,
		},
		{
			name: "empty summary falls back to last message",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "已有标题"
				s.Metadata.TitleSource = sessionTitleSourceDerived
				return s
			}(),
			want: true,
		},
		{
			name: "empty title is derived from history",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: true,
		},
		{
			name: "manual title keeps stored value",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "手动标题"
				s.Metadata.TitleSource = sessionTitleSourceManual
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: false,
		},
		{
			name: "manual title with empty summary still needs history",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "手动标题"
				s.Metadata.TitleSource = sessionTitleSourceManual
				return s
			}(),
			want: true,
		},
		{
			name: "compact title keeps stored value",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "标题 · compact #3"
				s.Metadata.TitleSource = sessionTitleSourceCompact
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: false,
		},
		{
			name: "legacy compaction-summary title is re-derived",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "compacted context from earlier turns: 摘要"
				s.Metadata.TitleSource = sessionTitleSourceDerived
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: true,
		},
		{
			name: "legacy instruction-polluted title is re-derived",
			session: func() *Session {
				s := metadataRow()
				s.Metadata.Title = "Shell guidance: - Detected operating system: windows"
				s.Metadata.TitleSource = sessionTitleSourceDerived
				s.Metadata.Summary = "已有摘要"
				return s
			}(),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.session.PreviewNeedsHistory(); got != tc.want {
				t.Fatalf("PreviewNeedsHistory() = %v, want %v", got, tc.want)
			}
		})
	}
}

// GetMetadata 必须只读元数据单行：不反序列化 prompt 投影，且过期语义与 Get 一致。
func TestSessionManagerGetMetadataSkipsHistory(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	manager := NewSessionManager(store, nil)
	t.Cleanup(manager.Stop)

	session, err := manager.Create(ctx, "metadata-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	session.Metadata.Title = "元数据标题"
	session.Metadata.Summary = "元数据摘要"
	session.AddMessage(*types.NewUserMessage("一段对话"))
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	meta, err := manager.GetMetadata(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if meta.HistoryLoaded || len(meta.History) != 0 {
		t.Fatalf("GetMetadata loaded history: loaded=%v len=%d", meta.HistoryLoaded, len(meta.History))
	}
	if meta.Metadata.Title != "元数据标题" {
		t.Fatalf("title not preserved: %#v", meta.Metadata)
	}
	// Save 会按既有规则从最后一条消息派生摘要（"一段对话"）；
	// 这里只要求元数据读取本身不加载历史、摘要字段与存储一致。
	if meta.Metadata.Summary != "一段对话" {
		t.Fatalf("summary = %q, want derived %q", meta.Metadata.Summary, "一段对话")
	}
	if meta.MessageCount() == 0 {
		t.Fatal("message count must come from the metadata row")
	}

	// 强证明：删掉 prompt 投影表后元数据读取仍必须成功（说明它不触碰历史）。
	if _, err := store.db.ExecContext(ctx, `DROP TABLE session_prompt_messages`); err != nil {
		t.Fatalf("drop session_prompt_messages: %v", err)
	}
	if _, err := manager.GetMetadata(ctx, session.ID); err != nil {
		t.Fatalf("GetMetadata after dropping history table: %v", err)
	}
	if _, err := manager.Get(ctx, session.ID); err == nil {
		t.Fatal("Get must fail once the prompt projection table is gone")
	}
}

func TestSessionManagerGetMetadataKeepsExpirySemantics(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	manager := NewSessionManager(store, nil)
	t.Cleanup(manager.Stop)

	session, err := manager.Create(ctx, "metadata-user")
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	expired := time.Now().Add(-time.Hour)
	session.ExpiresAt = &expired
	if err := manager.Update(ctx, session); err != nil {
		t.Fatalf("manager.Update: %v", err)
	}

	if _, err := manager.GetMetadata(ctx, session.ID); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("GetMetadata err = %v, want ErrSessionExpired", err)
	}
	// 与 Get 同语义：过期即回收，随后读取报 not found。
	if _, err := manager.GetMetadata(ctx, session.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetMetadata after expiry err = %v, want ErrSessionNotFound", err)
	}
}
