import { describe, expect, it } from "vitest";

import { type AgentChatStreamChunkPayload } from "@/types/runtime";

import {
  countContentLines,
  countDiffLines,
  extractToolDetails,
  matchToolFilePath,
  parseArgPreviewText,
  parseToolDetailsFromArgsText,
  resolveToolSegmentDetails,
} from "./details";

function payload(partial: Partial<AgentChatStreamChunkPayload>): AgentChatStreamChunkPayload {
  return partial as AgentChatStreamChunkPayload;
}

describe("countDiffLines / countContentLines", () => {
  it("忽略 +++/--- 文件头，只统计正负行", () => {
    const patch = [
      "*** Begin Patch",
      "*** Update File: src/a.ts",
      "@@",
      "-old",
      "+new",
      "+extra",
      "*** End Patch",
    ].join("\n");
    expect(countDiffLines(patch)).toEqual({ additions: 2, removals: 1 });
  });

  it("内容行数不计尾部换行造成的空行", () => {
    expect(countContentLines("a\nb\n")).toBe(2);
    expect(countContentLines("")).toBe(0);
    expect(countContentLines("\n")).toBe(0);
  });
});

describe("extractToolDetails", () => {
  it("从工具入参提取文件路径 / 命令 / 查询 / URL / 退出码", () => {
    expect(
      extractToolDetails(payload({ tool: { args: { file_path: "src/a.ts" } } }), "read_file"),
    ).toEqual({ filePath: "src/a.ts" });

    expect(
      extractToolDetails(payload({ tool: { args: { command: "npm test" } } }), "shell"),
    ).toEqual({ command: "npm test" });

    expect(
      extractToolDetails(payload({ tool: { args: { pattern: "useEffect" } } }), "grep"),
    ).toEqual({ query: "useEffect" });

    expect(
      extractToolDetails(payload({ tool: { args: { url: "https://example.com/a" } } }), "web_fetch"),
    ).toEqual({ url: "https://example.com/a" });

    expect(
      extractToolDetails(payload({ tool: { exit_code: 0 } }), "shell"),
    ).toEqual({ exitCode: 0 });
  });

  it("apply_patch 从完整 patch 统计行数并取目标文件", () => {
    const patch = [
      "*** Begin Patch",
      "*** Update File: src/feature.ts",
      "@@",
      "-const a = 1;",
      "+const a = 2;",
      "+const b = 3;",
      "*** End Patch",
    ].join("\n");

    expect(
      extractToolDetails(payload({ tool: { args: { patch } } }), "apply_patch"),
    ).toEqual({
      filePath: "src/feature.ts",
      diff: { additions: 2, removals: 1 },
    });
  });

  it("优先使用事件白名单里的行级字段", () => {
    expect(
      extractToolDetails(
        payload({ tool: { args: { file_path: "a.ts" }, additions: 7, removals: 3 } }),
        "edit",
      ),
    ).toEqual({ filePath: "a.ts", diff: { additions: 7, removals: 3 } });
  });

  it("无结构化字段时返回 undefined，不补默认值", () => {
    expect(extractToolDetails(payload({ tool: { args: {} } }), "unknown_tool")).toBeUndefined();
  });
});

// 回归（2026-09-16 页面 bug）：SSE live 摘要行只剩工具名（`ls` / `grep`），参数丢失。
// 根因：实时帧不带结构化 arguments，只有后端 `summarizeToolCallArgs` 渲染的
// `arg_preview` 键值文本（`command=ls -la` / `pattern=xxx path=src`），而提取层只认
// JSON，于是 command / query 全空，摘要退化成 0 个 part。
describe("实时帧入参预览（arg_preview 键值文本）", () => {
  it("按后端格式解析 key=value，值切到下一个键边界", () => {
    expect(parseArgPreviewText("command=Get-ChildItem -Force")).toEqual({
      command: "Get-ChildItem -Force",
    });
    expect(
      parseArgPreviewText('patterns=["Popover","DialogTrigger"] paths=["src"] glob=*.tsx context=2'),
    ).toEqual({
      patterns: '["Popover","DialogTrigger"]',
      paths: '["src"]',
      glob: "*.tsx",
      context: "2",
    });
    // 非预览文本（历史裸路径、JSON）不产生键值，交给原有分支。
    expect(parseArgPreviewText("src/index.ts")).toEqual({});
    expect(parseArgPreviewText('{"file_path":"src/a.ts"}')).toEqual({});
  });

  it("shell 实时帧：command_text 原样优先，缺失时回退预览里的 command", () => {
    expect(
      extractToolDetails(
        payload({
          tool: { name: "shell", args: "command=go test ./...", command_text: "go test ./..." },
        }),
        "shell",
      ),
    ).toEqual({ command: "go test ./..." });

    expect(
      extractToolDetails(payload({ tool: { args: "command=ls -la" } }), "shell"),
    ).toEqual({ command: "ls -la" });
  });

  it("grep / glob 实时帧：预览里的 pattern 变成摘要查询", () => {
    expect(
      extractToolDetails(
        payload({ tool: { args: "pattern=useEffect path=src glob=*.tsx" } }),
        "grep",
      ),
    ).toEqual({ query: "useEffect" });
    expect(
      extractToolDetails(payload({ tool: { args: "pattern=**/*.tsx path=apps limit=50" } }), "glob"),
    ).toEqual({ query: "**/*.tsx" });
  });

  it("列表值按后端新口径渲染（`a | b`，无 JSON 标点）", () => {
    expect(
      extractToolDetails(
        payload({
          tool: {
            args: "patterns=Popover | DialogTrigger paths=apps/portal-modern/src glob=*.tsx",
          },
        }),
        "grep",
      ),
    ).toEqual({ query: "Popover | DialogTrigger" });
    // 旧口径（JSON 数组文本）仍要认得：历史帧与截断帧都可能是这个形状。
    expect(
      parseToolDetailsFromArgsText('patterns=["Popover","DialogTrigger"] paths=["src"]', "grep"),
    ).toEqual({ query: "Popover | DialogTrigger" });
  });

  it("view 实时帧：普通路径走预览，长路径走 display_file_path", () => {
    expect(
      extractToolDetails(
        payload({ tool: { args: "file_path=main.go limit=20 offset=40" } }),
        "view",
      ),
    ).toEqual({ filePath: "main.go" });

    expect(
      extractToolDetails(
        payload({
          tool: { args: "limit=20" },
          display_file_path: "src/very long path/组件.tsx",
        }),
        "view",
      ),
    ).toEqual({ filePath: "src/very long path/组件.tsx" });
  });

  it("argsSummary 兜底同样认识预览文本（降级 / 已落盘数据）", () => {
    expect(parseToolDetailsFromArgsText("command=npm test", "shell")).toEqual({
      command: "npm test",
    });
    expect(parseToolDetailsFromArgsText("pattern=TODO", "grep")).toEqual({ query: "TODO" });
    // 预览里没有可用键时不产生明细，也不误判成裸路径。
    expect(parseToolDetailsFromArgsText("case_insensitive=false", "grep")).toBeUndefined();
  });
});

// 回归（2026-09-16）：shell 的真实入参是批量 `commands` 列表（toolargs 的 shell 参数
// 表），只认 `command` 时折叠行没有 command → 摘要只剩工具名。
describe("shell 批量命令（commands 列表）", () => {
  it("结构化入参：对象数组取 command 字段并连成单行", () => {
    expect(
      extractToolDetails(
        payload({
          tool: {
            args: {
              commands: [
                { command: "go test ./...", workdir: "E:/repo" },
                { cmd: "git status --short" },
                { workdir: "E:/repo" },
              ],
            },
          },
        }),
        "shell",
      ),
    ).toEqual({ command: "go test ./... ; git status --short" });
  });

  it("结构化入参：字符串数组同样支持", () => {
    expect(
      extractToolDetails(payload({ tool: { args: { commands: ["ls -la", "pwd"] } } }), "shell"),
    ).toEqual({ command: "ls -la ; pwd" });
  });

  it("实时帧：command_text（后端归一后的批量命令）优先于预览文本", () => {
    expect(
      extractToolDetails(
        payload({
          tool: {
            args: "command=go test ./... ; git status --short",
            command_text: "go test ./... ; git status --short",
          },
        }),
        "shell",
      ),
    ).toEqual({ command: "go test ./... ; git status --short" });
  });

  it("历史帧兜底：从被截断的 commands 预览 JSON 里宽松还原命令", () => {
    expect(
      parseToolDetailsFromArgsText(
        'commands=[{"command":"pnpm exec vitest run src/lib/thread-state","workdir":"E:\\\\projects"}]',
        "shell",
      ),
    ).toEqual({ command: "pnpm exec vitest run src/lib/thread-state" });
    // 只认得结构符号时宁可没有摘要，也不把半截 JSON 当命令展示。
    expect(
      parseToolDetailsFromArgsText('commands=[{"workdir":"E:\\\\projects"}]', "shell"),
    ).toBeUndefined();
  });
});

// 回归（2026-09-16）：grep 的 `patterns` 是列表形态，后端预览把它渲染成 JSON 数组
// 文本。只做单字符串读取时，实时帧摘要会显示 `["a","b"]`，结构化入参则直接没有摘要。
describe("grep 列表形态入参（patterns）", () => {
  it("实时帧预览：JSON 数组还原成 OR 语义的单行查询", () => {
    expect(
      extractToolDetails(
        payload({
          tool: { name: "grep", args: 'patterns=["Popover","DialogTrigger"] paths=["src"] glob=*.tsx' },
        }),
        "grep",
      ),
    ).toEqual({ query: "Popover | DialogTrigger" });
  });

  it("结构化入参：字符串数组同样还原", () => {
    expect(
      extractToolDetails(
        payload({ tool: { args: { patterns: ["a", "b"], paths: ["src"] } } }),
        "grep",
      ),
    ).toEqual({ query: "a | b" });
  });

  it("历史 JSON 入参文本：数组 patterns 不再退化成无摘要", () => {
    expect(parseToolDetailsFromArgsText('{"patterns":["a","b"]}', "grep")).toEqual({
      query: "a | b",
    });
  });

  it("被截断的 JSON 数组只取完整项，不展示 JSON 标点", () => {
    expect(parseToolDetailsFromArgsText('patterns=["Popover","DialogTrig', "grep")).toEqual({
      query: "Popover",
    });
  });

  it("单值 pattern 与 glob 不受影响", () => {
    expect(parseToolDetailsFromArgsText("pattern=**/*.tsx path=apps", "glob")).toEqual({
      query: "**/*.tsx",
    });
    expect(parseToolDetailsFromArgsText("pattern=TODO", "grep")).toEqual({ query: "TODO" });
  });
});

describe("parseToolDetailsFromArgsText（历史 / 演示数据兜底）", () => {
  it("JSON 入参解析出文件路径", () => {
    expect(
      parseToolDetailsFromArgsText('{"file_path":"src/b.ts","offset":10}', "read_file"),
    ).toEqual({ filePath: "src/b.ts" });
  });

  it("截断的 JSON 不产生错误的 diff 统计", () => {
    const truncated =
      '{"patch":"*** Begin Patch\\n*** Update File: a.ts\\n+1\\n+2\\n+3\\n+4\\n+5\\n+6\\n+7\\n+8\\n+9\\n+10\\n+11\\n+12...';
    const details = parseToolDetailsFromArgsText(truncated, "apply_patch");
    expect(details?.diff).toBeUndefined();
    expect(details?.filePath).toBe("a.ts");
  });

  it("裸路径字符串按读类工具识别，普通字符串不误判", () => {
    expect(parseToolDetailsFromArgsText("src/index.ts", "read_file")).toEqual({
      filePath: "src/index.ts",
    });
    expect(parseToolDetailsFromArgsText("42 行", "read_file")).toBeUndefined();
    expect(parseToolDetailsFromArgsText("npm run build", "shell")).toBeUndefined();
  });
});

// 回归（2026-09-16 页面 bug，回放会话 session_20260916171138_Hp8OaRmI）：view 的批量
// 形态入参是 `files: [{file_path, limit, offset}, …]`，顶层没有单文件键；只认顶层键
// 时整行 24px 折叠摘要空白（实测 14 行 view 里 9 行 `hasSummary=false`）。
describe("批量文件入参（files 列表）", () => {
  it("历史回放入参：取第一个条目的路径", () => {
    expect(
      parseToolDetailsFromArgsText(
        '{"files":[{"file_path":"backend/cmd/aicli/ui/screen.go","limit":120},' +
          '{"file_path":"backend/cmd/aicli/ui/app_screen_layout.go","limit":140}]}',
        "view",
      ),
    ).toEqual({ filePath: "backend/cmd/aicli/ui/screen.go" });
  });

  it("实时帧证据尾巴：结构化 arguments 走同一口径", () => {
    expect(
      extractToolDetails(
        payload({
          tool: {
            args: {
              files: [
                { file_path: "backend/cmd/aicli/ui/app_screen_layout.go", limit: 120, offset: 100 },
                { file_path: "backend/cmd/aicli/ui/bottom_pane_row_plan.go" },
              ],
            },
          },
        }),
        "view",
      ),
    ).toEqual({ filePath: "backend/cmd/aicli/ui/app_screen_layout.go" });
  });

  it("条目缺路径时不伪造：宁可没有摘要", () => {
    expect(parseToolDetailsFromArgsText('{"files":[{"limit":120}]}', "view")).toBeUndefined();
    expect(parseToolDetailsFromArgsText('{"files":[]}', "view")).toBeUndefined();
  });

  it("顶层单文件键优先于列表", () => {
    expect(
      parseToolDetailsFromArgsText(
        '{"file_path":"src/a.ts","files":[{"file_path":"src/b.ts"}]}',
        "view",
      ),
    ).toEqual({ filePath: "src/a.ts" });
  });

  it("目录列举（ls）：目标目录写入 directoryPath，不冒充文件路径", () => {
    // 回放：历史入参是结构化 JSON。
    expect(parseToolDetailsFromArgsText('{"path":"frontend/e2e","depth":3}', "ls")).toEqual({
      directoryPath: "frontend/e2e",
    });
    // 实时：SSE 帧只有 `arg_preview` 键值文本。
    expect(
      extractToolDetails(payload({ tool: { args: "path=frontend/src depth=2" } }), "ls"),
    ).toEqual({ directoryPath: "frontend/src" });
    // 结构化 arguments 的实时帧走同一口径。
    expect(extractToolDetails(payload({ tool: { args: { path: "backend" } } }), "ls")).toEqual({
      directoryPath: "backend",
    });
    // 目录不是文件：不得写入 filePath（折叠行不能给出「打开文件」死链接）。
    expect(
      extractToolDetails(payload({ tool: { args: { path: "backend" } } }), "ls")?.filePath,
    ).toBeUndefined();
    // 事件级文件字段（后端长路径下发的 display_file_path）对目录列举类同样必须忽略。
    expect(
      extractToolDetails(
        payload({
          tool: { args: "path=backend/internal/supervision depth=2" },
          display_file_path: "backend/internal/supervision",
        }),
        "ls",
      ),
    ).toEqual({ directoryPath: "backend/internal/supervision" });
  });
});

describe("resolveToolSegmentDetails", () => {
  it("已有结构化明细时优先使用，不回退解析", () => {
    expect(
      resolveToolSegmentDetails({
        name: "read_file",
        argsSummary: "other.ts",
        details: { filePath: "src/real.ts" },
      }),
    ).toEqual({ filePath: "src/real.ts" });
  });

  it("无明细时解析 argsSummary", () => {
    expect(resolveToolSegmentDetails({ name: "read_file", argsSummary: "src/x.ts" })).toEqual({
      filePath: "src/x.ts",
    });
    expect(resolveToolSegmentDetails({ name: "read_file" })).toBeUndefined();
  });
});

describe("matchToolFilePath", () => {
  it("分隔符归一后支持全等与相对/绝对后缀匹配", () => {
    expect(matchToolFilePath("src/a.ts", "src/a.ts")).toBe(true);
    expect(matchToolFilePath("E:\\repo\\src\\a.ts", "src/a.ts")).toBe(true);
    expect(matchToolFilePath("src/a.ts", "E:/repo/src/a.ts")).toBe(true);
    expect(matchToolFilePath("src/a.ts", "src/b.ts")).toBe(false);
    expect(matchToolFilePath("", "src/a.ts")).toBe(false);
  });
});

describe("extractToolDetails：行级 diff 文本", () => {
  /** 后端 tool.completed 的真实形态：说明行 + ```diff 围栏（带行号 hunk）。 */
  const DIFF_FENCE = [
    "补丁已应用：修改 1；影响 1 个路径",
    "",
    "文件差异:",
    "```diff",
    "--- a/src/a.ts",
    "+++ b/src/a.ts",
    "@@ -1,2 +1,2 @@",
    "-old",
    "+new",
    "```",
  ].join("\n");
  const CODEX_PATCH = "*** Begin Patch\n*** Update File: src/a.ts\n@@\n-old\n+new\n*** End Patch";

  it("apply_patch：从 render_output 围栏里保留补丁文本，并按补丁行统计兜底", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE, args: { patch: CODEX_PATCH } } }),
      "apply_patch",
    );

    expect(details?.filePath).toBe("src/a.ts");
    expect(details?.diffText).toBe("--- a/src/a.ts\n+++ b/src/a.ts\n@@ -1,2 +1,2 @@\n-old\n+new");
    expect(details?.diff).toEqual({ additions: 1, removals: 1 });
    expect(details?.diffTextTruncated).toBeUndefined();
  });

  it("事件里已有的真实统计优先于解析结果", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE, additions: 7, removals: 9 } }),
      "apply_patch",
    );

    expect(details?.diff).toEqual({ additions: 7, removals: 9 });
    expect(details?.diffText).toBeTruthy();
  });

  it("Codex 裸 @@ 补丁没有行号 → 不保留 diffText（回落原始文本）", () => {
    const details = extractToolDetails(
      payload({ tool: { args: { patch: CODEX_PATCH } } }),
      "apply_patch",
    );

    expect(details?.diffText).toBeUndefined();
    expect(details?.diff).toEqual({ additions: 1, removals: 1 });
  });

  it("非 diff 类工具即使输出里有补丁围栏也不保留行级文本", () => {
    const details = extractToolDetails(
      payload({ tool: { render_output: DIFF_FENCE } }),
      "read_file",
    );

    expect(details?.diffText).toBeUndefined();
  });

  it("resolveToolSegmentDetails：没有入参文本时从结果围栏恢复行级文本", () => {
    const details = resolveToolSegmentDetails({
      name: "apply_patch",
      resultSummary: DIFF_FENCE,
    });

    expect(details?.diffText).toContain("@@ -1,2 +1,2 @@");
  });
});
