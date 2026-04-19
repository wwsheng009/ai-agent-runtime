import { type Artifact } from "@/data/mock";

export type ArtifactCategory = "evidence" | "file";

export function classifyArtifactCategory(artifact: Artifact): ArtifactCategory {
  return artifact.path.startsWith("runtime/") ? "evidence" : "file";
}

export function formatArtifactCategory(category: ArtifactCategory) {
  return category === "evidence" ? "Runtime evidence" : "Output file";
}

export function isArtifactEvidence(artifact: Artifact) {
  return classifyArtifactCategory(artifact) === "evidence";
}
