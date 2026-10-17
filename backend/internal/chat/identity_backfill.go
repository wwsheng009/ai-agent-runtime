package chat

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// BackfillCanonicalIdentities 是 P3 的存量迁移原语
// （session-message-identity-protocol-20261017 §5 Backfill）：为无 message_id
// 的历史行铸造身份。幂等：已带 id 的行原样保留，重跑安全。
//
// 边界说明：这里只授予 message_id（行级身份），不伪造 turn_id——存量行的 turn
// 归属无法从行本身可靠推导，伪造会污染归属审计；无 turn_id 的存量行继续由
// 读取端按前驱继承展示。CLI 侧的 `aicli session backfill-identities` 命令在
// Load→本函数→Update 的循环上包装进度与统计。
//
// 返回本次实际授予的行数。
func BackfillCanonicalIdentities(messages []types.Message) int {
	granted := 0
	for index := range messages {
		message := &messages[index]
		if strings.TrimSpace(types.MessageID(*message)) != "" {
			continue
		}
		if message.Metadata == nil {
			message.Metadata = types.NewMetadata()
		}
		message.Metadata.Set(types.MetadataKeyMessageID, "msg_"+strings.ReplaceAll(uuidNewString(), "-", ""))
		granted++
	}
	return granted
}

// uuidNewString 间接引用 uuid，保持本文件可读性（迁移原语集中在此）。
func uuidNewString() string {
	return newUUID()
}
