// P0-2：workspace-page 拆分后保留原路径（@/pages/workspace-page）的纯再导出 barrel——
// 导入路径、导出名与签名保持不变；实现见 ./workspace-page/ 下的区块模块。

export { WorkspacePage } from "./workspace-page/workspace-page-view";
