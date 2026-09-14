// P1-6：从消息关联产物构造工具行文件链接解析器。命中产物才可激活，避免死链接。
// P2-1A：未命中产物时可回退到「运行时文件预览」（宿主传入 onOpenFile）；两者都没有
// 则不渲染链接（返回 null），保持「无目标即无按钮」的既有约束。

import { type Artifact } from "@/data/mock";

import { matchToolFilePath } from "./details";

export function createArtifactFilePathLinkResolver(
  artifacts: Artifact[],
  onSelectArtifact: (artifactId: string) => void,
  onOpenFile?: (path: string) => void,
) {
  return (path: string): (() => void) | null => {
    const artifact = artifacts.find((candidate) => matchToolFilePath(candidate.path, path));
    if (artifact) {
      return () => onSelectArtifact(artifact.id);
    }
    return onOpenFile ? () => onOpenFile(path) : null;
  };
}
