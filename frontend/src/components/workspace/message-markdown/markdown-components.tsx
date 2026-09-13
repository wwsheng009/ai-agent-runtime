// 由 components/workspace/message-markdown.tsx 机械拆分而来（P0-2），仅搬迁不改语义。
// Markdown 渲染组件表与文本/链接工具（react-markdown Components 配置）

import { isValidElement, type ReactNode } from "react";
import { type Components } from "react-markdown";
import { CodeBlock } from "@/components/ui/code-block";

const LINK_CLASS_NAME =
  "font-medium text-accent-secondary underline decoration-accent-secondary/35 underline-offset-4 transition hover:text-foreground hover:decoration-accent-secondary";

const INLINE_CODE_CLASS_NAME =
  "app-inline-mono rounded-md border border-border bg-surface-solid px-1.5 py-0.5 text-[0.95em] text-foreground";

// 图片与占位样式：占位尽量贴近 <img> 的卡片观感，避免流式/被拦截时布局跳动。
const IMAGE_CLASS_NAME =
  "my-4 max-h-[24rem] max-w-full rounded-card border border-border object-contain";

const IMAGE_PLACEHOLDER_CLASS_NAME =
  "my-4 inline-flex max-w-full items-center justify-center rounded-card border border-dashed border-border bg-surface-solid px-3 py-2 app-text-12 text-muted-foreground";

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

// 链接协议白名单：无 scheme 的引用（锚点 `#…` / 站内 `/…` / `./` `../` / 裸相对
// 路径）与绝对 http(s) / mailto / tel；协议相对 `//host`（scheme 由页面决定、
// 无法在此校验）与 javascript:、data:、file: 等一律不进入可导航链接。
// react-markdown 自带的 urlTransform 是前置防线，这里是渲染层兜底。
const URL_SCHEME_PATTERN = /^[a-z][a-z0-9+.-]*:/i;
const ALLOWED_URL_SCHEME_PATTERN = /^(?:https?|mailto|tel):/i;

// 图片仅放行绝对 http(s)，避免正文里的相对路径被上游文本注入成任意资源探测。
const ABSOLUTE_HTTP_URL_PATTERN = /^https?:\/\//i;

function isAllowedHref(href: string) {
  const trimmed = href.trim();
  if (trimmed.startsWith("//")) {
    return false;
  }
  if (!URL_SCHEME_PATTERN.test(trimmed)) {
    return true;
  }
  return ALLOWED_URL_SCHEME_PATTERN.test(trimmed);
}

// 流式期文本仍可能被追加/改写，`href` 一旦烘焙进 DOM 就可能指向已过期的目标，
// 也会提前产生可导航链接；因此流式期只渲染不带 href 的占位 <a>（保留样式与
// children），待 settled（streaming=false 的最终渲染）再按白名单重新解析并
// 补齐 href —— 这就是不烘焙 handler 后的自愈路径。
function renderMarkdownLink(
  children: ReactNode,
  href: string | undefined,
  streaming: boolean,
) {
  if (streaming) {
    return (
      <a className={LINK_CLASS_NAME} data-streaming-link="true">
        {children}
      </a>
    );
  }

  const safeHref = href && isAllowedHref(href) ? href : undefined;
  const target =
    safeHref && !isInternalHref(safeHref) ? "_blank" : undefined;
  const rel = target ? "noreferrer noopener" : undefined;

  return (
    <a
      className={LINK_CLASS_NAME}
      href={safeHref}
      rel={rel}
      target={target}
    >
      {children}
    </a>
  );
}

// 图片占位：流式期与「非绝对 http(s) 被拦截」共用同一样式，仅用 data-* 区分状态。
function renderImagePlaceholder(
  alt: string | undefined,
  source: "blocked" | "streaming",
) {
  return (
    <span
      aria-label={alt ?? ""}
      className={IMAGE_PLACEHOLDER_CLASS_NAME}
      data-image-blocked={source === "blocked" ? "true" : undefined}
      data-streaming-image={source === "streaming" ? "true" : undefined}
      role="img"
    >
      {alt}
    </span>
  );
}

// 流式期不为可能仍在变化的 src 发请求；settled 后仅放行绝对 http(s) 图片，
// 其余（相对路径、data: 等）保持占位，避免相对资源探测与二次渲染抖动。
function renderMarkdownImage(
  alt: string | undefined,
  src: string | undefined,
  streaming: boolean,
) {
  if (streaming) {
    return renderImagePlaceholder(alt, "streaming");
  }

  if (src && ABSOLUTE_HTTP_URL_PATTERN.test(src)) {
    return (
      <img
        alt={alt ?? ""}
        className={IMAGE_CLASS_NAME}
        loading="lazy"
        src={src}
      />
    );
  }

  return renderImagePlaceholder(alt, "blocked");
}

export function createMarkdownComponents(streaming: boolean): Components {
  return {
    a: ({ children, href }) => renderMarkdownLink(children, href, streaming),
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
    img: ({ alt, src }) => renderMarkdownImage(alt, src, streaming),
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

// 内联片段（列表项 / 表格单元格 / 引用段落）与完整组件表复用同一套链接/图片
// 安全策略：流式期传 streaming=true 关闭 href/src 烘焙，settled 时传 false 自愈。
export function createInlineMarkdownComponents(
  streaming: boolean,
): Components {
  return {
    a: ({ children, href }) => renderMarkdownLink(children, href, streaming),
    code: ({ children }) => (
      <code className={INLINE_CODE_CLASS_NAME}>
        {children}
      </code>
    ),
    img: ({ alt, src }) => renderMarkdownImage(alt, src, streaming),
    p: ({ children }) => <>{children}</>,
  };
}

