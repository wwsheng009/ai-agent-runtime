// 由 lib/workspace-thread-state.ts 机械拆分而来（P0-2），仅搬迁不改语义。

import { buildRuntimeUrl } from "@/api/runtime/shared";
import { type Artifact, type MessageSegment } from "@/data/mock";
import { type SessionHistoryMessage } from "@/types/runtime";

import { readFirstNumberValue, readFirstTextValue } from "./history-mapping";
import { filepathBase, sanitizeArtifactToken, stripFileExtension, truncateText } from "./text-utils";

export type GeneratedImageAttachments = {
  artifacts: Artifact[];
  segments: MessageSegment[];
};

type GeneratedImageSegment =
  | Extract<MessageSegment, { type: "image" }>
  | Extract<MessageSegment, { type: "image-placeholder" }>;

export function buildGeneratedImagePlaceholderSegment(
  metadata: Record<string, unknown> | null | undefined,
): Extract<MessageSegment, { type: "image-placeholder" }> | null {
  const source = normalizeGeneratedImageProgressSource(metadata);
  if (!source) {
    return null;
  }

  const imageId = normalizeGeneratedImageToken(
    readFirstTextValue(source, "sanitized_id", "image_id", "item_id", "id"),
  );
  if (!imageId) {
    return null;
  }

  const phase = normalizeGeneratedImagePhase(
    readFirstTextValue(source, "phase", "status"),
  );
  const caption =
    readFirstTextValue(source, "caption", "revised_prompt", "revisedPrompt") ||
    undefined;
  const progress = readFirstNumberValue(source, "progress", "progress_ratio");
  const errorMessage =
    phase === "failed"
      ? readFirstTextValue(source, "error", "error_message", "message") || undefined
      : undefined;

  return {
    type: "image-placeholder",
    imageId,
    phase,
    progress,
    caption,
    errorMessage,
  };
}

export function upsertGeneratedImageSegment(
  segments: MessageSegment[],
  nextSegment: GeneratedImageSegment,
) {
  const nextImageId = getGeneratedImageSegmentId(nextSegment);
  if (!nextImageId) {
    return [...segments, nextSegment];
  }

  const merged: MessageSegment[] = [];
  let matched = false;

  for (const segment of segments) {
    if (!isGeneratedImageSegment(segment)) {
      merged.push(segment);
      continue;
    }

    const currentImageId = getGeneratedImageSegmentId(segment);
    if (currentImageId !== nextImageId) {
      merged.push(segment);
      continue;
    }

    if (!matched) {
      matched = true;
      if (nextSegment.type === "image-placeholder" && segment.type === "image") {
        merged.push(segment);
      } else {
        merged.push(nextSegment);
      }
    }
  }

  if (!matched) {
    merged.push(nextSegment);
  }

  return merged;
}

export function buildGeneratedImageAttachments(
  sessionId: string,
  metadata: Record<string, unknown> | null | undefined,
) {
  const attachments: GeneratedImageAttachments = {
    artifacts: [],
    segments: [],
  };

  const root = metadata && typeof metadata === "object" ? metadata : undefined;
  const rawImages = root?.["generated_images"];
  if (Array.isArray(rawImages)) {
    rawImages.forEach((item, index) => {
      if (!item || typeof item !== "object") {
        return;
      }

      const value = item as Record<string, unknown>;
      const rawId = readFirstTextValue(value, "id") || `generated-image-${index + 1}`;
      const imageId = normalizeGeneratedImageToken(rawId);
      const savedPath = readFirstTextValue(value, "saved_path", "savedPath");
      const basename =
        (savedPath ? filepathBase(savedPath) : "") ||
        `${imageId || "generated-image"}.png`;
      const artifactName = basename;
      const artifactPath = `runtime/generated-images/${artifactName}`;
      const prompt =
        readFirstTextValue(value, "revised_prompt", "revisedPrompt") || undefined;
      const contentName = imageId || stripFileExtension(artifactName);
      const src = buildGeneratedImageUrl(sessionId, contentName);
      const artifactId = buildGeneratedImageArtifactId(sessionId, contentName);
      const mimeType =
        readFirstTextValue(value, "mime_type", "mimeType") || "image/png";
      const summary = prompt
        ? `Generated image for ${truncateText(prompt, 96)}`
        : "Generated image saved from assistant output.";

      attachments.artifacts.push({
        id: artifactId,
        name: artifactName,
        path: artifactPath,
        summary,
        kind: "image",
        content: src,
        mimeType,
        byteCount: readFirstNumberValue(value, "byte_count", "byteCount"),
        sha256: readFirstTextValue(value, "sha256"),
        revisedPrompt: prompt,
      });

      attachments.segments.push({
        type: "image",
        src,
        alt: prompt || "Generated image",
        caption: prompt,
        artifactId,
        imageId: contentName,
      });
    });
  }

  const error = root ? readFirstTextValue(root, "generated_images_error") : "";
  if (error) {
    attachments.segments.push({
      type: "callout",
      title: "图片保存失败",
      tone: "warning",
      content: error,
    });
  }

  return attachments;
}

export function isGeneratedImageSegment(
  segment: MessageSegment,
): segment is GeneratedImageSegment {
  return segment.type === "image" || segment.type === "image-placeholder";
}

function getGeneratedImageSegmentId(segment: GeneratedImageSegment) {
  if (segment.type === "image-placeholder") {
    return normalizeGeneratedImageToken(segment.imageId);
  }
  if (segment.imageId && segment.imageId.trim()) {
    return normalizeGeneratedImageToken(segment.imageId);
  }
  if (segment.artifactId && segment.artifactId.trim()) {
    const token = segment.artifactId.trim().split(":").pop() ?? segment.artifactId;
    return normalizeGeneratedImageToken(token);
  }
  return "";
}

function normalizeGeneratedImageProgressSource(
  metadata: Record<string, unknown> | null | undefined,
) {
  if (!metadata || typeof metadata !== "object") {
    return undefined;
  }
  const nested = metadata["image"];
  if (nested && typeof nested === "object" && !Array.isArray(nested)) {
    return nested as Record<string, unknown>;
  }
  return metadata;
}

function normalizeGeneratedImagePhase(value: string) {
  switch (value.trim().toLowerCase()) {
    case "partial":
    case "progress":
    case "streaming":
      return "partial";
    case "completed":
    case "complete":
    case "done":
      return "completed";
    case "failed":
    case "error":
      return "failed";
    default:
      return "started";
  }
}

function normalizeGeneratedImageToken(value: string) {
  return sanitizeArtifactToken(value) || "";
}

export function extractGeneratedImagesFromAssistantMessage(
  message: SessionHistoryMessage,
  sessionId: string,
) {
  return buildGeneratedImageAttachments(sessionId, message.metadata);
}

function buildGeneratedImageUrl(sessionId: string, name: string) {
  return buildRuntimeUrl(
    `/api/runtime/sessions/${encodeURIComponent(sessionId)}/generated-images/${encodeURIComponent(name)}`,
  );
}

function buildGeneratedImageArtifactId(sessionId: string, name: string) {
  return ["generated-image", sessionId, sanitizeArtifactToken(name || "generated-image")].join(":");
}
