package runtimeapi

import (
	"net/http/httptest"
	"testing"
	"time"
)

// resolveStreamFlushInterval 是两个 SSE 入口（会话运行时流、直连回合流）
// 共用的 flush_ms 解析口径：缺省 50ms、显式 0 关闭合并、非法/越界报错。
func TestResolveStreamFlushInterval(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "缺省", raw: "", want: streamFlushTickInterval},
		{name: "零值关闭合并", raw: "0", want: 0},
		{name: "自定义窗口", raw: "25", want: 25 * time.Millisecond},
		{name: "上界", raw: "1000", want: time.Second},
		{name: "非法值", raw: "abc", wantErr: true},
		{name: "负数越界", raw: "-1", wantErr: true},
		{name: "超过上界", raw: "1001", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/agent/chat"
			if tc.raw != "" {
				target += "?flush_ms=" + tc.raw
			}
			req := httptest.NewRequest("POST", target, nil)
			got, err := resolveStreamFlushInterval(req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveStreamFlushInterval(%q) = %v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStreamFlushInterval(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("resolveStreamFlushInterval(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}

	// nil 请求（防御性分支）退回默认窗口而不是 panic。
	got, err := resolveStreamFlushInterval(nil)
	if err != nil || got != streamFlushTickInterval {
		t.Fatalf("resolveStreamFlushInterval(nil) = (%v, %v), want (%v, nil)", got, err, streamFlushTickInterval)
	}
}
