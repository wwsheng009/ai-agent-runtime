import { describe, expect, it } from "vitest";

import {
  NO_LOCALLY_FINALIZED_TURNS,
  finalizedTurnIdsFor,
  withLocallyFinalizedTurn,
  withSnapshotTurn,
} from "./locally-finalized-turns";

describe("locally finalized turn arbitration", () => {
  it("按会话记录已终态回合（幂等，无变化保持原引用）", () => {
    const once = withLocallyFinalizedTurn(null, "session-1", "turn-9");
    expect(finalizedTurnIdsFor(once, "session-1").has("turn-9")).toBe(true);
    expect(withLocallyFinalizedTurn(once, "session-1", "turn-9")).toBe(once);
    // 其它会话视为空集（会话切换不串台）。
    expect(finalizedTurnIdsFor(once, "session-2")).toBe(
      NO_LOCALLY_FINALIZED_TURNS,
    );
  });

  it("忽略空的会话 / 回合 id", () => {
    expect(withLocallyFinalizedTurn(null, "session-1", "")).toBeNull();
    expect(withLocallyFinalizedTurn(null, "", "turn-9")).toBeNull();
  });

  it("快照仍报告该回合时保持抑制（无变化不换引用）", () => {
    const state = withLocallyFinalizedTurn(null, "session-1", "turn-9");
    expect(withSnapshotTurn(state, "session-1", "turn-9")).toBe(state);
  });

  it("快照收敛（active_turn 变空或换回合）后解除抑制", () => {
    const state = withLocallyFinalizedTurn(null, "session-1", "turn-9");
    expect(withSnapshotTurn(state, "session-1", null)).toBeNull();
    expect(withSnapshotTurn(state, "session-1", "turn-10")).toBeNull();
  });

  it("清理过期条目、保留仍被快照报告的回合", () => {
    const state = withLocallyFinalizedTurn(
      withLocallyFinalizedTurn(null, "session-1", "turn-8"),
      "session-1",
      "turn-9",
    );

    const next = withSnapshotTurn(state, "session-1", "turn-9");

    expect([...finalizedTurnIdsFor(next, "session-1")]).toEqual(["turn-9"]);
  });

  it("会话切换时丢弃旧会话的抑制集", () => {
    const state = withLocallyFinalizedTurn(null, "session-1", "turn-9");
    expect(withSnapshotTurn(state, "session-2", "turn-1")).toBeNull();
    expect(finalizedTurnIdsFor(state, "session-2")).toBe(
      NO_LOCALLY_FINALIZED_TURNS,
    );
  });
});
