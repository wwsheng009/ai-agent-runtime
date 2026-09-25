// Batch 12：`/profile` 候选组装与 ref 解析单测（纯函数，无 DOM / 无网络）。
//
// 覆盖口径：
// - 目录未就绪（null / undefined / profiles 非数组）→ 空集合，不补占位项；
// - 候选全集保留**不可解析**项（用于区分「不存在」与「存在但不可用」），
//   菜单 / 弹窗候选只收可解析项；
// - 顺序与原文：保持目录顺序，label 缺失回落 ref，layer 保持数据原文；
// - 去重口径：ref 去重（保留首条）；
// - ref 解析：精确优先、大小写不敏感唯一命中、歧义 / 空值不猜。

import { describe, expect, it } from "vitest";

import type { RuntimeProfileListResponse } from "@/types/runtime";

import {
  composerProfileCandidates,
  composerProfileCommandOptions,
  resolveComposerProfileRef,
} from "./composer-profile-options";

function entry(
  ref: string,
  overrides: Partial<RuntimeProfileListResponse["profiles"][number]> = {},
): RuntimeProfileListResponse["profiles"][number] {
  return {
    ref,
    name: "",
    description: "",
    layer: "user",
    path: `/profiles/${ref}`,
    valid: true,
    error: "",
    isDefault: false,
    defaultAgent: "",
    writable: true,
    promptSuppressed: false,
    promptSuppressionReason: "",
    isBound: false,
    ...overrides,
  };
}

function catalog(
  profiles: RuntimeProfileListResponse["profiles"],
  overrides: Partial<RuntimeProfileListResponse> = {},
): RuntimeProfileListResponse {
  return {
    count: profiles.length,
    defaultProfile: profiles[0]?.ref ?? "",
    defaultRoot: "/root",
    sessionSwitch: true,
    profiles,
    workspacePath: "",
    workspaceTrusted: true,
    workspaceTrustFeatureEnabled: false,
    projectBinding: null,
    ...overrides,
  };
}

describe("composerProfileCandidates", () => {
  it("目录未就绪时为空（不伪造 profile）", () => {
    expect(composerProfileCandidates(null)).toEqual([]);
    expect(composerProfileCandidates(undefined)).toEqual([]);
    expect(
      composerProfileCandidates({
        ...catalog([]),
        profiles: undefined as unknown as RuntimeProfileListResponse["profiles"],
      }),
    ).toEqual([]);
  });

  it("保持目录顺序与原文；label 缺失时回落 ref", () => {
    expect(
      composerProfileCandidates(
        catalog([
          entry("review", { name: "Review", layer: "user", isDefault: true }),
          entry("ops", { name: "", layer: "project" }),
        ]),
      ),
    ).toEqual([
      {
        ref: "review",
        label: "Review",
        layer: "user",
        isDefault: true,
        valid: true,
        invalidReason: "",
      },
      {
        ref: "ops",
        label: "ops",
        layer: "project",
        isDefault: false,
        valid: true,
        invalidReason: "",
      },
    ]);
  });

  it("不可解析项保留在候选全集并带原因（不静默丢弃）", () => {
    const candidates = composerProfileCandidates(
      catalog([
        entry("broken", { valid: false, error: "yaml: mapping values are not allowed" }),
      ]),
    );

    expect(candidates).toEqual([
      {
        ref: "broken",
        label: "broken",
        layer: "user",
        isDefault: false,
        valid: false,
        invalidReason: "yaml: mapping values are not allowed",
      },
    ]);
  });

  it("空 ref 与重复 ref 都不产出条目（去重保留首条）", () => {
    const candidates = composerProfileCandidates(
      catalog([
        entry("   ", { name: "blank" }),
        entry("review", { name: "First" }),
        entry("review", { name: "Second" }),
      ]),
    );

    expect(candidates.map((candidate) => candidate.ref)).toEqual(["review"]);
    expect(candidates[0]?.label).toBe("First");
  });
});

describe("composerProfileCommandOptions", () => {
  it("只收可解析项，value 是 ref、label 是可读名、description 是层名", () => {
    expect(
      composerProfileCommandOptions(
        catalog([
          entry("review", { name: "Review", layer: "user" }),
          entry("broken", { valid: false, error: "boom" }),
          entry("ops", { name: "Ops", layer: "project" }),
        ]),
      ),
    ).toEqual([
      { value: "review", label: "Review", description: "user" },
      { value: "ops", label: "Ops", description: "project" },
    ]);
  });

  it("层名为空时不产出 description（不写空串占位）", () => {
    expect(
      composerProfileCommandOptions(catalog([entry("review", { name: "Review", layer: "" })])),
    ).toEqual([{ value: "review", label: "Review" }]);
  });

  it("目录未就绪时为空", () => {
    expect(composerProfileCommandOptions(null)).toEqual([]);
  });
});

describe("resolveComposerProfileRef", () => {
  const candidates = composerProfileCandidates(
    catalog([entry("review"), entry("Review-Prod"), entry("other")]),
  );

  it("精确匹配优先（大小写不同也取精确那条）", () => {
    expect(resolveComposerProfileRef(candidates, "Review-Prod")?.ref).toBe(
      "Review-Prod",
    );
  });

  it("大小写不敏感且唯一命中时返回目录原文", () => {
    expect(resolveComposerProfileRef(candidates, "review-prod")?.ref).toBe(
      "Review-Prod",
    );
  });

  it("未命中 / 空值返回 null（不猜）", () => {
    expect(resolveComposerProfileRef(candidates, "nope")).toBeNull();
    expect(resolveComposerProfileRef(candidates, "   ")).toBeNull();
  });

  it("大小写不敏感命中多条时报歧义（返回 null）", () => {
    const ambiguous = composerProfileCandidates(
      catalog([entry("ops"), entry("OPS")]),
    );

    expect(resolveComposerProfileRef(ambiguous, "Ops")).toBeNull();
  });
});
