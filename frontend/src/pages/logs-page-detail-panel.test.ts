import { describe, expect, it } from "vitest";

import type { LogsPageDetailLabels } from "@/pages/logs-page-detail-panel.i18n";
import { buildRuntimeInsightRows } from "@/pages/logs-page-detail-panel";
import type { RuntimeLogEntry } from "@/types/runtime";

const insightLabels = {
  cacheHit: "Cache hit",
  cacheHitValueHit: "Hit",
  cacheHitValueMiss: "Miss",
  skillExposureMode: "Skill exposure mode",
  finalFunctionCount: "Final function count",
  routedSkillCount: "Routed skill count",
  candidateCount: "Candidate count",
  exposedFunctionCount: "Exposed function count",
} as LogsPageDetailLabels;

describe("logs page detail panel helpers", () => {
  it("extracts cache hit and skill exposure counters", () => {
    const entry = {
      cursor: 11,
      raw_text: "{}",
      fields: {
        cache_hit: true,
        skill_exposure: {
          mode: "prefer",
          final_function_count: 3,
          routed_skill_count: 2,
          candidate_count: 5,
        },
        exposed_function_count: 4,
      },
    } satisfies RuntimeLogEntry;

    expect(buildRuntimeInsightRows(entry, insightLabels)).toEqual([
      {
        key: "cache_hit",
        label: "Cache hit",
        value: "Hit",
        valueClassName:
          "inline-flex items-center rounded-full border px-2 py-0.5 font-mono app-text-10 uppercase tracking-[0.14em] border-emerald-500/25 bg-emerald-500/10 text-emerald-100",
      },
      {
        key: "skill_exposure_mode",
        label: "Skill exposure mode",
        value: "prefer",
      },
      {
        key: "skill_exposure_final_function_count",
        label: "Final function count",
        value: "3",
      },
      {
        key: "skill_exposure_routed_skill_count",
        label: "Routed skill count",
        value: "2",
      },
      {
        key: "skill_exposure_candidate_count",
        label: "Candidate count",
        value: "5",
      },
      {
        key: "exposed_function_count",
        label: "Exposed function count",
        value: "4",
      },
    ]);
  });
});
