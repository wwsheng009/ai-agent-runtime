// P2-1B：放大预览外壳（小窗口右上角「放大」入口 + 近满屏面板）的共享文案。
// 三个面（右侧栏 Git diff / 右侧栏文件预览 / 信息流 apply_patch 差异）共用同一个外壳，
// 本模块只放外壳自身的说法；各面入口按钮的文案写在各自模块内（git.diff.expand 等）。
export const zhWorkspacePanelsPreview = {
  eyebrow: "放大视图",
  close: "关闭放大面板",
  hint: "Esc 或点击遮罩关闭；关闭后焦点回到原窗口。",
} as const;
