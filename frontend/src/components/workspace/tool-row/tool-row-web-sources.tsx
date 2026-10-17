// P3-1：web_search 来源卡片（展开面板内的结构化块）。
//
// 数据只来自防御解析后的 `details.webSearch`（details.ts / history-mapping.ts）；
// 解析失败时组件不渲染，面板保持现有原始文本块——本组件绝不从渲染文本反推
// 结构化数据，也不为缺失字段补默认值。

import { ExternalLinkIcon } from "lucide-react";
import { useTranslation } from "react-i18next";

import { isExternalUrl } from "@/lib/tool-row";
import { webSearchSourceDomain, type WebSearchMetaView } from "@/lib/tool-row/web-search-meta";

type ToolRowWebSourcesProps = {
  meta: WebSearchMetaView;
};

export function ToolRowWebSources({ meta }: ToolRowWebSourcesProps) {
  const { t } = useTranslation("workspace");
  if (meta.sources.length === 0 && !meta.hasAnswer) {
    return null;
  }

  return (
    <div
      className="mt-1 rounded-lg bg-surface-soft px-2.5 py-2"
      data-tool-row-web-sources="true"
    >
      <div className="flex flex-wrap items-center gap-1.5 app-text-10 uppercase tracking-[0.14em] text-muted-foreground">
        <span>{t("panels.messages.toolRow.webSources.label")}</span>
        {meta.sources.length > 0 ? (
          <span
            className="rounded-full bg-accent-teal/10 px-1.5 app-text-10 normal-case tracking-normal text-accent-teal"
            data-tool-row-web-count={String(meta.sources.length)}
          >
            {t("panels.messages.toolRow.webSources.count", { count: meta.sources.length })}
          </span>
        ) : null}
        {meta.hasAnswer ? (
          <span
            className="rounded-full bg-accent-teal/10 px-1.5 app-text-10 normal-case tracking-normal text-accent-teal"
            data-tool-row-web-answer="true"
          >
            {t("panels.messages.toolRow.webSources.answer")}
          </span>
        ) : null}
        {meta.truncated ? (
          <span className="normal-case tracking-normal" data-tool-row-web-truncated="true">
            {t("panels.messages.toolRow.webSources.truncated")}
          </span>
        ) : null}
      </div>
      {meta.sources.length > 0 ? (
        <ul className="mt-1 max-h-40 space-y-0.5 overflow-y-auto">
          {meta.sources.map((source) => (
            <li
              className="flex min-w-0 items-center gap-1.5"
              data-tool-row-web-source="true"
              key={source.url}
            >
              {isExternalUrl(source.url) ? (
                <a
                  className="inline-flex min-w-0 shrink items-center gap-1 font-mono app-text-11 underline decoration-dotted underline-offset-2 transition hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-teal/45"
                  href={source.url}
                  rel="noreferrer noopener"
                  target="_blank"
                  title={source.url}
                >
                  <span className="min-w-0 truncate">{webSearchSourceDomain(source.url)}</span>
                  <ExternalLinkIcon className="shrink-0" size={11} />
                </a>
              ) : (
                <span className="min-w-0 truncate font-mono app-text-11">
                  {webSearchSourceDomain(source.url)}
                </span>
              )}
              {source.title ? (
                <span
                  className="min-w-0 truncate app-text-11 text-muted-foreground"
                  title={source.title}
                >
                  {source.title}
                </span>
              ) : null}
              {source.publishedAt ? (
                <span className="shrink-0 app-text-10 text-muted-foreground">
                  {source.publishedAt}
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}