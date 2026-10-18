package qqbot

// 官方富媒体限制（2026-07-21 文档）：
//   - 软限制：图片/语音 20MB、视频 30MB、文件 200MB；
//   - 硬限制：200MB；
//   - 超软限制降级为 file_type=4，超硬限制报错。

import (
	"strings"
	"testing"
)

func TestApplyMediaSizeLimits(t *testing.T) {
	cases := []struct {
		name     string
		fileType int
		size     int64
		want     int
		wantErr  bool
	}{
		{"图片未超软限", 1, mediaSoftLimitImage, 1, false},
		{"图片超软限降级", 1, mediaSoftLimitImage + 1, 4, false},
		{"视频未超软限", 2, mediaSoftLimitVideo, 2, false},
		{"视频超软限降级", 2, mediaSoftLimitVideo + 1, 4, false},
		{"语音超软限降级", 3, mediaSoftLimitVoice + 1, 4, false},
		{"文件刚好硬限", 4, mediaHardLimit, 4, false},
		{"文件超硬限", 4, mediaHardLimit + 1, 0, true},
		{"图片超硬限", 1, mediaHardLimit + 1, 0, true},
		{"未知大小不判定", 1, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyMediaSizeLimits(tc.fileType, tc.size)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "200MB") {
					t.Fatalf("err = %v, want hard limit error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("file_type = %d, want %d", got, tc.want)
			}
		})
	}
}
