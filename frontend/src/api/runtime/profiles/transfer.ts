// Runtime profile API —— 传输（导出/导入 zip；Batch 13 slice 8 / G5 / D32 / D33）。
//
// 纪律：
//   * 导出是 POST + 二进制响应：不能走 fetchRuntimeJson（它按 JSON 解析），
//     这里单独 fetch 并保留 X-Aicli-Profile-* 响应头；失败体仍是 JSON 错误信封，
//     所以错误路径复用 parseErrorPayload + RuntimeApiError，不吞后端原因；
//   * 导入的请求体是 zip 本身（不是 multipart）：name/layer/dry_run 走查询参数；
//   * `dry_run=true` 只预演不落盘（D33）；**400 + 报告体是领域结果不是传输错误**：
//     包能解压但过不了同一个 validate 时后端返回 `{valid:false, issues, paths}`，
//     这里按报告返回，让对话框展示问题清单；其余状态码（409 同名冲突等）照常抛错。

import type {
  RuntimeProfileExportBundle,
  RuntimeProfileImportOptions,
  RuntimeProfileImportReport,
} from "@/types/runtime";

import { RuntimeApiError, fetchRuntimeJson, parseErrorPayload } from "../shared";
import {
  asRecord,
  normalizeIssueList,
  readAliasedString,
  readNumber,
  readStringArray,
} from "./normalize";
import { buildRuntimeProfileExportUrl, buildRuntimeProfileImportUrl } from "./url";

/** 响应头里的十进制计数：缺失/非数字一律 0，不猜。 */
function headerCount(value: string | null) {
  const parsed = Number.parseInt(value ?? "", 10);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
}

/** 从 Content-Disposition 取文件名；缺失时用 `<ref>.zip`（ref 里的分隔符换成 `-`）。 */
function bundleFilename(disposition: string | null, ref: string) {
  const matched = disposition ? /filename="?([^";]+)"?/i.exec(disposition) : null;
  const name = matched?.[1]?.trim();
  return name || `${ref.replace(/[\\/:]/g, "-")}.zip`;
}

/** 导出 profile 目录为 zip（只读语义：后端不写盘、不碰会话与配置）。 */
export async function exportRuntimeProfile(ref: string): Promise<RuntimeProfileExportBundle> {
  const response = await fetch(buildRuntimeProfileExportUrl(ref), { method: "POST" });
  if (!response.ok) {
    throw new RuntimeApiError(response.status, await parseErrorPayload(response));
  }
  return {
    ref: response.headers.get("X-Aicli-Profile-Ref") || ref,
    filename: bundleFilename(response.headers.get("Content-Disposition"), ref),
    fileCount: headerCount(response.headers.get("X-Aicli-Profile-File-Count")),
    blob: await response.blob(),
  };
}

function normalizeImportReport(payload: unknown, dryRun: boolean): RuntimeProfileImportReport {
  const record = asRecord(payload) ?? {};
  return {
    ok: record.ok === true,
    valid: record.valid === true,
    imported: record.imported === true,
    activated: record.activated === true,
    dryRun: record.dry_run === true || dryRun,
    layer: readAliasedString(record, "layer"),
    name: readAliasedString(record, "name"),
    root: readAliasedString(record, "root"),
    paths: readStringArray(record.paths),
    fileCount: readNumber(record.file_count ?? record.fileCount, 0),
    error: readAliasedString(record, "error"),
    errorCount: readNumber(record.error_count ?? record.errorCount, 0),
    warningCount: readNumber(record.warning_count ?? record.warningCount, 0),
    issues: normalizeIssueList(record.issues),
    hint: readAliasedString(record, "hint"),
  };
}

/**
 * 400 报告体的判别：**同时**有 `valid` 布尔位与 `paths`/`issues` 数组才是
 * 导入报告；普通错误信封（`{error, code}`）没有这些字段，必须继续抛错。
 */
function isImportValidationReport(payload: unknown) {
  const record = asRecord(payload);
  if (!record || typeof record.valid !== "boolean") {
    return false;
  }
  return Array.isArray(record.paths) || Array.isArray(record.issues);
}

/**
 * 导入 zip 包为新 profile。
 *
 * 返回报告而不是「成功/失败」布尔：`valid=false`（包未过 validate）与
 * `imported=true`（已原子落位）都需要把 paths/issues/name 交给 UI（D28/D33）。
 * 绝不自动激活：报告的 `activated` 恒为 false，激活是独立动作。
 */
export async function importRuntimeProfile(
  bundle: Blob,
  options?: RuntimeProfileImportOptions,
): Promise<RuntimeProfileImportReport> {
  const url = buildRuntimeProfileImportUrl({
    name: options?.name,
    layer: options?.layer,
    dryRun: options?.dryRun,
  });
  const init: RequestInit = {
    method: "POST",
    headers: { "Content-Type": "application/zip" },
    body: bundle,
  };
  try {
    return normalizeImportReport(await fetchRuntimeJson<unknown>(url, init), options?.dryRun === true);
  } catch (error) {
    if (
      error instanceof RuntimeApiError &&
      error.status === 400 &&
      isImportValidationReport(error.payload)
    ) {
      return normalizeImportReport(error.payload, options?.dryRun === true);
    }
    throw error;
  }
}
