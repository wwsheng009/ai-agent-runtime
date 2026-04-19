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
