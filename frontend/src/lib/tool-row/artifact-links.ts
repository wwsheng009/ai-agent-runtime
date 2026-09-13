// P1-6：从消息关联产物构造工具行文件链接解析器。命中产物才可激活，避免死链接。

import { type Artifact } from "@/data/mock";

import { matchToolFilePath } from "./details";

export function createArtifactFilePathLinkResolver(
  artifacts: Artifact[],
  onSelectArtifact: (artifactId: string) => void,
) {
  return (path: string): (() => void) | null => {
    const artifact = artifacts.find((candidate) => matchToolFilePath(candidate.path, path));
    return artifact ? () => onSelectArtifact(artifact.id) : null;
  };
}
