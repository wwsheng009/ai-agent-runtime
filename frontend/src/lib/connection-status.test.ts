import { describe, expect, it } from "vitest";

import {
  canManualRetryConnection,
  connectionStatusFromLogsState,
  getConnectionStatusTone,
  withTransportDegradation,
  type ConnectionStatus,
  type ConnectionStatusLabels,
} from "./connection-status";

const labels: ConnectionStatusLabels = {
  connecting: "连接中…",
  idle: "空闲",
  offline: "连接中断",
  online: "在线",
  reconnecting: "重连中…",
};

describe("connection-status", () => {
  it("maps the logs stream state machine onto the unified status", () => {
    expect(connectionStatusFromLogsState("open")).toBe("online");
    expect(connectionStatusFromLogsState("connecting")).toBe("connecting");
    expect(connectionStatusFromLogsState("reconnecting")).toBe("reconnecting");
    expect(connectionStatusFromLogsState("error")).toBe("offline");
    expect(connectionStatusFromLogsState("idle")).toBe("idle");
  });

  it("keeps manual retry available for every non-online status", () => {
    expect(canManualRetryConnection("online")).toBe(false);

    const retryable: ConnectionStatus[] = [
      "idle",
      "connecting",
      "reconnecting",
      "offline",
    ];
    for (const status of retryable) {
      expect(canManualRetryConnection(status)).toBe(true);
    }
  });

  it("surfaces the direct chat stream failure as an offline connection", () => {
    // 直连 /api/agent/chat 失败会写 transport=error，此时会话流可能仍在线，
    // 呈现层必须收敛为 offline（可见 + 可手动重试）。
    expect(withTransportDegradation("online", "error")).toBe("offline");
    expect(withTransportDegradation("connecting", "error")).toBe("offline");
    expect(withTransportDegradation("offline", "live")).toBe("offline");
    expect(withTransportDegradation("online", "live")).toBe("online");
    expect(withTransportDegradation("reconnecting", undefined)).toBe(
      "reconnecting",
    );
  });

  it("resolves tone and label from the shared label set", () => {
    const offline = getConnectionStatusTone("offline", labels);
    expect(offline.label).toBe("连接中断");
    expect(offline.icon).toBe("offline");
    expect(offline.badgeClassName).toContain("red");

    const reconnecting = getConnectionStatusTone("reconnecting", labels);
    expect(reconnecting.label).toBe("重连中…");
    expect(reconnecting.icon).toBe("connecting");

    expect(getConnectionStatusTone("online", labels).label).toBe("在线");
  });
});
