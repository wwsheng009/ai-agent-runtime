# console-src：gateway 控制台的源码片段（按功能域拆分）

`console/app.js` 原本是单文件 IIFE（约 10.9k 行 / 460 KB），编辑与 review 都很难。
这里按功能域把它拆成若干**源码片段**，再由脚本拼回**单文件产物**
`console/app.js`（`go:embed` 的服务入口）。

```
console-src/00-state-api.js       状态、api/apiJSON、401 集中处理、toast
console-src/05-ui-basics.js       通用二次确认、模态与焦点、通用 DOM 助手
console-src/10-router.js          hash 路由、视图切换、深链参数
console-src/20-data-loading.js    /api/nodes /api/devices 等首屏数据加载
console-src/30-connectors.js      连接器列表渲染
console-src/31-connectors-manage.js  连接器编辑/测试/绑定/迁移
console-src/32-connectors-qr.js   连接器扫码登录
console-src/40-helpers.js         事件条、派生助手
console-src/50-sessions.js        统计卡、会话表与偏好
console-src/60-markdown.js        会话 Markdown 渲染
console-src/70-nodes.js           待处理/节点/设备渲染
console-src/80-mounts.js          MCP 挂载与令牌
console-src/85-logs.js            日志视图
console-src/90-session-view.js    会话打开/详情/模态
console-src/91-chat.js            会话流面板（历史 + 实时）
console-src/92-chat-approvals.js  审批 / 提问交互区
console-src/93-chat-statusbar.js  状态栏
console-src/94-chat-collapse.js   折叠交互
console-src/95-notify.js          注意力跟踪、presence、Web Push、原生通知、通知策略页
console-src/96-theme.js           主题
console-src/97-wiring.js          页面事件接线
console-src/98-notify-im.js       PWA 安装提示、通知设置交互、Bot 目标
console-src/99-connectors-wiring.js  连接器页接线
console-src/zz-boot.js            启动序列
console-src/manifest.json         拼装顺序（唯一权威）
```

## 为什么产物仍然是单文件

`console/app.js` 的 URL 与版本契约绑得很紧：`index.html` 的 `./app.js?v=N`、
`sw.js` 的 `CACHE`/`SHELL`、以及 `console_assets_test.go` 的
`TestConsoleAssetVersionIsConsistent`。把一个 460 KB 的文件改成运行时多文件会
同时改动缓存策略、Service Worker 预缓存列表与版本断言；当前拆分的目的是**可维护
性**，所以选择「源拆分 + 产物确定性拼装」，拼装结果与原单文件**逐字节相同**。

## 怎么改

1. 改 `console-src/` 下对应功能的片段（不要直接改 `console/app.js`）。
2. 重新生成产物：

   ```sh
   node scripts/build-gateway-console.mjs
   # 或
   make gateway-console
   ```

3. 校验（提交前 / CI）：

   ```sh
   node scripts/build-gateway-console.mjs --check
   make gateway-console-check
   ```

`TestConsoleAppMatchesSplitSources`（`cmd/gateway/console_assets_test.go`）会把
「片段拼装结果 == console/app.js」钉成显式断言：**只改 app.js 或只改片段而忘记
重建，都会让测试失败**（两个方向都兜住）。

## 新增一个功能片段

- 片段是**片段**，不是独立模块：内容处于同一个 IIFE 闭包内，共享闭包状态。
- 新片段必须从**顶层的语句边界**开始（源文件里顶层语句恰好缩进 2 个空格），
  并且在下一个顶层语句之前结束（括号深度回到 0）。不要在函数体中间切。
- 在 `manifest.json` 的 `files` 里按执行顺序插入文件名——顺序即原文件顺序，
  拼接后保持原有的顶层执行次序（脚本/事件接线依赖这个次序）。