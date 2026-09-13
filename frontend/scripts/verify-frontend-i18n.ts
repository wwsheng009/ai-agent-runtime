// P0-6 i18n 硬编码门禁：用 TypeScript 编译器 AST 扫描 src/**/*.{ts,tsx}，
// 找出未走 i18n 的可翻译 JSX 文本与 copy 型属性值
// （aria-label / title / placeholder / alt / label / confirmLabel）。
//
// 只依赖 Node 内置模块与 devDependency `typescript`，不引入新依赖、不依赖 tsx/ts-node。
// 运行（Node 24 原生类型剥离，本文件仅使用可擦除语法）：
//   cd frontend; node scripts/verify-frontend-i18n.ts [--json]
// 退出码：0 = 无违规；1 = 存在违规；2 = 扫描文件数低于防呆阈值（默认 80，可用
// 环境变量 I18N_LINT_MIN_FILES 覆盖）。
// 输出：按文件分组的 `文件:行:列 说明`，末尾摘要 `violations=N files=M scanned=K`
// （files = 含违规的文件数）；--json 输出机器可读结果。

import fs from "node:fs";
import path from "node:path";

import ts from "typescript";

const DEFAULT_MIN_FILES = 80;
const SOURCE_EXTENSIONS = new Set([".ts", ".tsx"]);
const COPY_ATTRIBUTES = new Set([
  "aria-label",
  "title",
  "placeholder",
  "alt",
  "label",
  "confirmLabel",
]);
const TRANSLATABLE_PATTERN = /\p{L}/u;
const MAX_REPORT_TEXT_LENGTH = 80;

const scriptDir = import.meta.dirname;
const frontendRoot = path.resolve(scriptDir, "..");
const sourceRoot = path.join(frontendRoot, "src");

type ViolationKind = "jsx-text" | "copy-attribute";

type Violation = {
  file: string;
  line: number;
  column: number;
  kind: ViolationKind;
  text: string;
  attribute?: string;
};

type Totals = {
  violations: number;
  scanned: number;
  files: number;
  skippedTokens: number;
  skippedInterpolated: number;
};

type JsonPayload = {
  totals: Totals;
  violations: Violation[];
  error?: string;
};

type FileScan = {
  violations: Violation[];
  skippedTokens: number;
  skippedInterpolated: number;
};

function toPosix(relativePath: string): string {
  return relativePath.split(path.sep).join("/");
}

function isExcluded(relativePath: string): boolean {
  if (!relativePath.startsWith("src/")) {
    return true;
  }
  if (
    relativePath.startsWith("src/i18n/") ||
    relativePath.startsWith("src/test/")
  ) {
    return true;
  }
  const base = path.posix.basename(relativePath);
  return base.endsWith(".d.ts") || /\.(test|spec)\./.test(base);
}

function collectSourceFiles(): string[] {
  const collected: string[] = [];
  const walk = (directory: string): void => {
    const entries = fs
      .readdirSync(directory, { withFileTypes: true })
      .sort((left, right) => left.name.localeCompare(right.name));
    for (const entry of entries) {
      const absolute = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        if (entry.name !== "node_modules") {
          walk(absolute);
        }
        continue;
      }
      if (!entry.isFile()) {
        continue;
      }
      if (!SOURCE_EXTENSIONS.has(path.extname(entry.name))) {
        continue;
      }
      const relative = toPosix(path.relative(frontendRoot, absolute));
      if (!isExcluded(relative)) {
        collected.push(relative);
      }
    }
  };
  walk(sourceRoot);
  return collected.sort((left, right) => left.localeCompare(right));
}

function normalizeText(raw: string): string {
  return raw.replace(/\s+/gu, " ").trim();
}

function truncateText(text: string): string {
  return text.length > MAX_REPORT_TEXT_LENGTH
    ? `${text.slice(0, MAX_REPORT_TEXT_LENGTH)}…`
    : text;
}

function getAttributeName(name: ts.JsxAttributeName): string | undefined {
  if (ts.isIdentifier(name)) {
    return name.text;
  }
  if (ts.isJsxNamespacedName(name)) {
    return `${name.namespace.text}:${name.name.text}`;
  }
  return undefined;
}

// 纯 token / 纯符号（·、—、/、%、→、+、:、…、×、纯数字等）不算违规，计入 skippedTokens。
function isTranslatable(text: string): boolean {
  return TRANSLATABLE_PATTERN.test(text);
}

function scanFile(relativeFile: string): FileScan {
  const absoluteFile = path.join(frontendRoot, relativeFile);
  const sourceText = fs.readFileSync(absoluteFile, "utf8");
  const scriptKind = relativeFile.endsWith(".tsx")
    ? ts.ScriptKind.TSX
    : ts.ScriptKind.TS;
  const sourceFile = ts.createSourceFile(
    relativeFile,
    sourceText,
    ts.ScriptTarget.Latest,
    false,
    scriptKind,
  );

  const violations: Violation[] = [];
  let skippedTokens = 0;
  let skippedInterpolated = 0;

  const report = (
    position: number,
    kind: ViolationKind,
    text: string,
    attribute?: string,
  ): void => {
    const location = sourceFile.getLineAndCharacterOfPosition(position);
    const violation: Violation = {
      file: relativeFile,
      line: location.line + 1,
      column: location.character + 1,
      kind,
      text,
    };
    if (attribute !== undefined) {
      violation.attribute = attribute;
    }
    violations.push(violation);
  };

  const inspectAttributeText = (
    raw: string,
    position: number,
    attribute: string,
  ): void => {
    const text = normalizeText(raw);
    if (text.length === 0) {
      return;
    }
    if (!isTranslatable(text)) {
      skippedTokens += 1;
      return;
    }
    report(position, "copy-attribute", text, attribute);
  };

  const inspectAttributeValue = (
    value: ts.JsxAttributeValue,
    attribute: string,
  ): void => {
    if (ts.isStringLiteral(value)) {
      inspectAttributeText(value.text, value.getStart(sourceFile) + 1, attribute);
      return;
    }
    if (!ts.isJsxExpression(value) || value.expression === undefined) {
      return;
    }
    let expression: ts.Expression = value.expression;
    while (ts.isParenthesizedExpression(expression)) {
      expression = expression.expression;
    }
    if (ts.isStringLiteral(expression)) {
      inspectAttributeText(
        expression.text,
        expression.getStart(sourceFile) + 1,
        attribute,
      );
      return;
    }
    if (ts.isNoSubstitutionTemplateLiteral(expression)) {
      inspectAttributeText(
        expression.text,
        expression.getStart(sourceFile) + 1,
        attribute,
      );
      return;
    }
    // 含插值的模板字面量不报违规，只计数（内容由运行期拼接，静态无法判定）。
    if (ts.isTemplateExpression(expression)) {
      skippedInterpolated += 1;
    }
  };

  const visit = (node: ts.Node): void => {
    if (ts.isJsxText(node)) {
      const raw = sourceText.slice(node.pos, node.end);
      const text = normalizeText(raw);
      if (text.length > 0) {
        if (isTranslatable(text)) {
          const leadingWhitespace = raw.length - raw.trimStart().length;
          report(node.pos + leadingWhitespace, "jsx-text", text);
        } else {
          skippedTokens += 1;
        }
      }
    } else if (ts.isJsxAttribute(node) && node.initializer !== undefined) {
      const attribute = getAttributeName(node.name);
      if (attribute !== undefined && COPY_ATTRIBUTES.has(attribute)) {
        inspectAttributeValue(node.initializer, attribute);
      }
    }
    ts.forEachChild(node, visit);
  };

  visit(sourceFile);

  return { violations, skippedTokens, skippedInterpolated };
}

function formatViolation(violation: Violation): string {
  const location = `${violation.file}:${violation.line}:${violation.column}`;
  const label =
    violation.kind === "jsx-text"
      ? "jsx-text"
      : `copy-attribute:${violation.attribute ?? "?"}`;
  return `${location} [${label}] ${JSON.stringify(truncateText(violation.text))}`;
}

function readMinFiles(): number {
  const raw = process.env.I18N_LINT_MIN_FILES?.trim();
  if (!raw) {
    return DEFAULT_MIN_FILES;
  }
  const parsed = Number(raw);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : DEFAULT_MIN_FILES;
}

function run(): void {
  const jsonMode = process.argv.includes("--json");
  const minFiles = readMinFiles();
  const files = collectSourceFiles();

  if (files.length < minFiles) {
    const message =
      `i18n lint 扫描文件数异常：scanned=${files.length} < min=${minFiles}；` +
      "请检查扫描范围（可用 I18N_LINT_MIN_FILES 覆盖阈值）";
    console.error(message);
    if (jsonMode) {
      const payload: JsonPayload = {
        totals: {
          violations: 0,
          scanned: files.length,
          files: 0,
          skippedTokens: 0,
          skippedInterpolated: 0,
        },
        violations: [],
        error: message,
      };
      console.log(JSON.stringify(payload, null, 2));
    }
    process.exitCode = 2;
    return;
  }

  const violations: Violation[] = [];
  let skippedTokens = 0;
  let skippedInterpolated = 0;
  for (const file of files) {
    const scan = scanFile(file);
    violations.push(...scan.violations);
    skippedTokens += scan.skippedTokens;
    skippedInterpolated += scan.skippedInterpolated;
  }
  violations.sort(
    (left, right) =>
      left.file.localeCompare(right.file) ||
      left.line - right.line ||
      left.column - right.column,
  );

  const totals: Totals = {
    violations: violations.length,
    scanned: files.length,
    files: new Set(violations.map((violation) => violation.file)).size,
    skippedTokens,
    skippedInterpolated,
  };

  if (jsonMode) {
    const payload: JsonPayload = { totals, violations };
    console.log(JSON.stringify(payload, null, 2));
    process.exitCode = violations.length > 0 ? 1 : 0;
    return;
  }

  if (violations.length > 0) {
    for (const violation of violations) {
      console.log(formatViolation(violation));
    }
    console.log("");
    console.log(
      `violations=${totals.violations} files=${totals.files} scanned=${totals.scanned}`,
    );
    process.exitCode = 1;
    return;
  }

  console.log(`i18n lint OK（scanned=${totals.scanned}, violations=0）`);
  process.exitCode = 0;
}

run();
