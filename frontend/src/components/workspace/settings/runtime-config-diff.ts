export type ConfigDiffLine = {
  type: "context" | "add" | "remove";
  value: string;
  beforeLine?: number;
  afterLine?: number;
};

function splitLines(value: string) {
  return value.replace(/\r\n/g, "\n").split("\n");
}

export function buildConfigLineDiff(
  beforeContent: string,
  afterContent: string,
): ConfigDiffLine[] {
  const before = splitLines(beforeContent);
  const after = splitLines(afterContent);
  const rows = before.length + 1;
  const cols = after.length + 1;
  const dp: number[][] = Array.from({ length: rows }, () =>
    Array.from({ length: cols }, () => 0),
  );

  for (let i = before.length - 1; i >= 0; i -= 1) {
    for (let j = after.length - 1; j >= 0; j -= 1) {
      if (before[i] === after[j]) {
        dp[i][j] = dp[i + 1][j + 1] + 1;
      } else {
        dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
  }

  const diff: ConfigDiffLine[] = [];
  let i = 0;
  let j = 0;
  let beforeLine = 1;
  let afterLine = 1;

  while (i < before.length && j < after.length) {
    if (before[i] === after[j]) {
      diff.push({
        type: "context",
        value: before[i],
        beforeLine,
        afterLine,
      });
      i += 1;
      j += 1;
      beforeLine += 1;
      afterLine += 1;
      continue;
    }

    if (dp[i + 1][j] >= dp[i][j + 1]) {
      diff.push({
        type: "remove",
        value: before[i],
        beforeLine,
      });
      i += 1;
      beforeLine += 1;
      continue;
    }

    diff.push({
      type: "add",
      value: after[j],
      afterLine,
    });
    j += 1;
    afterLine += 1;
  }

  while (i < before.length) {
    diff.push({
      type: "remove",
      value: before[i],
      beforeLine,
    });
    i += 1;
    beforeLine += 1;
  }

  while (j < after.length) {
    diff.push({
      type: "add",
      value: after[j],
      afterLine,
    });
    j += 1;
    afterLine += 1;
  }

  return diff;
}
