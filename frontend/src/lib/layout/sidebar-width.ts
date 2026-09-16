// 左侧栏宽度契约：展开态与「只显示图标的列」收起态共用同一组常量，
// 网格模板（workspace-shell 的 `--workspace-sidebar-width`）与图标列宽度都读这里。

/** 展开态：与既有网格默认值 16rem 保持一致（回归红线：不改变展开态视觉）。 */
export const WORKSPACE_SIDEBAR_EXPANDED_WIDTH = "16rem";

/** 收起态：容纳 `size-8` 图标按钮 + 两侧 `px-2` 留白（VS Code 活动栏量级）。 */
export const WORKSPACE_SIDEBAR_COLLAPSED_WIDTH = "3.5rem";
