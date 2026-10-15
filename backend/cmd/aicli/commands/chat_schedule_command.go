package commands

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// /schedule — 自动化任务（Schedule）查看命令（P4，方案见
// docs/automation-tasks/analysis-and-implementation-plan.md §7 / §9.2）。
//
// 运行时交互注册声明（与 /todos 同一机制，见 chat_todos_command.go 顶部注释）：
//
//	业务域: C12 任务与待办（自动化任务与 /todos 相邻；与 job_* 后台作业区分）
//	模式:   inline（只读目录文档；screen 面板留待后续）
//	生效域: read（只读快照；不写会话、不发 turn、零确认）
//
// 数据面与 frontend 面板 / micro web「任务」页签 / schedule_* 工具同源：
// 直接读 `session.LocalRuntimeHost.Schedule`（in-process；aicli 纯本地模式没有
// runtime-server，不伪造 HTTP 路径）。服务未启用时如实说明。
const (
	chatScheduleCommandName = "/schedule"
)

// executeStructuredScheduleCommand 渲染跨会话自动化任务目录。
func executeStructuredScheduleCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	var service *schedule.Service
	if session.LocalRuntimeHost != nil {
		service = session.LocalRuntimeHost.Schedule
	}
	if service == nil {
		return commandTextResult(
			"自动化任务未启用。\n" +
				"提示: 在 runtime 配置中设置 schedule.enabled: true（重启 aicli 生效），" +
				"或确认调度守护进程 scheduled 正在运行。")
	}
	filter := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	rows, err := chatScheduleCatalogTasks(service)
	if err != nil {
		return commandTextResult("自动化任务目录读取失败: " + err.Error())
	}
	// S3（§4.1）：目录头部展示调度者在岗状态（与 daemon /status 同口径）。
	presenceLine := chatSchedulePresenceLine(schedulehost.ProbePresence(schedule.SupportedSchemaVersion()))
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatScheduleDocument(rows, filter, presenceLine)}},
		Action: CommandContinue,
	}
}

// chatSchedulePresenceLine 渲染在岗状态行（S3 §4.1；数据面为
// schedulehost.Presence，与 daemon /status 的 presence 字段同源）。
func chatSchedulePresenceLine(presence schedulehost.Presence) string {
	switch presence.State {
	case schedulehost.PresenceOnline:
		line := fmt.Sprintf("调度者: 在岗 · %s (pid %d)", presence.HolderNodeID, presence.HolderPID)
		if !presence.LeaseExpiresAt.IsZero() {
			line += " · 租约至 " + presence.LeaseExpiresAt.Local().Format("15:04:05")
		}
		return line
	case schedulehost.PresenceIncompatible:
		return "调度者: 已在线但不兼容 — " + presence.Notice()
	default:
		return "调度者: 不在岗 — " + presence.Notice()
	}
}

// scheduleRow 是目录文档的一行投影。
type scheduleRow struct {
	id        string
	title     string
	kind      string
	frequency string
	zone      string
	sessionID string
	status    string
	target    time.Time
}

// chatScheduleCatalogTasks 读取并排序目录（与 catalog 端点同序：target 升序、id 字典序）。
func chatScheduleCatalogTasks(service *schedule.Service) ([]scheduleRow, error) {
	entries, err := service.Catalog()
	if err != nil {
		return nil, err
	}
	rows := make([]scheduleRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, scheduleRow{
			id:        entry.Record.ID,
			title:     entry.Record.Title,
			kind:      string(entry.Record.Kind),
			frequency: chatScheduleFrequency(entry.Record),
			zone:      entry.Record.TimeZone,
			sessionID: entry.SessionID,
			status:    string(entry.Status),
			target:    entry.Record.ScheduledAt,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].target.Equal(rows[j].target) {
			return rows[i].target.Before(rows[j].target)
		}
		return rows[i].id < rows[j].id
	})
	return rows, nil
}

// chatScheduleFrequency 是 kind 相关的频次摘要（与 frontend shared 同口径）。
func chatScheduleFrequency(record schedule.Record) string {
	switch record.Kind {
	case schedule.KindAfter:
		return fmt.Sprintf("%ds", record.AfterSeconds)
	case schedule.KindEvery:
		return fmt.Sprintf("%ds", record.EverySeconds)
	case schedule.KindDaily:
		return record.Time
	case schedule.KindWeekly:
		parts := make([]string, 0, len(record.Weekdays))
		for _, day := range record.Weekdays {
			parts = append(parts, fmt.Sprintf("%d", day))
		}
		return fmt.Sprintf("%s (%s)", record.Time, strings.Join(parts, ","))
	case schedule.KindCron:
		return record.Expression
	default:
		return ""
	}
}

// buildChatScheduleDocument 渲染目录文档。
func buildChatScheduleDocument(rows []scheduleRow, filter string, presenceLine string) render.Document {
	var builder strings.Builder
	builder.WriteString("自动化任务\n")
	builder.WriteString("==========\n\n")
	if presenceLine != "" {
		builder.WriteString(presenceLine)
		builder.WriteString("\n\n")
	}

	active := make([]scheduleRow, 0, len(rows))
	inactive := make([]scheduleRow, 0, len(rows))
	for _, row := range rows {
		if row.status == "inactive" {
			inactive = append(inactive, row)
		} else {
			active = append(active, row)
		}
	}

	switch filter {
	case "", "all", "list":
		// 全部
	case "active", "on":
		inactive = nil
	case "inactive", "done", "off":
		active = nil
	default:
		return buildChatPlainTextCommandDocument(
			fmt.Sprintf("未知过滤参数 %q（支持: 空|all|active|inactive）", filter))
	}

	if len(active) == 0 && len(inactive) == 0 {
		builder.WriteString("（还没有自动化任务；在会话中让模型调用 schedule_create 创建）\n")
		return buildChatPlainTextCommandDocument(builder.String())
	}

	if len(active) > 0 {
		builder.WriteString(fmt.Sprintf("已开启（%d）\n", len(active)))
		for _, row := range active {
			builder.WriteString(chatScheduleRowLine(row, true))
		}
		builder.WriteString("\n")
	}
	if len(inactive) > 0 {
		builder.WriteString(fmt.Sprintf("未运行（%d）\n", len(inactive)))
		for _, row := range inactive {
			builder.WriteString(chatScheduleRowLine(row, false))
		}
	}
	return buildChatPlainTextCommandDocument(builder.String())
}

func chatScheduleRowLine(row scheduleRow, live bool) string {
	var builder strings.Builder
	builder.WriteString("  ")
	if live {
		builder.WriteString("[开] ")
	} else {
		builder.WriteString("[停] ")
	}
	builder.WriteString(row.title)
	builder.WriteString("  (")
	builder.WriteString(row.id)
	builder.WriteString(")\n      ")
	builder.WriteString(scheduleKindDisplay(row))
	if row.frequency != "" {
		builder.WriteString(" · ")
		builder.WriteString(row.frequency)
	}
	if row.zone != "" {
		builder.WriteString(" · ")
		builder.WriteString(row.zone)
	}
	if live {
		builder.WriteString("  下次: ")
		builder.WriteString(row.target.Local().Format("2006-01-02 15:04:05"))
	}
	builder.WriteString("  会话: ")
	builder.WriteString(row.sessionID)
	builder.WriteString("\n")
	return builder.String()
}

func scheduleKindDisplay(row scheduleRow) string {
	switch row.kind {
	case "after":
		return "一次性（延迟）"
	case "at":
		return "一次性（绝对时间）"
	case "every":
		return "固定间隔"
	case "daily":
		return "每天"
	case "weekly":
		return "每周"
	case "cron":
		return "Cron"
	default:
		return row.kind
	}
}
