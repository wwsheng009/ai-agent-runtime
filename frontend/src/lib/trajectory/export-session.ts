/**
 * 会话轨迹导出（P2-7 从轨迹视图抽出共享）：EventStore 分页拉全量 chat.sse.* 事件
 * → JSONL 文本 → 浏览器下载。
 *
 * 单一事实源：`/trajectory` 视图的导出按钮与 composer `/export` 命令都走本函数，
 * 「按 after 游标分页、页不满即结束」的恢复语义只在 `lib/trajectory/recovery`
 * 定义一次，避免两处各写一份分页循环后口径漂移。
 */
import { fetchSessionRuntimeEvents } from "@/api/runtime/sessions";
import {
  buildTrajectoryExportFilename,
  downloadTrajectoryJsonl,
  eventsToTrajectoryJsonl,
  redactExportPayload,
} from "@/lib/trajectory/export";
import {
  fetchSessionHistoryMessages,
  hasTrajectoryContentFrames,
} from "@/lib/trajectory/history-fallback";
import {
  nextRecoveryAfter,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
} from "@/lib/trajectory/recovery";
import { sessionHistoryToTrajectoryPushes } from "@/lib/trajectory/session-history";

export type SessionTrajectoryExportResult = {
  /** 实际写入 JSONL 的原始事件条数（未过滤前的拉取总量）。 */
  eventCount: number;
  /** 历史兜底行数（无内容帧的会话把历史消息投影成导出行的条数）。 */
  historyRowCount: number;
  filename: string;
  redacted: boolean;
};

export type SessionTrajectoryExportOptions = {
  /** 脱敏导出：payload 中的敏感字段按 `lib/trajectory/export` 既有规则替换。 */
  redact?: boolean;
};

export async function exportSessionTrajectoryJsonl(
  sessionId: string,
  options: SessionTrajectoryExportOptions = {},
): Promise<SessionTrajectoryExportResult> {
  const redact = options.redact ?? false;
  const events: Awaited<
    ReturnType<typeof fetchSessionRuntimeEvents>
  >["events"] = [];
  let after = 0;
  for (;;) {
    const page = await fetchSessionRuntimeEvents(sessionId, {
      after,
      limit: TRAJECTORY_RECOVERY_PAGE_SIZE,
    });
    events.push(...page.events);
    if (
      page.events.length === 0 ||
      page.events.length < TRAJECTORY_RECOVERY_PAGE_SIZE
    ) {
      break;
    }
    after = nextRecoveryAfter(page.events, after);
  }

  const lines: string[] = [];
  const eventsJsonl = eventsToTrajectoryJsonl(events, { redact });
  if (eventsJsonl) {
    lines.push(eventsJsonl);
  }

  // 与轨迹恢复同一口径：没有任何内容帧的会话（消息只落在持久化会话历史里）
  // 若只导出 EventStore 事件，导出文件会只有生命周期行——因此同样回退到
  // 「会话历史 → 导出行」投影，保证导出的就是轨迹视图里看到的内容。
  let historyRowCount = 0;
  if (!hasTrajectoryContentFrames(events)) {
    const history = await fetchSessionHistoryMessages(sessionId);
    const ts = new Date().toISOString();
    for (const push of sessionHistoryToTrajectoryPushes(history)) {
      const payload = redact
        ? redactExportPayload({ ...push.payload })
        : push.payload;
      lines.push(JSON.stringify({ seq: 0, ts, kind: push.kind, payload }));
      historyRowCount += 1;
    }
  }

  const jsonl = lines.join("\n");
  const filename = buildTrajectoryExportFilename(sessionId, undefined, redact);
  downloadTrajectoryJsonl(jsonl, filename);
  return { eventCount: events.length, historyRowCount, filename, redacted: redact };
}
