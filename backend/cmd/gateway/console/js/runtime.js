// gateway 控制台共享运行时（console/js/*.js 的唯一公共面）。
//
// 控制台历史上是单文件 IIFE：所有顶层 function 与顶层 var 共享一个闭包作用域。
// 按功能域拆成模块后，用两个显式容器把那份作用域搬出来，使拆分成为**纯重构**：
//
//   S —— 可变状态。原顶层 `var NAME` 变成 `S.NAME`：读写都是属性访问，
//        「赋值前读到 undefined」的 var 提升语义与原文件一致。
//   G —— 函数注册表。原顶层 `function NAME` 由所属模块在**模块体最前面**注册
//        （函数声明在模块内仍会被提升），跨模块调用写 `G.NAME(...)`，等价原单闭包
//        里的函数可见性；重名函数按原语义「后声明者生效」（后注册者覆盖）。
//
// 模块之间没有 import 边，只有 `import { S, G } from "./runtime.js"`，因此模块求值
// 顺序 = 入口 app.js 的 import 顺序 = 原单文件的顶层语句顺序（顶层副作用次序不变）。
// 约束：这些模块保持 ES5 风格（var + function），不引入 let/const/箭头/class。
export const S = {};
export const G = {};

// 诊断钩子：URL 带 ?debugRuntime=1 时把共享面挂到 window.__GWRT，便于真浏览器
// 冒烟与排查"改动没落盘 / 某个模块没注册"这类问题。默认关闭，不改变运行期行为
// （同源脚本本来就能读到这些 DOM 状态，暴露面不变）。
if (typeof location !== "undefined" && /(^|[?&])debugRuntime=1(&|$)/.test(location.search || "")) {
  window.__GWRT = { S: S, G: G };
}