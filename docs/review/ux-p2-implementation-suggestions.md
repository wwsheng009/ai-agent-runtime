# UX-06/UX-07/UX-08 实施建议报告

**生成时间**: 2026-10-29  
**关联版本**: v63 → v64  
**优先级**: P2（不阻塞 v63 发布）

---

## 一、执行摘要

本次审查评估了三个 P2 项的实施可行性与工作量：

| 项目 | 核心问题 | 预计工作量 | 风险等级 |
|---|---|---|---|
| **UX-06** 危险操作层级 | 破坏性动作视觉权重过高，缺乏二次确认 | ~2 人日 | ⚠️ 高（误触风险） |
| **UX-07** 信息层级冗余 | 重复标题/刷新按钮，技术术语暴露，无最大宽度 | ~2.25 人日 | 🟡 中（体验噪声） |
| **UX-08** 可访问性与真机回归 | 小按钮尺寸不足 44px，a11y 自动化检测缺失 | ~3.25 人日 | ⚠️ 高（合规风险） |

**总计**: 约 7.5 人日，建议下一轮迭代完成。

---

## 二、详细评估

### UX-06 危险操作层级收敛

#### 当前实现问题

通过代码审查定位到以下直接暴露的破坏性动作：

```javascript
// app.js:2462 - 令牌页
revoke.className = "btn danger sm";  // "撤销令牌"
del.className = "btn danger sm";     // "删除记录"

// app.js:2601 - MCP 挂载页
del.className = "btn danger sm";     // "删除"

// app.js:2266 - 节点离线快照
rm.className = "btn danger sm";      // "移除"

// app.js:1684-1685 - 会话卡菜单
'<button class="menu-item danger"...>关窗口</button>' +
'<button class="menu-item danger"...>移除</button>' +

// index.html:335 - 仪表盘离线面板
<button id="clear-stale" type="button" class="btn danger sm">清除全部</button>
```

#### 影响分析

- **视觉干扰**: 红色按钮在暗色主题中获得最高注意力，用户浏览时被第一眼捕捉。
- **窄屏风险**: 换行后多个红色按钮堆叠，增加误触概率。
- **确认流程缺失**: 操作前无后果说明，部分关键动作仅依赖浏览器原生 confirm。

#### 建议改法

##### 1. 列表行默认移除红色按钮

- **令牌/MCP/节点卡片**: 撤销/删除操作收进各自卡片的 `⋯` 菜单。
- **会话卡**: 「关窗口」「移除」已在 v63 进入菜单，但仍未加二次确认。
- **离线快照**: 「清除全部」移至标题右侧溢出菜单（需新增徽章显示数量）。

##### 2. 二级确认策略通用化

**设计模式**:
```javascript
// 通用确认函数
async function confirmDestructive(action) {
  const result = await showModal({
    title: `${action.title}？`,
    message: action.consequence, // 包含对象、数量、不可恢复等后果说明
    cancelText: "取消",
    confirmText: "是的，执行",
    confirmClass: "danger-outline", // 描边样式而非实心红
  });
  
  if (result.confirmed) {
    await action.execute();
    showToast(action.successMessage);
    restoreFocusTo(action.trigger); // 焦点恢复
  }
}
```

**文案规范**:
- 单条令牌撤销："撤销设备令牌 `office-laptop`？该令牌的接入权限将立即失效。"
- MCP 挂载删除："删除 MCP 挂载 `/m/anthropic-claude`？历史调用日志仍将保留。"
- 清除离线快照："清除全部 3 个离线节点快照？此操作不可恢复，相关会话将无法恢复。"

##### 3. 批量操作的独立确认页

对于「清除全部」此类高风险操作，采用两步走：
1. 点击标题溢出菜单中的「清除全部（3）」→ 进入临时确认页。
2. 确认页展示影响范围清单 + 最终执行按钮。

#### 验收标准

- [ ] 普通浏览状态下，每张卡片最多出现一个高权重动作，且不是破坏性动作。
- [ ] 危险操作至少需要“进入菜单 + 确认”两步。
- [ ] 取消确认后焦点不丢失；操作完成后有成功/失败 toast 反馈。
- [ ] 窄屏（390px）下菜单完整落在视口内，不被裁切。

---

### UX-07 信息层级冗余收敛

#### 当前实现问题

通过 DOM 结构分析发现：

```html
<!-- index.html:434-441 - 连接器页面 -->
<section class="view" data-view="connectors">
  <section class="panel">
    <div class="panel-head">
      <h2>连接器</h2>  <!-- H2 与导航同名 -->
      <span class="badge" id="connectors-count"></span>
    </div>
    <p class="connectors-hint">
      连接器把平台消息投递到在线节点的会话（Backend=gateway_node）。
      这里只展示运行归属与配置面；完整编辑器由前端切片提供。
    </p>
    <div class="toolbar">
      <button id="connectors-refresh">刷新列表</button>  <!-- 顶栏已有刷新 -->
      <a href="/api/connectors">管理接口 JSON</a>
      <a href="/api/connectors/schema">Schema</a>  <!-- 技术术语 -->
    </div>
  </section>
</section>
```

```css
/* style.css - 缺少内容最大宽度 */
.views {
  display: flex;
  flex-direction: column;
  /* max-width 缺失 → 1920px+ 下拉成长条 */
}
```

#### 影响分析

- **认知负荷**: 管理员需要先理解 `Backend=gateway_node`、`Schema` 等技术术语才能判断页面用途。
- **重复噪声**: 顶栏刷新与页面内刷新并存，H2 标题与导航项同名。
- **超宽屏问题**: 1920px/2560px 下卡片被拉伸成超长横条，文本行宽超过 80 字符，扫读成本激增。

#### 建议改法

##### 1. 统一标题层级

**目标结构**:
```html
<main class="views">
  <h1 class="page-title">aicli-gateway 控制台</h1>
  
  <section class="view" data-view="connectors">
    <h2>连接器</h2>  <!-- 页面主标题，唯一 -->
    <p class="page-desc">管理跨平台的消息推送服务（Telegram、微信、飞书等）。</p>  <!-- 描述性副标题 -->
    
    <div class="panel">
      <div class="list"></div>
    </div>
  </section>
</main>
```

##### 2. 刷新入口单一化

- **保留**: 顶栏刷新按钮作为当前视图的统一数据刷新入口。
- **移除/隐藏**: 各面板内的「刷新列表」、「刷新」按钮移入 `⋯` 菜单或完全移除。
- **例外**: 日志视图的「自动刷新」开关保留，因为这是 SSE 流的实时订阅控制。

##### 3. 技术字段降级

**当前**:
```
Backend=gateway_node  Schema  前端切片  aicli-remote-tools serve
```

**改进**:
```
使用方式：连接器负责将平台消息投递到在线节点的会话。
【高级信息】（展开后显示）
  后端归属：gateway_node
  配置接口：/api/connectors
  Schema 版本：v1.2
  数据来源：aicli-remote-tools serve
```

##### 4. 事件条限高 + 去重

**目标设计**:
- 默认单行：最多 3 chip +「还有 N 条」链接文字。
- 点击展开：滑出抽屉显示最近 10 条记录。
- 已完成事件不进入全局事件条，仅保留在会话详情页的「最近活动」。

##### 5. 内容最大宽度 CSS

```css
.views {
  display: flex;
  flex-direction: column;
  max-width: 1440px;
  margin-inline: auto;
  padding-inline: 16px;
}
```

#### 验收标准

- [ ] 任一页面不出现重复的 H1/H2 同名标题和刷新按钮。
- [ ] 首屏主文案不超过一处未解释的实现术语。
- [ ] 390px 下事件条高度不超过 48px；展开后才显示全部记录。
- [ ] 导航徽标可被屏幕阅读器读成「待处理 1 项」而不是裸数字「1」。
- [ ] 1920px 与 2560px 下正文行宽不继续增长，卡片不被拉伸成超长横条。

---

### UX-08 可访问性与真机回归

#### 已通过基础

根据源码检查与行为测试验证：

- ✅ 跳转主内容链接 (`<a href="#main" class="skip-link">`)
- ✅ `focus-visible` 样式存在（style.css:1-50）
- ✅ `prefers-reduced-motion: reduce` 媒体查询降级
- ✅ 多数按钮已有 `aria-label`
- ✅ 暗色/亮色次要文字对比度抽查均≥4.5:1
- ✅ 粗指针普通按钮≥44px

#### 仍需验证的问题

##### 1. 44×44 一致性缺口

当前样式表中小按钮定义为 40px：
```css
.btn.sm, .btn-ghost.sm {
  min-height: 40px;
  min-width: 40px;
}
```

**建议**: 提升至 44px，与 WCAG 2.1 AA 触控目标建议保持一致。

##### 2. Live region 动态计数播报风险

当前统计卡使用 `role="status"`:
```html
<div role="status" aria-live="polite" class="stat-card">
  <div class="stat-value" id="attention-count">1</div>
  <div class="stat-label">待处理</div>
</div>
```

**风险**: 每次 SSE 更新触发重新渲染时，屏幕阅读器可能朗读「待处理 1」「待处理 2」...造成打断。

**建议**: 
- 使用单一 `role="status"` 汇总播报，例如「待处理 1 项」。
- 防抖更新频率：每 2 秒最多播报一次。
- 数值变化≤1 时不播报。

##### 3. 键盘全流程未覆盖

尚未验证的键路：
- [ ] 登录页 → 密码输入 → Enter 提交 → 仪表盘
- [ ] 仪表盘 → Tab 进入会话列表 → j/k 上下切换 → Enter 打开会话
- [ ] 会话流 → Tab 进入审批按钮 → Enter 审批 → 返回列表
- [ ] 通知设置页 → 修改免打扰时段 → 离开前拦截 → 确认离开

##### 4. 跨端环境缺失测试

未验证的平台：
- iOS Safari (15+)
- Android Chrome (最新版)
- 200% 缩放下的文本回流
- 软键盘遮挡与安全区

#### 建议验收清单

建立发布前的 a11y 矩阵，附在 PR 中：

| 类别 | 条件 | 必测内容 | 工具/方法 |
|---|---|---|---|
| **键盘流程** | 仅键盘 | 登录 → 会话列表 → 打开会话 → 审批/回答 → 返回列表 → 通知设置修改并保存 | 手动 + 录屏 |
| **缩放** | 200% Zoom | 所有页面无横向滚动、文本回流、菜单可达 | Chrome DevTools 设备模拟 |
| **尺寸** | 320px | 页头、会话列表、令牌页无裁切 | 同上 |
| **主题** | 暗色/亮色/跟随系统 | 对比度、状态色、禁用态 | WebAIM Contrast Checker |
| **输入** | 屏幕阅读器 | NVDA/VoiceOver 朗读会话徽标为「待处理 1 项」而非裸数字「1」 | 实际设备 |
| **动态区域** | SSE 更新 | 待处理计数变化不抢焦点、不连续朗读 | axe-core |
| **移动端** | iOS Safari/Android Chrome | toast、软键盘、安全区、浏览器工具栏遮挡 | 真机测试 |
| **触控** | ≥44×44 | 所有可点元素达标 | manual + Lighthouse |

#### 自动化检测建议

引入 `axe-core` 进行 CI 集成：
```javascript
// scripts/verify-a11y.mjs
import puppeteer from 'puppeteer';
import axe from '@axe-core/puppeteer';

const browser = await puppeteer.launch({ headless: 'new' });
const page = await browser.newPage();
await page.goto('http://localhost:8080/console');
const results = await axe(page);

if (results.violations.length > 0) {
  console.error(`Found ${results.violations.length} accessibility violations:`);
  console.dir(results.violations, { depth: null });
  process.exit(1);
}
```

CI 配置示例（.github/workflows/a11y.yml）:
```yaml
- name: Accessibility check
  run: node scripts/verify-a11y.mjs
```

#### 验收标准

- [ ] 仅键盘可完成：登录 → 打开会话 → 审批/回答 → 返回列表。
- [ ] 200% 缩放和 320px 宽度下无内容丢失、无横向滚动。
- [ ] 亮/暗主题、正常/减少动态效果、浏览器/PWA 安装态各跑一遍。
- [ ] 动态计数更新不会抢焦点，也不会连续朗读重复数字。
- [ ] 所有可点元素触控目标≥44×44px。

---

## 三、综合实施建议

### 发布节奏

| 批次 | 内容 | 预计时间 | 阻塞 v63 发布？ |
|---|---|---|---|
| **v63 (本次)** | UX-01/02/03/04/05/09 实施完成 | 2026-10-29 | ❌ 否 |
| **v64 (下一轮)** | UX-06/07/08 核心项 | 2026-11-10 ± 1 周 | N/A |

建议 v63 按现有进度发布，P2 项不影响当前里程碑。

### 资源需求

按当前评估，P2 总计约 **7.5 人日**，可由 1 名工程师分阶段完成，或拆分为 2 名工程师并行：

- **工程师 A** (前端): UX-06 (~2 人日) + UX-07 (~2.25 人日)
- **工程师 B** (QA/前端): UX-08 (~3.25 人日) + 自动化检测框架 (~1 人日)

### 优先顺序

| 优先级 | 项目 | 理由 |
|---|---|---|
| 🔴 **最高优** | UX-06 危险操作 | 直接影响误触与安全信任，生产环境易引发投诉 |
| 🔴 **最高优** | UX-08 a11y | 合规风险，企业客户可能需要 WCAG 2.1 AA 对齐 |
| 🟡 次优 | UX-07 信息架构 | 体验打磨，无严重业务影响 |

### 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|---|---|---|---|
| UX-06 若不收敛导致误操作投诉 | 中 | 高 | 纳入发布前安全检查清单，增加二次确认覆盖率测试 |
| UX-08 缺失引发合规争议 | 低 | 高 | 优先完成基础键盘流程与 44×44 校验，自动化检测 CI 化 |
| UX-07 延期不影响核心功能 | 高 | 低 | 作为可选打磨项，可在 v65 或更晚版本完成 |

---

## 四、下一步行动

### 立即可做

1. **创建追踪 Issue**: 在仓库新建 Issue/PR，关联 UX-06/UX-07/UX-08。
2. **安排 v63 发布**: 推进 v63 到生产环境，确保 Service Worker 缓存刷新。
3. **建立 a11y 测试矩阵**: 开始真机环境录制基础视频证据。

### v64 计划准备

1. **UI 设计评审**: 确认危险操作确认弹窗 UI 规范。
2. **文案审核**: 邀请产品团队评审「后果说明」文案。
3. **开发排期**: 分配 7.5 人日至下个迭代冲刺。

---

## 五、参考材料

- [wicg/inert](https://github.com/WICG/inert) - 模态焦点恢复规范
- [WCAG 2.1 AA](https://www.w3.org/TR/WCAG21/) - 无障碍准则
- [axe-core](https://github.com/dequelabs/axe-core) - 自动化 a11y 检测
- [MDN - Live Regions](https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Attributes/aria-live)

---

**生成者**: Qoder AI Agent  
**审查日期**: 2026-10-29  
**版本基线**: v63
