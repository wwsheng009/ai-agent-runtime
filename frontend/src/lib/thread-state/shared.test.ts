// 线程产物驻留上限测试：新→旧保留、替换不增长、批量插入同样有界。
import { describe, expect, it } from "vitest";

import { type Artifact } from "@/data/mock";

import { MAX_THREAD_ARTIFACTS, upsertArtifact, upsertArtifacts } from "./shared";

function makeArtifact(id: string): Artifact {
  return {
    id,
    name: `${id}.json`,
    path: `runtime/${id}.json`,
    summary: "",
    kind: "json",
    content: "{}",
  };
}

describe("upsertArtifact 上限", () => {
  it("持续追加时保留最新 MAX_THREAD_ARTIFACTS 个，最旧的被裁掉", () => {
    let artifacts: Artifact[] = [];
    const total = MAX_THREAD_ARTIFACTS + 5;
    for (let index = 0; index < total; index += 1) {
      artifacts = upsertArtifact(artifacts, makeArtifact(`a-${index}`));
    }
    expect(artifacts).toHaveLength(MAX_THREAD_ARTIFACTS);
    expect(artifacts[0]?.id).toBe(`a-${total - 1}`);
    expect(artifacts.at(-1)?.id).toBe(`a-${total - MAX_THREAD_ARTIFACTS}`);
  });

  it("同 id 替换既有产物不增长、不改顺序", () => {
    const artifact = makeArtifact("same");
    const list = upsertArtifact([makeArtifact("old"), artifact], {
      ...artifact,
      content: "{}",
    });
    expect(list).toHaveLength(2);
    expect(list[0]?.id).toBe("old");
    expect(list[1]?.content).toBe("{}");
  });

  it("批量插入（历史同步）同样受上限约束", () => {
    const incoming = Array.from({ length: MAX_THREAD_ARTIFACTS + 3 }, (_, index) =>
      makeArtifact(`batch-${index}`),
    );
    const list = upsertArtifacts([], incoming);
    expect(list).toHaveLength(MAX_THREAD_ARTIFACTS);
    expect(list[0]?.id).toBe(`batch-${incoming.length - 1}`);
  });
});
