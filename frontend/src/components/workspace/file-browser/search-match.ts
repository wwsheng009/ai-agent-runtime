// 搜索结果高亮切分（P1-9，规划 §4.7.2-A）：**纯函数**模块——`react-refresh/only-export-components`
// 不允许组件文件再导出工具函数，因此与 `search-results.tsx` 分开存放（同 `entry-sort.ts` 的纪律）。
//
// 归一化纪律：`match.start/end` 是**显示字符串的 rune（码点）偏移**（见 `FsSearchMatch` 注释），
//   直接用 `String.prototype.slice`（UTF-16 下标）会把代理对（emoji 等）切坏，所以先 `Array.from`；
//   越界一律夹紧到文本范围内；空命中（`end ≤ start`）不产生高亮，不猜位置。
import type { FsSearchMatch } from "@/types/runtime/fs-browser";

/** 按 rune 偏移切分高亮：[命中前, 命中, 命中后]；`match` 缺失或空命中时命中段为空串。 */
export function splitRuneMatch(text: string, match?: FsSearchMatch): [string, string, string] {
  if (!match) {
    return [text, "", ""];
  }
  const runes = Array.from(text);
  const start = Math.max(0, Math.min(Math.floor(match.start), runes.length));
  const end = Math.max(start, Math.min(Math.floor(match.end), runes.length));
  if (end === start) {
    return [text, "", ""];
  }
  return [runes.slice(0, start).join(""), runes.slice(start, end).join(""), runes.slice(end).join("")];
}
