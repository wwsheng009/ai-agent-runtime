// 结构化键值行编辑器契约测试：行输入渲染 / 添加删除按钮 / 空键与重复键校验提示 /
// 多行粘贴解析与落盘（renderToStaticMarkup + 纯函数，不引入 testing-library）。

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    // 组件文案全部由调用方传入或来自 mcp.kv.*：这里直接回显 key，便于断言。
    t: (key: string) => key,
  }),
}));

import {
  applyKeyValuePaste,
  createKeyValueRow,
  parseKeyValuePaste,
  type KeyValueRow,
} from "./key-value-rows";
import { KeyValueEditor } from "./key-value-editor";

function renderEditor(rows: KeyValueRow[], disabled = false) {
  return renderToStaticMarkup(
    <KeyValueEditor
      addLabel="ADD_ROW"
      disabled={disabled}
      keyPlaceholder="KEY"
      rows={rows}
      separator="="
      valuePlaceholder="VALUE"
      onChange={() => {}}
    />,
  );
}

describe("KeyValueEditor 渲染", () => {
  it("每行渲染键/值输入、行号 aria-label 与分隔符", () => {
    const markup = renderEditor([
      createKeyValueRow("TOKEN", "abc"),
      createKeyValueRow("REGION", "cn"),
    ]);

    expect(markup).toContain('value="TOKEN"');
    expect(markup).toContain('value="abc"');
    expect(markup).toContain('value="REGION"');
    expect(markup).toContain('placeholder="KEY"');
    expect(markup).toContain('placeholder="VALUE"');
    expect(markup).toContain('aria-label="KEY 1"');
    expect(markup).toContain('aria-label="VALUE 2"');
  });

  it("渲染添加按钮与每行删除按钮（带行号标签）", () => {
    const markup = renderEditor([createKeyValueRow("A", "1")]);

    expect(markup).toContain("ADD_ROW");
    expect(markup).toContain('aria-label="mcp.kv.removeRow 1"');
    expect(markup).toContain('title="mcp.kv.removeRow 1"');
  });

  it("空键行与重复键行加 aria-invalid 并给出提示文案", () => {
    const markup = renderEditor([
      createKeyValueRow("", "value-only"),
      createKeyValueRow("DUP", "1"),
      createKeyValueRow("DUP", "2"),
    ]);

    expect(markup.match(/aria-invalid="true"/g)).toHaveLength(3);
    expect(markup).toContain("mcp.kv.keyRequired");
    expect(markup.match(/mcp\.kv\.duplicateKey/g)).toHaveLength(2);
    // 校验行同时通过 aria-describedby 关联到提示文本。
    expect(markup).toContain('aria-describedby="kv-error-');
  });

  it("合法行不加 aria-invalid，也不渲染校验提示", () => {
    const markup = renderEditor([
      createKeyValueRow("TOKEN", "abc"),
      createKeyValueRow("REGION", "cn"),
    ]);

    expect(markup).not.toContain("aria-invalid");
    expect(markup).not.toContain("mcp.kv.keyRequired");
    expect(markup).not.toContain("mcp.kv.duplicateKey");
  });

  it("disabled 时输入框、添加与删除按钮都不可用", () => {
    const markup = renderEditor([createKeyValueRow("A", "1")], true);

    expect(markup).toContain("disabled");
    expect(markup.match(/disabled=""/g)?.length ?? 0).toBeGreaterThanOrEqual(4);
  });
});

describe("多行粘贴解析", () => {
  it("env 写法：A=1 / B=2 按分隔符拆分并忽略空行", () => {
    expect(parseKeyValuePaste("A=1\n\n B = 2 \n", "=")).toEqual([
      { key: "A", value: "1" },
      { key: "B", value: "2" },
    ]);
  });

  it("headers 写法：Name: Value 支持冒号分隔", () => {
    expect(parseKeyValuePaste("Authorization: Bearer x\nX-Trace: 1", ":")).toEqual(
      [
        { key: "Authorization", value: "Bearer x" },
        { key: "X-Trace", value: "1" },
      ],
    );
  });

  it("值里含分隔符时只切首个：TOKEN=a=b", () => {
    expect(parseKeyValuePaste("TOKEN=a=b", "=")).toEqual([
      { key: "TOKEN", value: "a=b" },
    ]);
  });

  it("回退到另一种分隔符：env 编辑器也能粘贴 Name: Value", () => {
    expect(parseKeyValuePaste("X-Trace: 1", "=")).toEqual([
      { key: "X-Trace", value: "1" },
    ]);
    expect(parseKeyValuePaste("TOKEN=abc", ":")).toEqual([
      { key: "TOKEN", value: "abc" },
    ]);
  });

  it("无分隔符行整体作为键，值留空", () => {
    expect(parseKeyValuePaste("lonely-line", "=")).toEqual([
      { key: "lonely-line", value: "" },
    ]);
  });
});

describe("applyKeyValuePaste 落盘", () => {
  it("首行替换当前行（保留其 id），其余行追加为新行", () => {
    const first = createKeyValueRow("OLD", "old");
    const second = createKeyValueRow("KEEP", "keep");

    const next = applyKeyValuePaste(
      [first, second],
      first.id,
      "A=1\r\nB=2",
      "=",
    );

    expect(next).not.toBeNull();
    expect(next?.map((row) => [row.key, row.value])).toEqual([
      ["A", "1"],
      ["B", "2"],
      ["KEEP", "keep"],
    ]);
    expect(next?.[0]?.id).toBe(first.id);
    expect(next?.[1]?.id).not.toBe(first.id);
    expect(next?.[2]?.id).toBe(second.id);
  });

  it("单行文本不拦截（返回 null，保留浏览器默认粘贴行为）", () => {
    const row = createKeyValueRow("A", "");
    expect(applyKeyValuePaste([row], row.id, "B=2", "=")).toBeNull();
  });

  it("目标行不存在或粘贴内容为空行时返回 null", () => {
    const row = createKeyValueRow("A", "");
    expect(applyKeyValuePaste([row], "missing", "B=2\nC=3", "=")).toBeNull();
    expect(applyKeyValuePaste([row], row.id, "\n\n", "=")).toBeNull();
  });
});
