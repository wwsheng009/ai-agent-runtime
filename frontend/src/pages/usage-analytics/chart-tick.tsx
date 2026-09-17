// 批次 7.2：共享的 Recharts 坐标轴刻度组件。
// 用于截断过长的标签（如 provider/model 名称、错误码+分类组合），
// 鼠标悬浮时通过 title 属性显示完整名称。

import type { CSSProperties } from "react";

const MAX_LABEL_LENGTH = 24;

type TruncatedTickProps = {
  x: number | string;
  y: number | string;
  payload?: { value: string };
  textAnchor?: "inherit" | "end" | "start" | "middle";
  fill?: string;
  fontSize?: number;
  style?: CSSProperties;
};

export function TruncatedTick({
  x,
  y,
  payload,
  textAnchor = "end",
}: TruncatedTickProps) {
  const value = payload?.value ?? "";
  const truncated =
    value.length > MAX_LABEL_LENGTH
      ? `${value.slice(0, MAX_LABEL_LENGTH - 1)}…`
      : value;

  return (
    <text
      x={x}
      y={y}
      dx={-12}
      dy={4}
      fill="var(--analytics-chart-axis)"
      fontSize={12}
      textAnchor={textAnchor}
      style={{
        maxWidth: "200px",
        overflow: "hidden",
        textOverflow: "ellipsis",
        whiteSpace: "nowrap",
      }}
    >
      {truncated}
      <title>{value}</title>
    </text>
  );
}
