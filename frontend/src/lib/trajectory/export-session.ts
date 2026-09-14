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
} from "@/lib/trajectory/export";
import {
  nextRecoveryAfter,
  TRAJECTORY_RECOVERY_PAGE_SIZE,
} from "@/lib/trajectory/recovery";

export type SessionTrajectoryExportResult = {
  /** 实际写入 JSONL 的原始事件条数（未过滤前的拉取总量）。 */
  eventCount: number;
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
  const jsonl = eventsToTrajectoryJsonl(events, { redact });
  const filename = buildTrajectoryExportFilename(sessionId, undefined, redact);
  downloadTrajectoryJsonl(jsonl, filename);
  return { eventCount: events.length, filename, redacted: redact };
}
