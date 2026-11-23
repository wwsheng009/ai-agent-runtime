package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type chatMemoryRollbackArgs struct {
	ID               string
	TargetVersion    int64
	ExpectedRevision int64
	Reason           string
	Yes              bool
	RevertSupersede  bool
}

func parseChatMemoryHistoryArgs(argument string) (string, bool) {
	fields := splitChatCommandFields(argument)
	if len(fields) != 1 {
		return "", false
	}
	id := strings.TrimSpace(fields[0])
	return id, id != "" && !strings.HasPrefix(id, "-")
}

func parseChatMemoryRollbackArgs(argument string) (chatMemoryRollbackArgs, bool) {
	var args chatMemoryRollbackArgs
	fields := splitChatCommandFields(argument)
	if len(fields) < 2 {
		return args, false
	}
	args.ID = strings.TrimSpace(fields[0])
	if args.ID == "" || strings.HasPrefix(args.ID, "-") {
		return chatMemoryRollbackArgs{}, false
	}
	target, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || target <= 0 {
		return chatMemoryRollbackArgs{}, false
	}
	args.TargetVersion = target

	seenExpectedRevision := false
	seenReason := false
	seenYes := false
	seenRevert := false
	for index := 2; index < len(fields); index++ {
		switch strings.ToLower(strings.TrimSpace(fields[index])) {
		case "--expected-revision":
			if seenExpectedRevision || index+1 >= len(fields) {
				return chatMemoryRollbackArgs{}, false
			}
			index++
			revision, parseErr := strconv.ParseInt(fields[index], 10, 64)
			if parseErr != nil || revision <= 0 {
				return chatMemoryRollbackArgs{}, false
			}
			args.ExpectedRevision = revision
			seenExpectedRevision = true
		case "--reason":
			if seenReason || index+1 >= len(fields) {
				return chatMemoryRollbackArgs{}, false
			}
			index++
			args.Reason = strings.TrimSpace(fields[index])
			if args.Reason == "" {
				return chatMemoryRollbackArgs{}, false
			}
			seenReason = true
		case "--yes":
			if seenYes {
				return chatMemoryRollbackArgs{}, false
			}
			args.Yes = true
			seenYes = true
		case "--revert-supersede":
			if seenRevert {
				return chatMemoryRollbackArgs{}, false
			}
			args.RevertSupersede = true
			seenRevert = true
		default:
			return chatMemoryRollbackArgs{}, false
		}
	}
	if !seenExpectedRevision || !seenReason || !args.Yes {
		return chatMemoryRollbackArgs{}, false
	}
	return args, true
}

func chatMemoryRollbackErrorMessage(err error) string {
	if err == nil {
		return "memory rollback 失败（内部诊断已隐藏）"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "changed concurrently"):
		return "冲突：记录 revision 已变化；请重新执行 /memory history 并使用当前 --expected-revision"
	case strings.Contains(message, "revert_supersede"):
		return "回滚被拒绝：重新激活已 supersede 的记录需要显式传入 --revert-supersede"
	case strings.Contains(message, "successor"):
		return "回滚被拒绝：后继记录已变化；请先重新检查相关记录"
	case strings.Contains(message, "not found"), strings.Contains(message, "no rows"):
		return "回滚目标不存在（记录或版本不可用）"
	case strings.Contains(message, "maintenance"), strings.Contains(message, "purge"), strings.Contains(message, "fence"):
		return "回滚被安全约束阻止（maintenance/purge fence）"
	default:
		return "memory rollback 失败（参数、记录状态或安全约束不满足；内部诊断已隐藏）"
	}
}

func memoryHistoryIntervalLabel(from, until time.Time) string {
	start, end := "—", "—"
	if !from.IsZero() {
		start = from.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	if !until.IsZero() {
		end = until.Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	return fmt.Sprintf("[%s, %s)", start, end)
}
