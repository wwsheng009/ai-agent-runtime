import Prism from "prismjs";

// Prism language components are legacy scripts which read a global `Prism`
// identifier. Static side-effect imports can be reordered by production
// bundlers, causing a component chunk to execute before the core is exposed.
// Set the global explicitly, then load components in dependency order.
(globalThis as typeof globalThis & { Prism: typeof Prism }).Prism = Prism;

async function loadPrismLanguages() {
  await import("prismjs/components/prism-markup");
  await import("prismjs/components/prism-css");
  await import("prismjs/components/prism-clike");
  await import("prismjs/components/prism-javascript");
  await import("prismjs/components/prism-markup-templating");
  await import("prismjs/components/prism-bash");
  await import("prismjs/components/prism-diff");
  await import("prismjs/components/prism-go");
  await import("prismjs/components/prism-json");
  await import("prismjs/components/prism-jsx");
  await import("prismjs/components/prism-markdown");
  await import("prismjs/components/prism-powershell");
  await import("prismjs/components/prism-python");
  await import("prismjs/components/prism-sql");
  await import("prismjs/components/prism-typescript");
  await import("prismjs/components/prism-tsx");
  await import("prismjs/components/prism-yaml");
}

// Do not block the Workspace route module with top-level await. Code blocks
// render a plain-text fallback while the legacy Prism components load, then
// re-render once the language catalog is ready.
export const codeHighlightingReady = loadPrismLanguages().then(
  () => undefined,
  () => undefined,
);

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
