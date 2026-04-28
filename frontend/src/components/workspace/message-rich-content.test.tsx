// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import type { Artifact, MessageSegment } from "@/data/mock";

import {
  MessageRelatedArtifacts,
  MessageRichSegment,
} from "./message-rich-content";

function extractAttribute(markup: string, attribute: string) {
  const match = markup.match(new RegExp(`${attribute}="([^"]+)"`));

  expect(match).not.toBeNull();

  return match?.[1] ?? "";
}

describe("MessageRichSegment", () => {
  it("renders generated image segments with captions and accessible alt text", () => {
    const image: MessageSegment = {
      type: "image",
      src: "/api/runtime/sessions/session-1/generated-images/image_1",
      alt: "a tiny robot",
      caption: "a tiny robot",
      artifactId: "generated-image:session-1:image_1",
      imageId: "image_1",
    };

    const markup = renderToStaticMarkup(
      <MessageRichSegment segment={image} />,
    );

    expect(markup).toContain("generated-images/image_1");
    expect(markup).toContain('alt="a tiny robot"');
    expect(markup).toContain("a tiny robot");
    expect(markup).toContain("figure");
  });

  it("renders image placeholders with progress and failure state", () => {
    const placeholder: MessageSegment = {
      type: "image-placeholder",
      imageId: "image_1",
      phase: "partial",
      progress: 0.5,
      caption: "a tiny robot",
    };

    const markup = renderToStaticMarkup(
      <MessageRichSegment segment={placeholder} />,
    );

    expect(markup).toContain("图片生成中");
    expect(markup).toContain("a tiny robot");
    expect(markup).toContain("width:50%");

    const failed: MessageSegment = {
      type: "image-placeholder",
      imageId: "image_2",
      phase: "failed",
      errorMessage: "image save warning",
    };

    const failedMarkup = renderToStaticMarkup(
      <MessageRichSegment segment={failed} />,
    );

    expect(failedMarkup).toContain("图片生成失败");
    expect(failedMarkup).toContain("image save warning");
  });

  it("adds labels to checklist sections", () => {
    const checklist: MessageSegment = {
      type: "checklist",
      title: "Review steps",
      items: ["Check stream state", "Confirm markdown output"],
    };

    const markup = renderToStaticMarkup(
      <MessageRichSegment segment={checklist} />,
    );

    const labelId = extractAttribute(markup, "aria-labelledby");

    expect(markup).toContain(`id="${labelId}"`);
    expect(markup).toContain('role="list"');
    expect(markup).toContain("Review steps");
  });

  it("adds note semantics to callouts", () => {
    const callout: MessageSegment = {
      type: "callout",
      title: "Runtime note",
      tone: "info",
      content: "Use **structured tails** while streaming.",
    };

    const markup = renderToStaticMarkup(
      <MessageRichSegment segment={callout} />,
    );

    const labelId = extractAttribute(markup, "aria-labelledby");
    const descriptionId = extractAttribute(markup, "aria-describedby");

    expect(markup).toContain('role="note"');
    expect(markup).toContain(`id="${labelId}"`);
    expect(markup).toContain(`id="${descriptionId}"`);
    expect(markup).toContain("structured tails");
  });
});

describe("MessageRelatedArtifacts", () => {
  let container: HTMLDivElement;
  let root: Root | null;

  const relatedArtifacts: Artifact[] = [
    {
      id: "artifact-1",
      name: "runtime-summary.md",
      path: "artifacts/runtime-summary.md",
      summary: "Streaming summary snapshot",
      kind: "code",
      language: "ts",
      content: "console.log('runtime');",
    },
  ];

  beforeEach(() => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = null;
  });

  afterEach(() => {
    if (root) {
      act(() => {
        root?.unmount();
      });
    }
    container.remove();
    document.body.innerHTML = "";
  });

  function renderMessageRelatedArtifacts() {
    root = createRoot(container);

    act(() => {
      root?.render(
        <MessageRelatedArtifacts
          onSelectArtifact={() => {}}
          relatedArtifacts={relatedArtifacts}
        />,
      );
    });
  }

  function dispatchClick(target: EventTarget) {
    act(() => {
      target.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
  }

  it("renders related evidence collapsed by default", () => {
    const markup = renderToStaticMarkup(
      <MessageRelatedArtifacts
        onSelectArtifact={() => {}}
        relatedArtifacts={relatedArtifacts}
      />,
    );

    const labelId = extractAttribute(markup, "aria-labelledby");
    const descriptionId = extractAttribute(markup, "aria-describedby");

    expect(markup).toContain(`id="${labelId}"`);
    expect(markup).toContain(`id="${descriptionId}"`);
    expect(markup).toContain('aria-expanded="false"');
    expect(markup).toContain('hidden=""');
    expect(markup).toContain("Related evidence");
    expect(markup).toContain("1 related evidence item hidden");
    expect(markup).not.toContain("runtime-summary.md");
    expect(markup).not.toContain("Streaming summary snapshot");
  });

  it("expands related evidence on demand", () => {
    renderMessageRelatedArtifacts();

    const toggle = container.querySelector('button[aria-expanded="false"]');
    expect(toggle).toBeInstanceOf(HTMLButtonElement);

    dispatchClick(toggle as HTMLButtonElement);

    expect(container.textContent).toContain("runtime-summary.md");
    expect(container.textContent).toContain("Streaming summary snapshot");
    expect(container.querySelector('button[aria-expanded="true"]')).toBeInstanceOf(
      HTMLButtonElement,
    );
  });
});
