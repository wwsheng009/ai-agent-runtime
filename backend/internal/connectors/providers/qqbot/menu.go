// 全局自定义菜单（官方 2026-08/10 新增）：
//
//	GET /v2/menu   查询（30 QPM）
//	PUT /v2/menu   修改（5 QPM）
//
// 仅支持 C2C（单聊）场景，设置后对所有用户生效，不支持按用户区分。
// MenuItem.type：switch（开关）/ send_message（发送消息）/ link（链接）/
// menu（含子菜单的折叠项）；子菜单最多 5 个且不支持再嵌套。
package qqbot

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// 官方菜单限制。
const (
	menuMaxItems    = 10
	menuMaxSubItems = 5
)

// QQMenu 是全局自定义菜单。
type QQMenu struct {
	Items []QQMenuItem `json:"items"`
}

// QQMenuItem 是菜单项。
type QQMenuItem struct {
	Name         string          `json:"name"`
	Type         string          `json:"type"` // switch | send_message | link | menu
	SubMenuItems []QQSubMenuItem `json:"sub_menu_items,omitempty"`
	SendMessage  string          `json:"send_message,omitempty"`
	Link         string          `json:"link,omitempty"`
	Switch       *QQMenuSwitch   `json:"switch,omitempty"`
}

// QQSubMenuItem 是二级菜单项（仅 send_message / link）。
type QQSubMenuItem struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	SendMessage string `json:"send_message,omitempty"`
	Link        string `json:"link,omitempty"`
}

// QQMenuSwitch 是开关配置。
type QQMenuSwitch struct {
	SwitchID string `json:"switch_id"`
	Default  bool   `json:"default"`
}

// QQMenuState 是查询响应。
type QQMenuState struct {
	Version int     `json:"version"`
	Menu    *QQMenu `json:"menu"`
}

// GetMenu 查询当前全局自定义菜单。
func (p *Provider) GetMenu(ctx context.Context, cfg connectors.Config) (QQMenuState, error) {
	var state QQMenuState
	if err := p.apiJSON(ctx, cfg, http.MethodGet, p.baseFor(cfg)+"/v2/menu", nil, &state); err != nil {
		return QQMenuState{}, err
	}
	return state, nil
}

// SetMenu 修改全局自定义菜单（覆盖完整配置），返回新版本号。
func (p *Provider) SetMenu(ctx context.Context, cfg connectors.Config, menu QQMenu) (int, error) {
	if err := validateMenu(menu); err != nil {
		return 0, err
	}
	var response struct {
		Version int `json:"version"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPut, p.baseFor(cfg)+"/v2/menu",
		map[string]any{"menu": menu}, &response); err != nil {
		return 0, err
	}
	return response.Version, nil
}

// validateMenu 按官方限制做本地校验，避免注定失败的请求。
func validateMenu(menu QQMenu) error {
	if len(menu.Items) > menuMaxItems {
		return fmt.Errorf("qqbot: menu items exceed limit %d", menuMaxItems)
	}
	for i, item := range menu.Items {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		switch itemType {
		case "switch", "send_message", "link", "menu":
		default:
			return fmt.Errorf("qqbot: menu item %d has invalid type %q", i, item.Type)
		}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("qqbot: menu item %d missing name", i)
		}
		if itemType == "link" {
			if !strings.HasPrefix(strings.TrimSpace(item.Link), "https://") {
				return fmt.Errorf("qqbot: menu item %d link must start with https://", i)
			}
		}
		if itemType == "switch" {
			if item.Switch == nil || strings.TrimSpace(item.Switch.SwitchID) == "" {
				return fmt.Errorf("qqbot: menu item %d switch missing switch_id", i)
			}
		}
		if itemType != "menu" && len(item.SubMenuItems) > 0 {
			return fmt.Errorf("qqbot: menu item %d has sub items but type is not menu", i)
		}
		if len(item.SubMenuItems) > menuMaxSubItems {
			return fmt.Errorf("qqbot: menu item %d sub items exceed limit %d", i, menuMaxSubItems)
		}
		for j, sub := range item.SubMenuItems {
			subType := strings.ToLower(strings.TrimSpace(sub.Type))
			if subType != "send_message" && subType != "link" {
				return fmt.Errorf("qqbot: sub item %d/%d type must be send_message or link", i, j)
			}
			if subType == "link" && !strings.HasPrefix(strings.TrimSpace(sub.Link), "https://") {
				return fmt.Errorf("qqbot: sub item %d/%d link must start with https://", i, j)
			}
		}
	}
	return nil
}
