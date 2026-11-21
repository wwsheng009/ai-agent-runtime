// P3：自动化任务（Schedule）面板的表单状态入口。
//
// P0-2 行数拆分：类型/常量、校验、创建负载与编辑负载分别拆到相邻文件，
// 本文件仅保留原有公开导出名，调用方无需感知内部拆分。

export {
  createKinds,
  emptyCreateForm,
} from "./form-state-types";
export type {
  CreateFormState,
  ScheduleFormErrors,
  ScheduleFormField,
} from "./form-state-types";
export {
  localDateTimeToRfc3339,
  scheduleErrorFields,
  validateCreateForm,
} from "./form-state-validation";
export { createRequestFrom } from "./form-state-create";
export {
  editFormFromTask,
  recordFromTask,
  timingChangeFromForm,
  timingRuleChanged,
  updateRequestFrom,
} from "./form-state-edit";