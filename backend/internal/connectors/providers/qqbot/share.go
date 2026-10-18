// 生成分享链接（官方 2026-09-16）：
//
//	POST /v2/generate_url_link
//	body: {callback_data?}（最长 32 字符）
//	resp: {data:{url}}
//
// 用于邀请用户添加机器人为好友；用户通过链接添加时 callback_data 会随
// FRIEND_ADD 事件的 scene_param 回传。
package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const shareCallbackDataMax = 32

// GenerateShareLink 生成机器人分享链接（callbackData 可空，最长 32 字符）。
func (p *Provider) GenerateShareLink(ctx context.Context, cfg connectors.Config, callbackData string) (string, error) {
	callbackData = strings.TrimSpace(callbackData)
	if len([]rune(callbackData)) > shareCallbackDataMax {
		return "", fmt.Errorf("qqbot: callback_data exceeds %d characters", shareCallbackDataMax)
	}
	payload := map[string]any{}
	if callbackData != "" {
		payload["callback_data"] = callbackData
	}
	var response struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, p.baseFor(cfg)+"/v2/generate_url_link", payload, &response); err != nil {
		return "", err
	}
	link := strings.TrimSpace(response.Data.URL)
	if link == "" {
		return "", fmt.Errorf("qqbot: generate_url_link returned empty url")
	}
	return link, nil
}
