package connectors

import (
	"context"
	"strings"
	"testing"
)

func TestParseOutboundMediaDirectives(t *testing.T) {
	qqFences := []string{"qq-attachments", "attachments"}
	cases := []struct {
		name      string
		text      string
		fences    []string
		wantText  string
		wantMedia []Media
	}{
		{
			name:     "channel fence with image",
			text:     "配图\n```qq-attachments\nimage /tmp/pic.png\n```",
			fences:   qqFences,
			wantText: "配图",
			wantMedia: []Media{
				{Kind: "image", Path: "/tmp/pic.png", FileName: "pic.png"},
			},
		},
		{
			name:     "generic fence with file and remote video",
			text:     "```attachments\nfile /tmp/doc.pdf\nvideo https://cdn.example/v.mp4\n```",
			fences:   qqFences,
			wantText: "",
			wantMedia: []Media{
				{Kind: "file", Path: "/tmp/doc.pdf", FileName: "doc.pdf"},
				{Kind: "video", URL: "https://cdn.example/v.mp4"},
			},
		},
		{
			name:      "directive line",
			text:      "FILE:/tmp/a.zip\n正文",
			fences:    qqFences,
			wantText:  "正文",
			wantMedia: []Media{{Kind: "file", Path: "/tmp/a.zip", FileName: "a.zip"}},
		},
		{
			name:      "voice kept as voice",
			text:      "VOICE:/tmp/v.silk",
			fences:    qqFences,
			wantText:  "",
			wantMedia: []Media{{Kind: "voice", Path: "/tmp/v.silk", FileName: "v.silk"}},
		},
		{
			name:      "heading block with bare spec",
			text:      "说明\nqq-attachments:\nimage /tmp/d.png\n\n后续",
			fences:    qqFences,
			wantText:  "说明\n后续",
			wantMedia: []Media{{Kind: "image", Path: "/tmp/d.png", FileName: "d.png"}},
		},
		{
			name:     "bare spec infers kind by extension",
			text:     "```qq-attachments\n/tmp/raw.png\n```",
			fences:   qqFences,
			wantText: "",
			wantMedia: []Media{
				{Kind: "image", Path: "/tmp/raw.png", FileName: "raw.png"},
			},
		},
		{
			name:     "other channel fence untouched",
			text:     "```wechat-attachments\nimage /tmp/x.png\n```",
			fences:   qqFences,
			wantText: "```wechat-attachments\nimage /tmp/x.png\n```",
		},
		{
			name:     "plain text unchanged",
			text:     "你好，没有媒体指令。",
			fences:   qqFences,
			wantText: "你好，没有媒体指令。",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			visible, media := ParseOutboundMediaDirectives(testCase.text, testCase.fences...)
			if visible != testCase.wantText {
				t.Fatalf("visible text = %q, want %q", visible, testCase.wantText)
			}
			if len(media) != len(testCase.wantMedia) {
				t.Fatalf("media = %+v, want %+v", media, testCase.wantMedia)
			}
			for index := range media {
				if media[index] != testCase.wantMedia[index] {
					t.Fatalf("media[%d] = %+v, want %+v", index, media[index], testCase.wantMedia[index])
				}
			}
		})
	}
}

func TestMediaDirectiveNote(t *testing.T) {
	note := MediaDirectiveNote("qq-attachments")
	for _, want := range []string{"qq-attachments", "image <absolute-path-or-https-url>", "video", "file"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q, missing %q", note, want)
		}
	}
	if got := MediaDirectiveNote(""); !strings.Contains(got, "attachments") {
		t.Fatalf("default fence note = %q", got)
	}
}

// noteProvider 在 fakeProvider 上叠加出站媒体提示能力（OutboundMediaNoteProvider）。
type noteProvider struct {
	*fakeProvider
	note string
}

func (p *noteProvider) OutboundMediaNote() string { return p.note }

func TestServiceInjectsOutboundMediaNote(t *testing.T) {
	note := MediaDirectiveNote("qq-attachments")
	provider := &noteProvider{fakeProvider: &fakeProvider{}, note: note}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "ok"}}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "backend handled", func() bool { return len(backend.requests()) == 1 })
	first := backend.requests()[0].Text
	if !strings.HasPrefix(first, "hi\n\n") || !strings.HasSuffix(first, note) {
		t.Fatalf("injected text = %q", first)
	}

	// 文本已包含提示时不重复注入。
	second := "hi2\n\n" + note
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", second)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second handled", func() bool { return len(backend.requests()) == 2 })
	if got := backend.requests()[1].Text; strings.Count(got, note) != 1 {
		t.Fatalf("note duplicated: %q", got)
	}
}
