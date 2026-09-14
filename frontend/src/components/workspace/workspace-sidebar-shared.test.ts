import { describe, expect, it } from "vitest";

import type { RuntimeSessionRecord, RuntimeWorkspaceDirectory } from "@/types/runtime";

import {
  applySessionWorkspaceOverrides,
  pruneSessionMoveOverrides,
  resolveSessionMoveTarget,
} from "./workspace-sidebar-shared";

function session(
  id: string,
  workspacePath?: string,
  extraContext: Record<string, unknown> = {},
): RuntimeSessionRecord {
  return {
    id,
    metadata: {
      summary: `${id}-summary`,
      context:
        workspacePath === undefined
          ? { ...extraContext }
          : { workspace_path: workspacePath, ...extraContext },
    },
  };
}

function directory(
  id: string,
  path: string,
  overrides: Partial<RuntimeWorkspaceDirectory> = {},
): RuntimeWorkspaceDirectory {
  return {
    id,
    path,
    created_at: 0,
    exists: true,
    ...overrides,
  };
}

describe("workspace-sidebar-shared · 跨组移动纯口径", () => {
  describe("applySessionWorkspaceOverrides", () => {
    it("只改写目标会话的 workspace_path，保留 metadata/context 其它键", () => {
      const sessions = [session("s1", "E:\\old", { profile_ref: "p1" }), session("s2", "E:\\old")];

      const applied = applySessionWorkspaceOverrides(sessions, {
        s1: "E:/target",
      });

      expect(applied).toHaveLength(2);
      expect(applied[0]).not.toBe(sessions[0]);
      expect(applied[0].metadata?.context).toEqual({
        workspace_path: "E:/target",
        profile_ref: "p1",
      });
      expect(applied[0].metadata?.summary).toBe("s1-summary");
      // 未覆盖的会话保持同一引用。
      expect(applied[1]).toBe(sessions[1]);
    });

    it("无覆盖时返回入参同一引用", () => {
      const sessions = [session("s1", "E:\\old")];

      expect(applySessionWorkspaceOverrides(sessions, {})).toBe(sessions);
    });
  });

  describe("pruneSessionMoveOverrides", () => {
    it("Host 数据已反映目标路径（含路径规范化差异）时回收覆盖", () => {
      const overrides = { s1: "E:/target" };
      const sessions = [session("s1", "E:\\target\\")];

      expect(pruneSessionMoveOverrides(overrides, sessions)).toEqual({});
    });

    it("尚未反映的覆盖保留；会话消失时一并回收", () => {
      const overrides = { s1: "E:/target", gone: "E:/target" };
      const sessions = [session("s1", "E:\\old")];

      expect(pruneSessionMoveOverrides(overrides, sessions)).toEqual({
        s1: "E:/target",
      });
    });

    it("无需变更时返回入参同一引用", () => {
      const overrides = { s1: "E:/target" };
      const sessions = [session("s1", "E:\\old")];

      expect(pruneSessionMoveOverrides(overrides, sessions)).toBe(overrides);
      expect(pruneSessionMoveOverrides({}, sessions)).toEqual({});
    });
  });

  describe("resolveSessionMoveTarget", () => {
    it("注册目录优先用注册表原始路径与别名", () => {
      const target = resolveSessionMoveTarget(
        {
          key: "dir-1",
          label: "demo",
          fullPath: "E:/projects/demo",
          directoryId: "dir-1",
        },
        [directory("dir-1", "E:\\projects\\demo", { name: "演示目录" })],
      );

      expect(target).toEqual({
        key: "dir-1",
        label: "演示目录",
        path: "E:\\projects\\demo",
      });
    });

    it("注册目录在宿主机缺位时不可作落点（不改绑到打不开的目录）", () => {
      expect(
        resolveSessionMoveTarget(
          {
            key: "dir-1",
            label: "demo",
            fullPath: "E:/projects/demo",
            directoryId: "dir-1",
          },
          [directory("dir-1", "E:\\projects\\demo", { exists: false })],
        ),
      ).toBeNull();
    });

    it("未登记归属路径（Unscoped）不可作落点", () => {
      expect(
        resolveSessionMoveTarget(
          { key: "__unknown__", label: "Unscoped", fullPath: "" },
          [],
        ),
      ).toBeNull();
    });

    it("派生分组以自身路径为落点，注册表缺记录时回落到展示路径", () => {
      expect(
        resolveSessionMoveTarget(
          { key: "e:/derived", label: "derived", fullPath: "E:/derived" },
          [],
        ),
      ).toEqual({ key: "e:/derived", label: "derived", path: "E:/derived" });

      expect(
        resolveSessionMoveTarget(
          {
            key: "dir-9",
            label: "demo",
            fullPath: "E:/projects/demo",
            directoryId: "dir-9",
          },
          [],
        ),
      ).toEqual({
        key: "dir-9",
        label: "demo",
        path: "E:/projects/demo",
      });
    });
  });
});
