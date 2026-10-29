# UX-06 危险操作层级收敛 - 实施预研

**目标版本**: v64+  
**优先级**: P2（不阻塞 v63 发布）  
**预计工作量**: ~2 人日  

> **实施状态（2026-10-29）**：已完成，落在控制台资产 `v67`。新增 `confirmDestructive()` 与 `#confirm-modal`，令牌 / 离线快照 / 离线 MCP 危险动作进入 `⋯`，会话窗口与 MCP 令牌操作改用站内确认；`verify-console-danger.mjs` 55/55、`go test ./cmd/gateway` 通过。批量清除采用“描边按钮 + 确认 + 影响清单”，MCP 令牌保留模态框内行内确认。

---

## 一、当前问题分析

### 1.1 危险暴露点定位

通过代码审查，发现以下直接暴露的破坏性动作：

| 位置 | 元素 | 实现方式 | 确认级别 |
|---|---|---|---|
| **令牌页** | 「撤销令牌」按钮 | `.btn.danger.sm` + `window.confirm()` | 原生 confirm |
| **令牌页** | 「删除记录」按钮（已撤销） | `.btn.danger.sm` + `window.confirm()` | 原生 confirm |
| **MCP 挂载页** | 「删除」按钮（离线） | `.btn.danger.sm` + `window.confirm()` | 原生 confirm |
| **节点离线快照** | 「移除」按钮（单节点） | `.btn.danger.sm` + 无确认 | ❌ 无确认 |
| **节点离线快照** | 「清除全部」按钮（批量） | `.btn.danger.sm` + `window.confirm()` | 原生 confirm |
| **会话卡菜单** | 「关窗口」菜单项 | `.menu-item.danger` + `window.confirm()` | 原生 confirm |
| **会话卡菜单** | 「移除」菜单项 | `.menu-item.danger` + `window.confirm()` | 原生 confirm |

### 1.2 主要问题

1. **视觉权重过高**: 红色按钮在浏览时第一眼就被捕捉，窄屏换行后堆叠增加误触风险。
2. **确认流程简陋**: 全部使用原生 `window.confirm()`，无法自定义文案与样式。
3. **焦点管理缺失**: 确认后焦点未归还触发器，toast 反馈分散。
4. **部分操作无确认**: 「移除」单节点离线快照缺少二次确认。

---

## 二、设计方案

### 2.1 总体策略

采用「**渐进暴露 + 统一确认 + 优雅反馈**」三层模型：

```
┌─────────────────────────────────────────────────────┐
│ Layer 1: 列表行默认隐藏危险操作（视觉降噪）           │
│   → 收进 ⋯ 菜单 / 页面级危险区                        │
├─────────────────────────────────────────────────────┤
│ Layer 2: 统一模态确认弹窗（标准化流程）               │
│   → 可配置文案 / 描边样式 / 焦点恢复 / toast 反馈     │
├─────────────────────────────────────────────────────┤
│ Layer 3: 批量高危操作独立确认页（额外保护层）          │
│   → 影响范围清单 + 最终执行按钮                      │
└─────────────────────────────────────────────────────┘
```

### 2.2 组件设计规范

#### 2.2.1 通用确认函数

**接口定义**:
```javascript
/**
 * 显示通用二次确认模态框
 * @param {Object} options
 * @param {string} options.title - 标题（如 "撤销令牌"）
 * @param {string} options.message - 后果说明（必含对象、数量、不可恢复等）
 * @param {string} [options.cancelText="取消"]
 * @param {string} [options.confirmText="是的，执行"]
 * @param {boolean} [options.showCancel=true]
 * @param {HTMLElement|null} [options.trigger=null] - 触发按钮，用于焦点恢复
 * @returns {Promise<{confirmed: boolean, result?: any}>}
 */
async function confirmDestructive(options) { ... }
```

**文案规范示例**:
```javascript
// 单条令牌撤销
{
  title: "撤销设备令牌？",
  message: "撤销设备令牌「office-laptop」？\n该令牌的接入权限将立即失效。\n持有此令牌的 agent 将无法再注册。",
  trigger: revokeBtn
}

// MCP 挂载删除
{
  title: "删除 MCP 挂载？",
  message: "删除 MCP 挂载 `/m/anthropic-claude`？\n地址将不再保留，持有该 URL 的客户端会得到 404。\n历史调用日志仍将保留。",
  trigger: delBtn
}

// 批量清除离线快照
{
  title: "清除全部离线快照？",
  message: "清除全部 3 个离线节点快照？\n\n受影响节点:\n  • node-alpha (最后活跃：2026-10-28)\n  • node-beta (最后活跃：2026-10-27)\n  • node-gamma (最后活跃：2026-10-26)\n\n这些节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。",
  trigger: clearStaleBtn
}
```

#### 2.2.2 模态框 HTML 结构

```html
<!-- 通用模态框宿主（动态注入） -->
<div class="modal-scrim" id="confirm-modal" hidden role="dialog" aria-modal="true" aria-labelledby="confirm-title">
  <div class="modal modal-sm">
    <div class="modal-head">
      <h3 id="confirm-title">确认操作</h3>
      <button class="btn ghost sm modal-close" aria-label="关闭">×</button>
    </div>
    <div class="modal-body">
      <p id="confirm-message">后果说明文本...</p>
    </div>
    <div class="modal-actions">
      <button type="button" class="btn ghost" id="confirm-cancel">取消</button>
      <button type="button" class="btn danger-outline" id="confirm-confirm">是的，执行</button>
    </div>
  </div>
</div>
```

#### 2.2.3 CSS 样式定义

**新增 `.danger-outline` 类** (style.css):
```css
/* 描边危险样式 - 用于确认弹窗的确认按钮，避免实心红的视觉干扰 */
.btn.danger-outline {
  background: var(--bg-secondary);
  border: 2px solid var(--color-danger);
  color: var(--color-danger);
  font-weight: 500;
}

.btn.danger-outline:hover {
  background: var(--color-danger);
  color: white;
}

.btn.danger-outline:focus-visible {
  outline: 3px solid var(--focus-ring);
  outline-offset: 2px;
}

/* 禁用状态保持描边 */
.btn.danger-outline:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

**现有红色实心保留为高危批量操作**:
```css
/* .btn.danger 保持不变，用于极端高危操作（如「清空所有审计日志」） */
```

### 2.3 具体改法

#### 2.3.1 令牌页危险操作菜单整合

**目标结构**:
```text
[头像] 设备名称 [active]          签发于 2026-10-01
        记录 ID abc123 · 最近使用 昨天
        [复制标识] [⋯]
                      └─ ⋯ 菜单
                         • 复制标识（只非完整令牌）
                         • 撤销令牌  ← 新增确认
                         • 删除记录  ← 新增确认（仅已撤销）
```

**代码改造点** (`app.js:2459-2484`):

```javascript
// 移除 actions 中的红色按钮
// var actions = card.querySelector(".card-actions");
// if (!d.revoked) { ...actions.appendChild(revoke); } else { ...actions.appendChild(del); }

// 改为插入到 ⋯ 菜单（复用会话卡菜单模板）
var menuHtml = 
  '<button class="menu-item" type="button" role="menuitem" data-copy-prefix>' +
    '复制令牌标识' +
  '</button>' +
  '<button class="menu-item danger" type="button" role="menuitem" data-revoke hidden="' + d.revoked + '">' +
    (d.revoked ? '删除记录' : '撤销令牌') +
  '</button>';

// 添加事件监听
card.querySelector('[data-revoke]').addEventListener('click', function () {
  confirmDestructive({
    title: d.revoked ? '删除记录？' : '撤销令牌？',
    message: d.revoked 
      ? `彻底删除已撤销令牌「${d.name || d.id}」的记录？\n此操作不可恢复（令牌本身已失效）。`
      : `撤销令牌「${d.name || d.id}」？\n使用它的 agent 将无法再注册；撤销后可选择彻底删除记录。`,
    trigger: this
  }).then(function (result) {
    if (result.confirmed) {
      var path = '/api/devices/' + encodeURIComponent(d.id) + (d.revoked ? '?purge=1' : '');
      return api(path, { method: 'DELETE' });
    }
  }).then(function () {
    notify('令牌处理成功：' + (d.name || d.id), 'ok');
    loadDevices();
  }).catch(function (err) {
    notify('处理失败：' + err, 'error');
  });
});
```

#### 2.3.2 MCP 挂载页删除按钮整合

**改动逻辑**: 同令牌页，将「删除」按钮移入溢出菜单。

**特殊考虑**:
- MCP 挂载的删除仅在 `m.stale === true` 时可用（离线挂载）。
- 文案需强调「地址不再保留」vs「日志仍保留」。

#### 2.3.3 节点离线快照优化

**当前问题**: 整卡「移除」按钮无确认，「清除全部」红色按钮视觉嘈杂。

**目标结构**:
```text
离线快照 [溢出菜单 ⋯]
         └─ 清除全部（3）← 进入临时确认页
         └─ 导出快照

[节点卡片 1] [… 菜单]
            └─ 查看详情
            └─ 移除 ← 单个节点的移除也进菜单
```

**代码改造点** (`index.html:335` + `app.js:2263-2275`):

1. **标题旁新增溢出菜单**:
```html
<div class="panel-head">
  <h2>离线快照</h2>
  <div class="dropdown">
    <button id="offline-menu-btn" type="button" class="btn ghost sm" aria-haspopup="menu">⋯</button>
    <div id="offline-menu" class="dropdown-menu" role="menu" hidden>
      <button id="offline-clear-all" type="button" role="menuitem" class="menu-item danger">清除全部（<span id="offline-count">0</span>）</button>
    </div>
  </div>
</div>
```

2. **单节点移除进菜单**:
每个离线节点卡片的操作区改为仅显示「查看详情」，「移除」移入卡片自己的溢出菜单。

3. **批量清除进入独立确认页**:
```javascript
// app.js:6627-6635
var clearStale = el("clear-stale");
if (clearStale) {
  clearStale.addEventListener("click", function () {
    var staleCount = lastNodes.filter(function (n) { return n.stale; }).length;
    if (!staleCount) { return; }
    
    // 改用通用确认弹窗（而非 window.confirm）
    confirmDestructive({
      title: "清除全部离线快照？",
      message: "清除全部 " + staleCount + " 个离线快照节点？\n这些节点的所有会话条目会一并删除（agent 重新上线会自动重新注册）。",
      trigger: this
    }).then(function (result) {
      if (!result.confirmed) { return; }
      
      // 执行批量删除
      return api("/api/nodes?stale=1&purge=1", { method: 'DELETE' });
    }).then(function () {
      notify("已清除 " + staleCount + " 个离线快照", "ok");
      loadNodes();
    }).catch(function (err) {
      notify("清除失败：" + err, "error");
    });
  });
}
```

#### 2.3.4 会话卡菜单增加确认

**现状**: 「关窗口」「移除」已有 `window.confirm()`，但视觉上是红色菜单项。

**改进方案**:
1. 移除 `.menu-item.danger` 红色样式。
2. 改用通用确认弹窗替代 `window.confirm()`。
3. 确认后焦点归还触发按钮，toast 提示结果。

**代码改造点** (`app.js:1845-1869`):

```javascript
var windowActions = [
  { 
    selector: "[data-window-open]", 
    kind: "open-terminal", 
    done: "已排队：该节点下一个心跳会在本机新终端里打开此会话",
    confirm: null // 无需确认
  },
  { 
    selector: "[data-window-resume]", 
    kind: "resume", 
    done: "已排队：该节点下一个心跳会恢复此会话",
    confirm: null
  },
  { 
    selector: "[data-window-close]", 
    kind: "close", 
    done: "已排队：该节点下一个心跳会关闭此窗口",
    confirm: "关闭该会话在本机的窗口进程？\n（等同在窗口里敲 /exit：会话会正常保存。）"
  }
];

windowActions.forEach(function (action) {
  var btn = card.querySelector(action.selector);
  btn.addEventListener("click", function (e) {
    e.stopPropagation();
    var it = card.__item;
    if (!it || btn.disabled) { return; }
    
    if (action.confirm) {
      confirmDestructive({
        title: "关闭窗口进程？",
        message: action.confirm,
        trigger: btn
      }).then(function (result) {
        if (!result.confirmed) { return; }
        requestWindowAction(it.node, it.session, action.kind, action.done);
        showToast(action.done.replace("已排队：", "排队成功！"), "ok");
      });
    } else {
      requestWindowAction(it.node, it.session, action.kind, action.done);
      showToast(action.done, "info");
    }
  });
});

// 移除按钮同理
var rmBtn = card.querySelector("[data-remove]");
rmBtn.addEventListener("click", function (e) {
  e.stopPropagation();
  var it = card.__item;
  if (!it) { return; }
  
  confirmDestructive({
    title: "从注册表移除会话？",
    message: "从注册表移除该会话条目？\n（仅清除网关侧快照，不影响 agent 上的会话数据。）",
    trigger: this
  }).then(function (result) {
    if (!result.confirmed) { return; }
    return api("/api/nodes/" + encodeURIComponent(it.node.node_id) +
                "/sessions/" + encodeURIComponent(it.session.session_id), { method: "DELETE" });
  }).then(function () {
    showToast("会话条目已移除", "ok");
    loadNodes();
  }).catch(function (err) {
    notify("移除失败：" + err, "error");
  });
});
```

---

## 三、实施计划

### 3.1 任务分解

| 编号 | 任务 | 预计时间 | 依赖 | 验收标准 |
|---|---|---|---|---|
| **UX-06-01** | 设计通用确认组件 `confirmDestructive()` | 0.5 人日 | — | jsdom 测试通过 |
| **UX-06-02** | 令牌页危险操作菜单整合 | 0.25 人日 | UX-06-01 | ⋯ 菜单可见，确认弹窗正常 |
| **UX-06-03** | MCP 挂载页删除按钮整合 | 0.25 人日 | UX-06-01 | 同上 |
| **UX-06-04** | 节点离线快照优化（标题菜单 + 单卡菜单） | 0.5 人日 | UX-06-01 | 批量/单卡确认分离 |
| **UX-06-05** | 会话卡菜单改用通用确认 + 移除红色样式 | 0.25 人日 | UX-06-01 | 焦点/ toast 正常 |
| **UX-06-06** | danger-outline CSS 样式定义 | 0.25 人日 | — | 暗色/亮色对比度达标 |
| **UX-06-07** | 补充 jsdom 行为测试 | 0.5 人日 | 全部完成 | 15+ 测试项通过 |
| **UX-06-08** | Go 资产测试更新 | 0.25 人日 | UX-06-07 | `go test ./cmd/gateway` 通过 |
| **总计** | — | **~2.75 人日** | — | — |

### 3.2 开发顺序

```
Day 1 上午: UX-06-01 通用确认组件设计与实现
Day 1 下午: UX-06-02 令牌页改造 + UX-06-03 MCP 页改造

Day 2 上午: UX-06-04 离线快照优化
Day 2 下午: UX-06-05 会话卡改造 + UX-06-06 CSS 样式

Day 3 上午: UX-06-07 jsdom 行为测试
Day 3 下午: UX-06-08 Go 测试 + 文档更新 + Code Review
```

### 3.3 回归测试矩阵

| 场景 | 浏览器 | 必测内容 |
|---|---|---|
| **令牌页** | Chrome/Edge/Firefox | ⋯ 菜单展开 → 点击「撤销令牌」→ 确认弹窗出现 → 输入正确设备名 → 确认 → toast 提示成功 |
| **MCP 页** | Chrome/Edge | 离线挂载卡片 ⋯ 菜单 → 「删除」→ 确认弹窗 → 取消 → 菜单关闭且卡片仍在 |
| **离线快照** | Chrome | 标题「清除全部（3）」→ 确认弹窗展示 3 个节点列表 → 确认 → 列表清空 |
| **会话卡** | Chrome | 卡片 ⋯ 菜单 → 「关窗口」→ 确认弹窗 → 确认 → toast「排队成功」→ 返回列表看徽章数不变 |
| **窄屏** | 390px | 所有 ⋯ 菜单完整落在视口内，不被裁切 |
| **键盘** | 仅键盘 | Tab → Enter 打开菜单 → ↓↑方向键移动焦点 → Escape 关闭菜单并返回触发器 |
| **屏幕阅读器** | NVDA/VoiceOver | 「撤销令牌」按钮读作「撤销令牌，按钮」；确认弹窗读作「模态对话框，撤销令牌，后果说明，取消按钮，是的执行按钮」 |

---

## 四、风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| 通用确认组件与现有模态框冲突 | 低 | 中 | 采用独立 `#confirm-modal` 宿主，不与 `#token-modal` 等混用 |
| 窄屏下确认弹窗被遮挡 | 中 | 低 | 弹窗限制最大高度 + 滚动，底部留出安全区 |
| 焦点管理异常导致卡死 | 低 | 高 | 严格测试 Escape/Tab/Enter 路径，增加 `document.activeElement` 校验 |
| 文案过长导致排版混乱 | 中 | 低 | 文案模板固定格式，超长时用「还有 N 项」省略 |

---

## 五、验收 Checklist

- [ ] **功能验收**
  - [ ] 令牌页撤销/删除均进 ⋯ 菜单，且有二次确认
  - [ ] MCP 页删除进 ⋯ 菜单，且有二次确认
  - [ ] 离线快照「移除」进单卡 ⋯ 菜单，「清除全部」进标题菜单
  - [ ] 会话卡「关窗口」「移除」进 ⋯ 菜单，均有二次确认
  - [ ] 所有确认后焦点归还触发器，toast 提示结果

- [ ] **样式验收**
  - [ ] 确认弹窗使用 `.danger-outline` 描边样式，非实心红
  - [ ] ⋯ 菜单移除 `.menu-item.danger` 红色样式
  - [ ] 暗色/亮色主题下对比度≥4.5:1

- [ ] **键盘验收**
  - [ ] Tab 顺序合理
  - [ ] Enter/Space 激活按钮
  - [ ] Escape 关闭菜单/弹窗并归还焦点

- [ ] **屏幕阅读器验收**
  - [ ] ARIA 标签完整
  - [ ] 动态内容变化有 live region 播报（防重复）

- [ ] **自动化测试**
  - [ ] jsdom 行为测试 15+ 项全部通过
  - [ ] Go 资产测试通过

---

## 六、参考材料

- [wicg/inert](https://github.com/WICG/inert) - 模态焦点恢复规范
- [MDN - Dialog Modal](https://developer.mozilla.org/en-US/docs/Web/HTML/Element/dialog)
- [WCAG 2.1 AA - 4.1.3 Status Messages](https://www.w3.org/TR/WCAG21/#status-messages)

---

**预研日期**: 2026-10-29  
**负责人**: TBD  
**下一步**: 启动 Day 1 实施（通用确认组件 + 令牌页改造）
