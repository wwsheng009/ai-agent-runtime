// 修复 remark-breaks 与 `whitespace-pre-wrap` 叠加产生的「双换行」（每个换行多渲染一行）。
//
// mdast-util-to-hast 的 hardBreak 处理器在输出 `<br>` 的同时，还会追加一个纯换行文本节点
// （mdast-util-to-hast/lib/handlers/break.js → [<br>, { type: "text", value: "\n" }]）。
// 普通 `white-space: normal` 下该换行只是可折叠空白；但正文段落用 `whitespace-pre-wrap`
// 保留缩进，这个换行会被再次渲染为强制换行，于是每个换行都被渲染成两行——
// Tool receipt 的 JSON 正文每行都多出一个空行即由此而来。
//
// 这里在 hast 层去掉紧跟在 `<br>` 之后的换行文本：换行仍由 `<br>` 承担，
// `pre-wrap` 的缩进保留能力不受影响；表格/列表等非 pre-wrap 上下文也没有布局变化
// （`white-space: normal` 下紧贴强制换行的前导空白本来就会被去掉）。

type HastNode = {
  type: string;
  value?: string;
  tagName?: string;
  children?: HastNode[];
};

function isBreakElement(node: HastNode | undefined) {
  return node?.type === "element" && node.tagName === "br";
}

function stripBreakNewlines(node: HastNode) {
  const children = node.children;
  if (!children || children.length === 0) {
    return;
  }

  for (const child of children) {
    stripBreakNewlines(child);
  }

  for (let index = children.length - 1; index >= 1; index -= 1) {
    const child = children[index];
    if (
      child.type !== "text" ||
      typeof child.value !== "string" ||
      !child.value.startsWith("\n") ||
      !isBreakElement(children[index - 1])
    ) {
      continue;
    }

    child.value = child.value.slice(1);
    if (child.value.length === 0) {
      children.splice(index, 1);
    }
  }
}

export function rehypeCollapseBreakNewlines() {
  return (tree: unknown) => {
    stripBreakNewlines(tree as HastNode);
  };
}
