import Prism from "prismjs";

import "prismjs/components/prism-bash";
import "prismjs/components/prism-clike";
import "prismjs/components/prism-css";
import "prismjs/components/prism-diff";
import "prismjs/components/prism-go";
import "prismjs/components/prism-javascript";
import "prismjs/components/prism-json";
import "prismjs/components/prism-jsx";
import "prismjs/components/prism-markdown";
import "prismjs/components/prism-markup";
import "prismjs/components/prism-markup-templating";
import "prismjs/components/prism-powershell";
import "prismjs/components/prism-python";
import "prismjs/components/prism-sql";
import "prismjs/components/prism-tsx";
import "prismjs/components/prism-typescript";
import "prismjs/components/prism-yaml";

export type CodeHighlightSegment = {
  content: string;
  types: string[];
};

export type CodeHighlightLineKind = "normal" | "inserted" | "deleted";

export type CodeHighlightLine = {
  kind: CodeHighlightLineKind;
  segments: CodeHighlightSegment[];
};

type PrismTokenLike = Prism.Token | string | Prism.TokenStream;

const LANGUAGE_ALIASES: Record<string, string> = {
  bash: "bash",
  console: "bash",
  css: "css",
  diff: "diff",
  golang: "go",
  go: "go",
  html: "markup",
  javascript: "javascript",
  js: "javascript",
  jsx: "jsx",
  json: "json",
  markdown: "markdown",
  md: "markdown",
  patch: "diff",
  plaintext: "text",
  powershell: "powershell",
  ps1: "powershell",
  py: "python",
  python: "python",
  pwsh: "powershell",
  shell: "bash",
  shellsession: "bash",
  "shell-session": "bash",
  sh: "bash",
  sql: "sql",
  text: "text",
  ts: "typescript",
  tsx: "tsx",
  txt: "text",
  typescript: "typescript",
  xml: "markup",
  yml: "yaml",
  yaml: "yaml",
  zsh: "bash",
};

function normalizeLanguage(language: string) {
  const normalized = language.trim().toLowerCase();
  return LANGUAGE_ALIASES[normalized] ?? normalized;
}

function normalizeTypes(types: string[]) {
  return [...new Set(types.map((type) => type.trim()).filter(Boolean))];
}

function appendSegments(
  lines: CodeHighlightLine[],
  text: string,
  types: string[],
) {
  const normalizedText = text.replace(/\r\n?/g, "\n");
  const parts = normalizedText.split("\n");

  for (let index = 0; index < parts.length; index += 1) {
    const part = parts[index];
    if (part) {
      lines[lines.length - 1].segments.push({
        content: part,
        types,
      });
    }

    if (index < parts.length - 1) {
      lines.push({
        kind: "normal",
        segments: [],
      });
    }
  }
}

function walkTokens(
  lines: CodeHighlightLine[],
  token: PrismTokenLike,
  inheritedTypes: string[],
) {
  if (typeof token === "string") {
    appendSegments(lines, token, inheritedTypes);
    return;
  }

  if (Array.isArray(token)) {
    token.forEach((item) => walkTokens(lines, item, inheritedTypes));
    return;
  }

  const aliases = Array.isArray(token.alias)
    ? token.alias
    : typeof token.alias === "string"
      ? [token.alias]
      : [];
  const nextTypes = normalizeTypes([...inheritedTypes, token.type, ...aliases]);

  walkTokens(lines, token.content as PrismTokenLike, nextTypes);
}

function buildPlainTextLines(code: string) {
  return code.replace(/\r\n?/g, "\n").split("\n").map((line) => ({
    kind: "normal" as const,
    segments: line
      ? [
          {
            content: line,
            types: [],
          },
        ]
      : [],
  }));
}

function resolveDiffLineKind(line: string): CodeHighlightLineKind {
  if (line.startsWith("+") && !line.startsWith("+++")) {
    return "inserted";
  }
  if (line.startsWith("-") && !line.startsWith("---")) {
    return "deleted";
  }
  return "normal";
}

function applyDiffLineKinds(
  lines: CodeHighlightLine[],
  code: string,
  language: string,
) {
  if (language !== "diff") {
    return lines;
  }

  const rawLines = code.replace(/\r\n?/g, "\n").split("\n");
  return lines.map((line, index) => ({
    ...line,
    kind: resolveDiffLineKind(rawLines[index] ?? ""),
  }));
}

export function highlightCode(code: string, language: string): CodeHighlightLine[] {
  const normalizedCode = code.replace(/\r\n?/g, "\n");
  const normalizedLanguage = normalizeLanguage(language);
  const grammar =
    normalizedLanguage === "text" ? null : Prism.languages[normalizedLanguage];

  if (!grammar) {
    return applyDiffLineKinds(
      buildPlainTextLines(normalizedCode),
      normalizedCode,
      normalizedLanguage,
    );
  }

  const lines: CodeHighlightLine[] = [
    {
      kind: "normal",
      segments: [],
    },
  ];
  walkTokens(lines, Prism.tokenize(normalizedCode, grammar), []);

  return applyDiffLineKinds(
    lines.length > 0
      ? lines
      : [
          {
            kind: "normal",
            segments: [],
          },
        ],
    normalizedCode,
    normalizedLanguage,
  );
}
