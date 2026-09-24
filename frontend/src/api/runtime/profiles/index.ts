// Runtime profile API barrel（Batch 8 §10.5 契约）。
//
// 对外面 = 拆分前的原公共导出（URL 构造 9 个 + 端点 13 个 + 2 个归一化/错误助手），
// 目录内 asRecord/read* 等内部工具不在此再导出；调用方一律从本模块导入。

export {
  applyRuntimeProfile,
  createRuntimeProfile,
  deleteRuntimeProfile,
  duplicateRuntimeProfile,
  moveRuntimeProfile,
  renameRuntimeProfile,
  setDefaultRuntimeProfile,
  updateRuntimeProfile,
} from "./mutations";
export { normalizeProfileView, readProfileDeleteBlockingReferences } from "./normalize";
export {
  normalizeSessionProfileSwitchReport,
  setSessionProfile,
  type SessionProfileSwitchChanged,
  type SessionProfileSwitchReport,
  type SessionProfileSwitchResponse,
  type SetSessionProfileOptions,
} from "./session-switch";
export {
  getRuntimeProfile,
  listRuntimeProfileReferences,
  listRuntimeProfiles,
  previewRuntimeProfile,
  validateRuntimeProfile,
} from "./queries";
export {
  buildRuntimeProfileApplyUrl,
  buildRuntimeProfileDefaultUrl,
  buildRuntimeProfileDuplicateUrl,
  buildRuntimeProfileExportUrl,
  buildRuntimeProfileImportUrl,
  buildRuntimeProfileMoveUrl,
  buildRuntimeProfilePreviewUrl,
  buildRuntimeProfileReferencesUrl,
  buildRuntimeProfileRenameUrl,
  buildRuntimeProfileUrl,
  buildRuntimeProfileValidateUrl,
} from "./url";
export { exportRuntimeProfile, importRuntimeProfile } from "./transfer";
