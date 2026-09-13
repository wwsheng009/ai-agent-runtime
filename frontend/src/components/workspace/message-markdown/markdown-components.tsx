// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// Markdown 渲染组件表与文本/链接工具（react-markdown Components 配置）

import { isValidElement, type ReactNode } from "react";
import { type Components } from "react-markdown";
import { CodeBlock } from "@/components/ui/code-block";

const LINK_CLASS_NAME =
  "font-medium text-accent-secondary underline decoration-accent-secondary/35 underline-offset-4 transition hover:text-foreground hover:decoration-accent-secondary";

const INLINE_CODE_CLASS_NAME =
  "app-inline-mono rounded-md border border-border bg-surface-solid px-1.5 py-0.5 text-[0.95em] text-foreground";

function collectTextContent(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") {
    return String(node);
  }

  if (Array.isArray(node)) {
    return node.map((item) => collectTextContent(item)).join("");
  }

  if (node === null || node === undefined || typeof node === "boolean") {
    return "";
  }

  if (isValidElement<{ children?: ReactNode }>(node)) {
    return collectTextContent(node.props.children);
  }

  return "";
}

function getCodeLanguage(className?: string) {
  const match = /language-([A-Za-z0-9_-]+)/.exec(className ?? "");
  return match?.[1] ?? null;
}

function isInternalHref(href: string) {
  return /^(#|\/(?!\/)|\.\.?\/)/.test(href);
}

function renderMarkdownLink(children: ReactNode, href?: string) {
  const target =
    href && !isInternalHref(href) ? "_blank" : undefined;
  const rel = target ? "noreferrer noopener" : undefined;

  return (
    <a
      className={LINK_CLASS_NAME}
      href={href}
      rel={rel}
      target={target}
    >
      {children}
    </a>
  );
}

export function createMarkdownComponents(streaming: boolean): Components {
  return {
    a: ({ children, href }) => renderMarkdownLink(children, href),
    blockquote: ({ children }) => (
      <blockquote className="my-4 rounded-r-card border-l-2 border-accent-secondary/45 bg-surface-solid px-4 py-2.5 text-muted-foreground">
        {children}
      </blockquote>
    ),
    code: ({ children, className }) => {
      const language = getCodeLanguage(className);
      if (language) {
        return (
          <CodeBlock
            className="my-4"
            collapsible
            code={collectTextContent(children).replace(/\n$/, "")}
            language={language}
            streaming={streaming}
          />
        );
      }

      return (
        <code className={INLINE_CODE_CLASS_NAME}>
          {children}
        </code>
      );
    },
    h1: ({ children }) => (
      <h1 className="mb-3 mt-5 text-[1.45em] font-semibold tracking-[-0.02em] text-foreground first:mt-0">
        {children}
      </h1>
    ),
    h2: ({ children }) => (
      <h2 className="mb-3 mt-5 text-[1.28em] font-semibold tracking-[-0.02em] text-foreground first:mt-0">
        {children}
      </h2>
    ),
    h3: ({ children }) => (
      <h3 className="mb-2.5 mt-4 text-[1.14em] font-semibold text-foreground first:mt-0">
        {children}
      </h3>
    ),
    hr: () => <hr className="my-4 border-0 border-t border-border" />,
    img: ({ alt, src }) => (
      <img
        alt={alt ?? ""}
        className="my-4 max-h-[24rem] max-w-full rounded-card border border-border object-contain"
        loading="lazy"
        src={src}
      />
    ),
    input: ({ checked, type }) =>
      type === "checkbox" ? (
        <input
          checked={checked}
          className="mr-2 size-3.5 accent-accent-secondary"
          disabled
          type="checkbox"
        />
      ) : null,
    li: ({ children }) => (
      <li className="break-words pl-1 [&>p]:my-0">{children}</li>
    ),
    ol: ({ children }) => (
      <ol className="my-3 list-decimal space-y-2 pl-6 marker:text-muted-foreground">
        {children}
      </ol>
    ),
    p: ({ children }) => (
      <p className="my-3 whitespace-pre-wrap break-words text-foreground first:mt-0 last:mb-0">
        {children}
      </p>
    ),
    pre: ({ children }) => <>{children}</>,
    table: ({ children }) => (
      <div className="my-4 overflow-x-auto rounded-card border border-border bg-surface-solid">
        <table className="min-w-full border-collapse text-left app-text-13">
          {children}
        </table>
      </div>
    ),
    td: ({ children }) => (
      <td className="border-t border-border px-3 py-2.5 align-top text-foreground">
        {children}
      </td>
    ),
    th: ({ children }) => (
      <th className="bg-surface-softer px-3 py-2.5 font-semibold text-foreground">
        {children}
      </th>
    ),
    ul: ({ children }) => (
      <ul className="my-3 list-disc space-y-2 pl-6 marker:text-accent-secondary">
        {children}
      </ul>
    ),
  };
}

export const inlineMarkdownComponents: Components = {
  a: ({ children, href }) => renderMarkdownLink(children, href),
  code: ({ children }) => (
    <code className={INLINE_CODE_CLASS_NAME}>
      {children}
    </code>
  ),
  p: ({ children }) => <>{children}</>,
};
