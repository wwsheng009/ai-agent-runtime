// Package api 定义连接器的对外契约：Provider Schema（前端渲染的唯一来源）、
// 视图类型与字段校验/脱敏助手。
//
// 目标架构见 docs/bots/06-target-architecture.md §2.5 与配置设计
// docs/bots/07-config-ux.md §5.2/§7：
//   - 三个前端（gateway console / aicli 微前端 / runtime-server React）
//     共用同一份 Schema JSON，渲染器只做"字段 → 控件"的机械映射；
//   - 密钥只写不读：视图返回 secret_keys（字段名）而非明文；
//   - 未知字段类型由前端降级为文本输入，核心可先行升级。
//
// 阶段说明（P-1 骨架）：本包当前只锁定"契约类型 + 校验/脱敏"这两层稳定接口；
// HTTP handler 构造器（含 Authorizer 注入）与三宿主契约测试矩阵在 P0 首个
// 宿主接线时一起落地，避免先写空壳路由。
package api

import (
	"fmt"
	"sort"
	"strings"
)

// FieldType 是前端控件类型的固定枚举（docs/bots/07-config-ux.md §7）。
type FieldType string

const (
	FieldText   FieldType = "text"
	FieldSecret FieldType = "secret"
	FieldURL    FieldType = "url"
	FieldBool   FieldType = "bool"
	FieldEnum   FieldType = "enum"
	FieldNumber FieldType = "number"
)

// FieldSchema 描述一个配置字段。
type FieldSchema struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	Help        string    `json:"help,omitempty"`
	Default     string    `json:"default,omitempty"`
	Options     []Option  `json:"options,omitempty"` // 仅 enum
	SecretKey   bool      `json:"-"`                 // 由 Type==secret 推导，不参与 JSON
}

// Secret 报告该字段是否为密钥（API 不回显其值）。
func (f FieldSchema) IsSecret() bool { return f.Type == FieldSecret }

// Option 是 enum 字段的一个取值。
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ActionSpec 是通道特有动作（如微信/QQ 扫码登录）。
type ActionSpec struct {
	Key   string `json:"key"`   // 如 start_login / login_status
	Label string `json:"label"` // 按钮文案
	Kind  string `json:"kind"`  // qr | button
}

// ProviderSchema 描述一个通道（provider）的全部配置面。
type ProviderSchema struct {
	Provider string        `json:"provider"`
	Label    string        `json:"label"`
	Modes    []string      `json:"modes,omitempty"` // 如 polling / webhook / gateway
	Fields   []FieldSchema `json:"fields"`
	Actions  []ActionSpec  `json:"actions,omitempty"`
}

// Schema 是三端渲染器的输入（GET /api/connectors/schema 的载荷）。
type Schema struct {
	Version   string           `json:"version"` // 契约版本，字段新增向后兼容
	Providers []ProviderSchema `json:"providers"`
}

// FieldError 是字段级校验错误（前端据此定位输入框）。
type FieldError struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Validate 校验用户提交的字段值：
//   - 必填字段缺失（空串）→ 错误；对密钥字段，已存在（existing 中有值）且本次未提交视为"保持不变"；
//   - enum 取值必须在 options 内；
//   - 未知 key 由调用方决定是否拒绝（此处忽略，便于向前兼容）。
//
// values 是本次提交的 settings+secrets（合并视图），existing 是已存值（可为 nil）。
func Validate(schema ProviderSchema, values map[string]string, existing map[string]string) []FieldError {
	var errs []FieldError
	for _, field := range schema.Fields {
		value, submitted := values[field.Key]
		value = strings.TrimSpace(value)
		if !submitted || value == "" {
			if !field.Required {
				continue
			}
			if _, ok := existing[field.Key]; ok {
				continue // 已配置且本次留空 = 保持不变
			}
			errs = append(errs, FieldError{Key: field.Key, Message: fmt.Sprintf("%s 为必填项", field.Label)})
			continue
		}
		if field.Type == FieldEnum && len(field.Options) > 0 {
			allowed := false
			for _, opt := range field.Options {
				if opt.Value == value {
					allowed = true
					break
				}
			}
			if !allowed {
				errs = append(errs, FieldError{Key: field.Key, Message: fmt.Sprintf("%s 取值不合法", field.Label)})
			}
		}
	}
	return errs
}

// SortedSecretKeys 返回密钥字段名（排序后）——对外视图只暴露字段名，不回明文。
func SortedSecretKeys(schema ProviderSchema, secrets map[string]string) []string {
	if len(secrets) == 0 {
		return nil
	}
	secretFields := make(map[string]struct{})
	for _, field := range schema.Fields {
		if field.IsSecret() {
			secretFields[field.Key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(secrets))
	for key, value := range secrets {
		if _, ok := secretFields[key]; !ok {
			continue
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// MergeSubmit 把本次提交合并到已存值上，实现密钥的"留空=不改"语义：
//   - 非密钥字段：以本次提交为准（空串表示清空）；
//   - 密钥字段：本次为空 → 保留旧值；提交特殊值 ClearSecret 表示显式清除。
func MergeSubmit(schema ProviderSchema, existing, submitted map[string]string) map[string]string {
	merged := make(map[string]string, len(existing)+len(submitted))
	for key, value := range existing {
		merged[key] = value
	}
	secretKeys := make(map[string]struct{})
	for _, field := range schema.Fields {
		if field.IsSecret() {
			secretKeys[field.Key] = struct{}{}
		}
	}
	for key, value := range submitted {
		if _, isSecret := secretKeys[key]; isSecret {
			value = strings.TrimSpace(value)
			if value == "" {
				continue // 留空 = 保持原值
			}
			if value == ClearSecret {
				delete(merged, key)
				continue
			}
		}
		merged[key] = value
	}
	return merged
}

// ClearSecret 是显式清除密钥的提交值（UI 用「清除」按钮产生，不要求用户手输）。
const ClearSecret = "__clear__"
