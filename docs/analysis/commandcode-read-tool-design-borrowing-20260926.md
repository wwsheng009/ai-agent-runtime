# CommandCode Read Tool 设计借鉴与落地分析

**分析日期：** 2026-09-26
**分析对象：** [Command Code · The Read Tool](https://commandcode.ai/docs/harness-engineering/read-tool)（harness-engineering 系列，2026-08-09 发布 / 2026-09-04 更新）
**代码基线：** `backend/` @ `e4138f63`（工作区实测，非文档推断）
**关联文档：**
- `docs/analysis/commandcode-tools-design-borrowing-20260926.md`（工具总览，本报告是其 read 工具的深挖补充）
- `docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md`
- `backend/docs/aicli/tool_output_contract.md`（L4 渲染层折叠 / 工具自持窗口契约）

> 说明：本文所有「证据」均给出 `文件:行号`，可直接跳转核对；状态口径为 **具备 / 部分 / 缺失 / 强于文档**。
> 文中 CommandCode 的数字（353k、347k、top 项价格等）来自其文章的自述，是其**模型化估算**（原文明确 "modeled estimates, directional, not measured"），引用时保留该口径。

---

## 0. TL;DR

1. **CommandCode 的 read tool 本质是一个「把文件系统编译进模型上下文」的编译器**：每一次读取都是一次 token 预算决策乘以每月 ~5000 万次调用，因此它把读取管线拆成三重天花板（2,000 行 / 128 KB / 2,000 字符每行）、恢复路径、会话账本、去重缓存、Unicode 修复、格式渲染等 30+ 项能力，并声称在 37 项能力组成的「token 效率前沿」上覆盖 98% 的可回收浪费。

2. **本项目（aicli）的 view 读链已经覆盖了 CommandCode 收益最高的几项**：行窗口（默认 400 / 上限 2000，`view.go:40-45`）、自持字节预算（32 KiB，`tool_output_budget.go:30-34`）、单行 rune 级 clamp（2000 字符，`view.go:486-509`）、1-indexed 行号（`view.go:643-655`）、续读偏移预计算（`view.go:318-328`）、EOF 是 note 不是 error（`view.go:572-576,630-637`）、流式读取（`view.go:556-640`）、图片直通与尺寸披露（`view_image.go`）、批量读取（`view.go:353-438`）、读账本 + 陈旧写拒绝（`read_ledger.go`）、编码保真（`file_encoding.go`）、输入别名/类型修复（`toolargs` + `toolexec`）。
   按 CommandCode 的价格表，价格最高的 5 项是：行窗口（~40k）、字节天花板（~30k）、unchanged dedup（~26k）、单行 clamp（~24k）、一次调用多文件（~20k）。本项目**已具备前三项中的行窗口/字节天花板/单行 clamp，多文件为部分具备（有 `files[]`，但缺聚合 cap）**；唯一完全缺失的高价项是 **unchanged-read dedup**。

3. **当前最值得做的不是抄功能清单，而是补三类结构性缺口**：
   - **P0-a 批量读没有聚合上限**：`files[]` 逐项各自 32 KiB，但合并结果不做聚合折叠，且 `skip_render_truncation` 阻止渲染层兜底 → 一次宽批量可把 N×32 KiB 灌进上下文。这正是文章 §16 "the caps have to move" 描述的坑（`view.go:353-438`；`output/tool_result_content.go:541-553`）。
   - **P0-b 特殊路径无前置拒绝**：`/dev/zero`、FIFO、Windows 设备名（`NUL`/`COM1`）没有黑名单，`os.Open` 一个 FIFO 会阻塞（`view.go:194-201,518-523`；仅风险表里出现：`bash.go:46-47`、`cmd/aicli/commands/command.go:1241`）。
   - **P0-c 大于 5 MiB 的单行没有恢复路径**：`bufio.Scanner` 在 `maxLineSize=5 MiB` 处返回 `token too long`，模型只看到一句中文错误，既不知道隐藏了多少字节，也不知道下一步做什么（`view.go:112,558`）。

4. **落地顺序建议**：Wave 1（聚合 cap / 特殊路径拒绝 / 超大单行 / 负 offset / 空文件文案）是低风险、不改变现有契约的加固；Wave 2（unchanged-read dedup / Unicode 文件名修复 / 账本语义补齐）涉及会话状态与跨工具不变量，需要按文章 §4 的三边修复方式一起做；Wave 3（文档抽取 / notebook / 图片强化 / schema 联合类型治理）成本更高、依赖外部转换器，按需推进。

---

## 1. CommandCode 文档的设计思想提炼

文章用 16 个小节 + 「合并读工具」专题讲了一个主题：**read tool 不是"读文件"，而是上下文预算的守门人**。把它的表述翻译成可复用的设计公理，共 8 条。

### 1.1 公理一：读工具是「文件系统 → 模型上下文」的编译器

- 每个决策（窗口多大、截断在哪、返回什么、怎么续读）都会被 ~5000 万次/月调用放大；成本结构是 **cost per successful read**，而不是单次调用的正确性。
- 结论：读工具的每一项能力都要问「它每百万读流量能省多少 token」，而不是「功能表上有没有勾」。文章给出的可回收总量是 **353k tokens / 1M 读流量**，CommandCode 自称覆盖 347k（98%）；第二名 Hermes 268k（76%）。
- 价格表 top：**行窗口 ~40k / 字节天花板 ~30k / unchanged dedup ~26k / 单行 clamp ~24k / 一次调用多文件 ~20k**，尾部（设备路径黑名单、confusables note）各 ~2k。

### 1.2 公理二：三重天花板缺一不可

| 天花板 | 挡住的"动物" | CommandCode 参数 |
|---|---|---|
| 行窗口 | 80,000 行的 lockfile | 2,000 行 |
| 字节预算 | 行数不多但很宽的日志 | 128 KB |
| 单行 clamp | minified bundle：一行就吃光预算 | 2,000 字符/行 |

- 关键洞见：**三个天花板各挡一种文件形状，任意去掉一个，就有一种文件形状会把整次读取变成"付了全价、什么都没得到"**，而且日志里不会留下任何痕迹。
- 单行 clamp 还必须"诚实"：截断处标记隐藏了多少内容，否则模型不知道自己漏了什么。

### 1.3 公理三：工具能返回的最贵的东西是"沉默"

- 空字符串、空结果在模型视角里和"工具坏了"不可区分 → 模型重读、放大窗口、换路径，烧掉 3 个回合才学到一句话就能说明的事。
- 因此 **每一个 dead end 都必须自带恢复路径**，且：
  - 续读偏移**预先算好**（模型不做分页算术，避免 reasoning token 与算错导致的额外回合）；
  - 这些提示**不带 `Error:` 前缀**，UI 不涂红、模型也不把它当成"需要道歉的失败"；
  - 字节截断的续读偏移要**落在最后一行上（而不是下一行）**，因为那一行是被从中间切断的——off-by-one 的续读提示会造成"静默损坏的读"，比浪费一个回合更糟。
- 示例对照：`offset=900` 越界时返回 `Note: offset 900 is beyond the end of the file (412 lines scanned). Retry with a smaller offset.`，而不是空串。

### 1.4 公理四：真正的 bug 住在「工具之间的关系」里（relational invariant）

文章最有价值的一节：`read`、`write`、`dedup` 三个互不调用的工具组合出一个死循环：

```
read（带单行 clamp，标记 partial）
  → ledger 记录 "partial view"
  → write 拒绝覆盖（因为"只读过一部分"）
  → 模型重读同一窗口
  → dedup 返回 "unchanged" 存根
  → 回到 write 拒绝 ↺ 无限循环
```

- 三方修复缺一不可：
  1. `write` 只要**账本内容与磁盘字节一致就允许覆盖**（即使视图被 clamp 标记为 partial，因为 clamp 的读已记录完整原始字节的哈希）；
  2. 真正 partial 的视图要有**准确的错误**（"Only part of this file has been read"），而不是误导性的 "has not been read yet"；
  3. dedup 在「首行开始的完整重读」场景必须让路，除非账本里已有完整视图；而且 dedup 判定要放在这个 guard **之后**，因为「命中即消费」会消耗记录。
- 方法论：字段级 schema 校验查不出这类不变量，只有观察生产流量才能发现。**落地任何"读-写-缓存"组合能力时，必须同时定义三者的交互语义。**

### 1.5 公理五：stale hit 代价巨大的缓存，必须"命中即自毁"

- 未变更文件的同窗口重读是纯浪费，返回一个短存根（stub）即可；命中条件苛刻：mtime + size + 精确 `(offset, limit)` 全一致。
- 但存根指向的是**上下文里此前的那条工具结果**——如果 compaction 吃掉了它，模型被指向一个永远看不见的地方，并且**无法通过重试逃脱**。
- 所以：**dedup 命中即消费记录**。最坏情况是浪费一个回合（下次重读拿到真实内容），而不是无界损失。原文的判词："the failure you're insuring against is unbounded. the premium is one turn."
- 附注：缓存必须带 kill-switch 环境变量。

### 1.6 公理六：模型看不见的失败，重试是工具的职责（invisible failure repair）

- **文件名层面**：macOS 截图名里的 NARROW NO-BREAK SPACE、NFD 分解、Finder 的弯引号，在终端里和 ASCII 版本渲染完全相同；模型忠实重打还是 "file not found"，靠推理永远无法收敛。
  → 失败前先重试 7 种候选拼写（窄空格↔普通空格、NFD、NFC、直引号↔弯引号、NFD+弯引号），**每个候选都重新过 workspace 边界检查**；仍失败才给 did-you-mean（substring + 有界 Levenshtein ≤2，覆盖 `AGENT.md → AGENTS.md` 这类 substring 找不到的场景）。
- **输入层面**：10 个 `file_path` 别名在 repair 层归一；数字字符串用 `Number()` 语义强转——`"2000"` 接受，`"2abc"` 拒绝而不是静默当 2，`1.5` 拒绝而不是 floor。**"静默错误的窗口"比报错更糟。**
- **路径安全层面**：`/dev/zero`、`/dev/urandom`、`/dev/stdin`、`/proc/<pid>/fd/*` 在**任何 I/O 之前**按名字拒绝；workspace 边界在 `cwd=/` 时救不了你，"read tool 挂在 /dev/zero 上"是自带的 DoS。
- 这一节的核心判词：当失败对模型不可见时，模型会用同样的错误字节永远重试；**harness engineering 就是把这些不可见失败在工具层修掉，让模型专注推理**。

### 1.7 公理七：流式与边界的正确性（chunk boundary / UTF-8 / hygiene）

- **流式、内存有界**：逐 chunk 读，窗口之前有一行 400 MB 也不会累积到内存。
- **chunk 边界处的"未知"不要猜**：行长限制恰好命中 chunk 末尾时，"还有没有更多文件"这个问题的答案此时**不存在**；此时说 "more file remains" 约有一半是假话，且每次都赔一个回合。做法是**推迟到下一个 chunk 再判断**——不知道就先不说。另外：不要 `break` 出 `for await`（会触发迭代器 `return()` 把流销毁）。
- **卫生项**（平时注意不到，踩到才疼）：BOM 剥掉；CRLF → LF；按字节截断时二分找 UTF-8 前缀，**绝不切断码点**；dedup 有 kill-switch。

### 1.8 公理八：格式感知渲染——"读"的产物是渲染结果，不是字节

| 输入 | 输出 |
|---|---|
| 图片（vision 模型） | 真图片附件；4K 截图走 JPEG 质量阶梯 95→80→60→40→20，**首次适配即停**；magic bytes 判定格式，扩展名不可信 |
| 被缩放的图片 | 明确披露缩放系数（`3024x1964 → 1092x709`，告知"显示坐标乘 2.77"），否则任何基于截图的点击坐标都自信地算错 |
| `.ipynb` | 带标签的 cell；图像输出作为真图片；>10,000 字符的 cell 输出折叠为 jq 指针 |
| `.svg` | 文本（它是 XML，模型可以编辑） |
| 其他二进制 | 一行 MIME 说明，绝不回垃圾字节 |
| `.docx/.pptx/.xlsx/.odt/.rtf/.epub/.pdf` | Markdown（标题/列表/表格保留），**与文本共用同一 offset/limit 窗口**（400 页合同 = 一个窗口） |
| 扫描版 PDF | 文本层转换会"成功返回空"，与空文件不可区分 → 明确告知"第 3/4/9 页是图片"，并给出 `pdftoppm -f 3 -l 9` 的恢复命令；下一次读把这些页渲染成图片附件 |
| 转换器依赖 | 7.8 MB 原生绑定**不随包发布**：首次读文档时安装到用户缓存，后续所有会话复用；离线/无预编译则降级为一行 MIME 说明 |

### 1.9 公理九：两个读工具是"多了一个"——每个广告的 schema 都是按请求收费的租金

- `read_file`（825 tok）+ `read_multiple_files`（860 tok）= **1,685 tok × 每个请求**，模型还要为"我该用哪个读工具"这个被发明出来的问题付费；小模型经常选错。
- 合并成一个 `read_file` 后 **1,685 → 1,147 tok（约 540 tok/请求）**，且模型自发用出了更强的形状：`["README.md", "src/**/*.ts"]` 这种"字面路径 + glob"混合的单次调用是两个旧工具都表达不了的。
- **合并必须解决三件事**（"a merge is only as good as its worst inherited input shape"）：
  1. **退役名字继续可用**：`read_multiple_files` 在查找前别名到 `read_file`，它的字段（`include/targetDirectory/gitIgnore/defaultExclude`）重命名到新 schema；且要容忍继承来的畸形形状（如 `include` 传裸字符串，因为数组包装修复不会再触发）。
  2. **预算必须跟着搬**：`read_file` 因自持有界而豁免 runner 的 25K-token 输出 cap；若合并后不把聚合 cap 搬进工具内部，一个宽 glob 会直接灌进 25 万 token。做法：**~100 KB 的工具内聚合 cap**，摘要明确说被跳过多少匹配。
  3. **沉默仍然付费**：模型会给批量读带 `limit`，旧工具当未知字段丢掉；合并后必须**明确告知"此调用无法兑现该参数"**，而不是静默返回整文件。
- **Schema 可移植性教训（Gemini 事故）**：第一版把 `file_path` 声明为 `['string','array']`（union 类型），自家测试全绿，但 Gemini 的函数调用层**直接 400 拒绝整个请求**（`any_of` 不能带兄弟字段如 `description`）。最终方案：**两个各自标量类型的字段**（`file_path` 单文件 + `paths` 多文件），`file_path` 保持 required，用不上线的 schema 侧 `requiredAlternatives` 兼容 paths-only 调用；并加一条契约测试断言"任何广告出去的属性都不是 union 类型"。
- **为什么大厂/开源都在收拢**：Cline 最激进——`read_files` 是它**唯一的**读工具，没有单数版本，每个条目带自己的 `start_line/end_line`（一次调用可以分别给 5 个文件开窗，表达力强于 CommandCode 现版）；Gemini CLI 还是两个工具；其余 harness 根本没有批量读。**多数 harness 的批量读、设备黑名单、Unicode 名重试、partial-view 账本、扫描件提示、按需转换器都只有 1/10 的覆盖率**——这些能力在 demo 里都看不见，只在长会话的第 9 小时开始收费。

### 1.10 对本文档的取舍提醒

- 文章是**带营销性质的**技术博客：353k/347k/98% 是模型化估算；能力对照表由 AI 读源码产出，文章中亦自述 "should be read that way... we expect errors in it"。
- 可以放心借用的是**设计公理与失败模式**（第 1-9 条），不是具体数字；价格表适合作为**优先级排序的启发式**，不适合作为验收指标。

---

## 2. 本项目 view 读链现状盘点

### 2.1 读链架构总览（证据：代码）

一次 `view` 调用实际经过的状态机（简化）：

```
Execute (view.go:142)
 ├─ 参数归一化/别名修复      internal/tools/argument_aliases.go:16-33 → toolargs/toolkit_args.go:35,43-49
 ├─ 路由：单文件 / 批量      view.go:152-176（file_path 单文件；files[] 批量；compact 扫描模式）
 ├─ 预检（存在性/路径修复）  toolexec/preflight.go:207-320（唯一高置信候选自动改写；歧义则拒并给候选）
 ├─ sandbox 边界             sandbox_support.go:87-105（会话 workspace 锚定）、:135-147（CheckPermission）
 ├─ 图片直通                view.go:248-250 → view_image.go:25-95（扩展名初筛 + imageprep 真实解码）
 ├─ 文本读取                view.go:518-640（UTF-8 流式；BOM/UTF-16 整文件解码 ≤8 MiB）
 │   ├─ 行窗口 offset/limit view.go:40-45（默认 400 / 上限 2000）
 │   ├─ 字节预算 32 KiB      tool_output_budget.go:30-34 + view.go:599-605（字节感知提前停止）
 │   └─ 单行 clamp 2000 字符 view.go:486-509（rune 安全 + 诚实标记 hidden 字节）
 ├─ 二进制判定               view.go:267-274,694-715（NUL 比例 >5% 拒绝）
 ├─ 结果元数据/续读偏移      view.go:280-311,318-328
 ├─ 读账本                   view.go:296-298 → read_ledger.go:99-121
 └─ 工具自持窗口             view.go:175 → tool_output_budget.go:81-104（skip_render_truncation + 声明预算）
```

写侧联动：`write`/`edit`/`multiedit` 覆写前查账本（`write.go:185-191`），陈旧则拒写并给出重新 view / 显式 `expected_sha256` 的恢复路径（`read_ledger.go:184-202`）；写入成功回写账本（`write.go:216`）。编码保真（BOM/UTF-16/原子写）见 `file_encoding.go` + `atomic_write.go`。

### 2.2 与 CommandCode read tool 的能力对照矩阵

状态口径：**具备** = 语义等价或更强；**部分** = 主干有、边界/兜底缺失；**缺失** = 未实现。

#### A. 有界窗口（三重天花板）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| 行窗口 | 具备 | `view.go:40-45`（默认 400，显式上限 2000） | 默认窗口比 CC 的 2000 更保守；显式 limit 被 clamp 到 2000 且给出 `suggested_next_offset` |
| 字节天花板（工具自持） | 具备 | `tool_output_budget.go:30-34`（32 KiB）；`view.go:599-605` | 32 KiB 比 CC 的 128 KB 更激进；工具声明预算并阻止渲染层二次折叠 |
| 单行 clamp | 具备 | `view.go:486-509`（2000 rune） | 诚实标记 `…[line truncated: N more chars]`；`hidden_bytes` 进 metadata（`view.go:302-305`） |
| 大文件之外的内存有界 | 具备 | `view.go:556-560`（Scanner 流式，buffer 64 KiB→5 MiB） | UTF-8 走流式；BOM/UTF-16 整文件解码 ≤8 MiB（`view.go:514-543`），超出转为明确报错 |
| 超大单行（>5 MiB） | **缺失** | `view.go:112,558` | Scanner `token too long` 直接成为一条无恢复路径的错误；CC 的做法是先 clamp 再决策，永远不把整行读进内存 |

#### B. 输出契约（死路自带恢复）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| 1-indexed 行前缀（cat -n 对齐） | 具备 | `view.go:643-655`（`%d: `） | 与编辑器/堆栈行号一致 |
| 截断时给续读偏移 | 具备 | `view.go:318-328` | `suggested_next_offset = offset + lines_read`；字节预算截断时该行未消费，续读会重新覆盖 |
| offset 越界是 note 不是 error | 具备 | `view.go:572-576,630-637`（`Success:true` + 说明句） | 已有 "Reached end of file: offset X ..."；文案未含 "retry with a smaller offset" 的显式动作建议 |
| 空文件说明 | 部分 | `view.go:633-636` | 返回 "offset 0 equals total lines 0"，但从措辞上不直接说 "file is empty"；与 CC 的 empty → "is empty" 相比恢复性弱半档 |
| 无 `Error:` 前缀的世界事实 | 部分 | `view.go:572-576` 是 success；但 `view.go:260-265` 的读失败仍走 `Error` + 中文包裹 | dead-end 分类尚未统一（哪些是"世界事实"、哪些是"失败"），缺一个结果契约清单 |
| 效率建议（advisory） | 具备（强于文档） | `view.go:329-351` | 首窗口截断时软提醒"用更小 limit / 继续 offset / 先批量 files[]"，CC 没有对应项 |
| 批量读的顶层 limit 语义 | **部分（沉默成本）** | `view.go:92-99,152-156` | 仅 `file_path` 会消费顶层 `offset/limit`；`files`-only 调用携带的顶层 `limit` 被**静默忽略**——正是文章 §16 "silence still costs" 的形状 |

#### C. 会话状态（读账本 / 去重）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| read-before-write 账本 | 具备 | `read_ledger.go:27-34,99-136`；`write.go:185-191` | 会话级（sessionID）path → SHA-256/size/FullRead/source/read_at，上限 4096 条 |
| 账本跟踪 partial view | 部分 | `read_ledger.go:31,117`；`view.go:297`（FullRead 计算处） | `FullRead` 字段被记录但**从未被消费**；写侧只判断"哈希是否变化 + 来源是否 view"（`shouldRefuseStaleWrite`，`read_ledger.go:158-160`），没有"只看到部分内容"的独立语义与文案 |
| unchanged-read dedup（命中即消费） | **缺失** | 全仓检索未见 view 侧去重；仅 `toolexec/preflight.go:172-203` 有空结果负缓存 | CC 的 ~26k/1M 项；且缺 kill-switch 与"compaction 吃掉引用"的自毁语义 |
| 批量读写入账本 | 具备（文档待更正） | `view.go:371` → `executeSingle` → `view.go:296-298` | 现有 `commandcode-tools-design-borrowing` §9.6 说"批量暂未写账本"，按当前代码已过期：批量中的**文本成功项**都会记录；目录自动列举（`view.go:218-243`）、图片（:248-250）、超 8 MiB（`read_ledger.go:107`）不记账 |
| 图片结果记账 | 缺失（合理） | `view.go:248-250` 提前返回 | 图片不写账本；后续 write 覆盖图片文件会走 unread 提示 |
| 跨进程/重启的账本 | 缺失（已知边界） | 进程内 `sync.Map` | 重启后退化为 `unread` 提示（现有文档 §9.6 已记录） |

#### D. 恢复与鲁棒性（不可见失败）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| did-you-mean / 邻近候选 | 具备（强于文档） | `toolexec/preflight.go:262-320`；`runeEditDistance` :2210-2250 | substring + Levenshtein + 父目录兄弟采样 + 唯一高置信自动改写（:207-235） |
| Unicode 文件名重试（7 拼写） | **缺失** | 未找到 NFD/NFC/窄空格/弯引号候选生成；`filebrowse/search.go:745-746` 明确记录"不做 NFC/NFD 归一化"的已知限制 | CC 的 1/10 项；对 macOS 产出的文件名（本机以 Windows 为主，风险较低但跨平台存在） |
| 设备路径 / 特殊文件黑名单 | **缺失** | 仅出现在风险表：`bash.go:46-47`、`cmd/aicli/commands/command.go:1241`；view 路径无任何检查 | FIFO/串口设备 `os.Open` 可阻塞（`view.go:194-201,518-523`）；Windows 的 `NUL`/`COM1`、Unix 的 `/dev/*`、`/proc/*/fd/*` 未按名拒绝 |
| 输入别名修复 | 部分（可见性更强） | `toolargs/toolkit_args.go:35`（file_path: path/file/filename/filePath，共 4 个别名）；`internal/tools/argument_aliases.go:146-175`（repair note 可见化） | 别名覆盖面小于 CC 的 10 个，但"修复对模型可见"更强：repair note 只报告规则与键、不泄漏值（§9.4）；数组项内字段别名也归一（`argument_aliases.go:70-100`） |
| 数字校验 / 强转语义（"2000" / "2abc" / 1.5） | 部分 | `toolexec/preflight.go:782-790,884-916`（整数样值判定）；强转仅 number→string 与 string→array（:1101-1123） | 拒绝语义与 CC 一致（`2abc`、1.5 不会被静默当作整数）；但**缺 string→integer 强转**：字符串 `"2000"` 能过 preflight 的 schema 判定，却会在 view 的严格 JSON 解码（`view.go:143-151`）处被拒为"参数格式无效" |
| 路径边界（workspace 锚定） | 具备 | `sandbox_support.go:87-105,135-147` | 相对路径按会话 workspace 解析；绝对路径由 sandbox 权限把关 |

#### E. 格式感知（渲染而非字节）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| 图片直通 | 具备 | `view_image.go:14-95`；注入侧 `agent/tool_result_images.go`（见 §9.7） | png/jpg/jpeg/gif；长边 1568px / 32 MiB 上限复用 `imageprep`；缩放产物内容寻址落盘 |
| 缩放披露 | 部分 | `imageprep/imageprep.go:134-144`（`已压缩 3024x1964 → 1092x709`） | 披露了前后尺寸，但没有 CC 那句 "multiply displayed coords by 2.77" 的乘数/坐标换算指引 |
| magic bytes 判定（不信任扩展名） | 部分 | `view_image.go:26-28`（扩展名初筛）→ `:32` `imageprep.Prepare`（真实解码校验） | 效果等价（假 .png 会解码失败回退），但没有显式 sniff；格式面只覆盖 4 个扩展名（无 webp/tiff/bmp） |
| 批量读中的图片注入 | 部分（已知边界） | `view.go:371` 走 executeSingle 会生成 items 元数据；注入侧只读顶层元数据 | 现有文档 §9.7 边界已记录"批量 files[] 尚未接图片直通" |
| 二进制 MIME note | 缺失 | `view.go:267-274` 直接报"疑似二进制文件，不支持显示" | CC 是"一行 MIME + 大小"，可被模型用于判断下一步（转码/下载） |
| 文档抽取（pdf/docx/pptx/xlsx/odt/rtf/epub） | **缺失** | 全仓检索无 docx/xlsx/pptx/odt/epub 处理；`filebrowse/stat.go:174` 只有扩展名→MIME 表 | 依赖外部转换器与降级策略，工作量最大的一项 |
| notebook（.ipynb） | **缺失** | 未找到 ipynb 处理 | CC：tagged cells + 图像输出 + 大 cell 折叠为 jq 指针 |
| 扫描 PDF 恢复提示 | **缺失** | 无 PDF 管线 | 依赖文档抽取先行 |

#### F. 工具面与 schema（一个工具 vs 两个）

| 能力 | 状态 | 证据 | 差异说明 |
|---|---|---|---|
| 单一文件读工具 | 具备 | toolkit 注册只有 `view`；`agent/tool_vocabulary.go:90-117` 把 `read_file/read_files/...` 记为**仅诊断**的退役名建议 | 与 CC 合并后形态一致；且没有 read_file/read_multiple_files 双 schema 租金 |
| 批量读入参 | 部分 | `view.go:67-90`：`file_path`（string）+ `files[]`（逐项 offset/limit）+ `compact` | 无 `paths` + glob 混合、无 exclude/gitignore、无 ~100 KB 聚合 cap、无 per-file 数组项默认 limit 之外的窗口控制 |
| 属性非 union 类型 | 部分 | view 自身是标量字段（良好）；但 `grep.go:251-280` 的两个 builder 派生出 10 个 `anyOf` 参数（使用点 :298,303,308-311,:496-498,:559），`toolbroker/broker.go:219-227` 的 `plan_path` 也是 anyOf | CC 的 Gemini 400 事故说明这是**按 provider 才暴露**的可移植性 bug；本仓有 Codex 侧 sanitizer（`llm/adapter/codex.go:3393+`）但缺"广告属性不得为 union"的全 provider 契约测试 |
| 退役名可执行（而非仅纠错） | 部分 | `tool_vocabulary.go:83-89` 注释：**从不重写请求**，只做诊断 | CC 是"查找前别名到 read_file 并继续执行"；本项目刻意保守（避免吞掉同名 MCP），差异需保留 |
| schema token 治理 | 部分（已有总览分析） | 见 `commandcode-tools-design-borrowing-20260926.md` §4.2 | view 描述本身较短；grep 20.4 KB 是最大头 |

### 2.3 结构性差异小结（决定"抄什么、不抄什么"）

1. **预算口径不同**：CC 128 KB / 行窗 2000；本项目 32 KiB / 默认 400、上限 2000。本项目更省，但"更省"的代价是**批量场景缺少聚合层**（见 3.1）。
2. **会话状态层薄**：账本有了，但只服务"陈旧写防护"；CC 的账本同时服务 dedup、partial-view 拒绝和"读过了什么"的语义。**FullRead 字段已存在但无消费者**，是最明显的半成品。
3. **格式面只在图片**：文档/notebook/扫描件全缺；而这一层的成本主要在外部转换器与降级策略，适合按"可选依赖 + MIME note 兜底"分阶段做。
4. **平台差异**：本项目主要面向 Windows（PowerShell 环境），macOS 特有的 NFD/窄空格问题权重较低；但 **Windows 设备名（NUL/COM1）与 Unix 特殊文件的风险更值得先堵**。

---

## 3. 落地方案（按收益/风险分 Wave）

排序依据：CommandCode 的价格表（行窗口 ~40k / 字节天花板 ~30k / dedup ~26k / 单行 clamp ~24k / 多文件 ~20k）+ 本项目已有实现的杠杆位置。
原则：**Wave 1 不改会话状态契约；Wave 2 动状态，必须同时定义 read/write/dedup 三方交互；Wave 3 引入外部依赖或 provider 面变更，单独排期。**

### Wave 1（P0：读工具自持窗口的加固，预计 2-4 天）

#### 3.1 【P0-A】批量读聚合 cap：把天花板搬进工具内部

**问题（对照文章 §16「the caps have to move」）**：
- `view files[]` 每项走 `executeSingle`，各自受 32 KiB 字节预算约束（`view.go:599-605`）；`executeBatch` 只做字符串拼接与 metadata 汇总（`view.go:390-397,432-437`），**没有任何跨项累计**；
- 结果又盖上 `skip_render_truncation`（`view.go:175` → `tool_output_budget.go:62-71`），渲染层按契约**不会再折叠**（`output/tool_result_content.go:535-553`）；
- 因此 `files` 传 20 个 200 行文件，模型可见输出的**理论上界**可达 20×~32 KiB ≈ 640 KiB（批量每项默认 200 行，`view.go:361-363`，实际通常低于此上界），一次工具调用就能吃掉绝大部分上下文。这是本项目读链上**目前最大的单点 token 泄漏**。

**设计**：
1. 在 `tool_output_budget.go` 增加 `viewBatchAggregateBudgetBytes`（建议 **96 KiB** = 3×单文件窗口；对齐 CC "聚合 cap 在工具内部、且摘要说明跳过多少"的语义，而不是照搬 100 KB 数值）。
2. `executeBatch` 逐项累加 `len(section)`：
   - 达到 cap 后**停止追加内容**，把剩余请求记为 skipped（保留 file_path 列表与原因）；
   - 在结果尾部生成一段 `batch summary`：`Read N/M files; skipped K files (aggregate window ~96 KiB); re-read skipped ones individually with a narrower offset/limit, or use compact=true for a 10-line scan first.`
   - metadata 增加 `batch_aggregate_budget_bytes` / `batch_skipped_count` / `batch_skipped_files` / `batch_bytes_emitted`，并保持 `partial_failure` 语义不变。
3. **不要**把 cap 做在 `stampToolOwnsOutputWithBudget`（那是声明，不折叠）；聚合折叠必须是真实的内容裁剪——文章明确警告"inherit the exemption without moving the ceiling"就是这个坑。
4. compact 模式（`compact=true`，每项前 10 行，`view.go:368-370`）天然在 cap 之下，无需改。

**改动点**：`view.go:353-438`（累加与摘要）、`tool_output_budget.go:30-55`（新常量与注释）、`view_test.go`。

**验证**：
- `TestViewBatchAggregateCapStopsAtBudget`：20 个各 4 KiB 文件 → 总可见字节 ≤ cap + notice，`batch_skipped_count>0`，被跳过文件出现在摘要里；
- `TestViewBatchAggregateKeepsPartialFailureSemantics`：失败项与跳过项同时存在时，`failed_count`/`skipped_count` 各自准确；
- 既有 `TestViewTool_BatchReadsReturnSuccessfulFilesAndPartialErrors`、`TestViewTool_BatchCompactMode` 全绿。

**风险**：cap 会把"一次读 4 个 plan 文件"的常见工作流变成两次调用。建议 cap 后按**剩余请求**给 `suggested_next_offset` 式的批量提示（"re-call with files[K:]"），把恢复路径一并给出（公理三）。

#### 3.2 【P0-B】特殊路径前置拒绝：任何 I/O 之前

**问题（对照文章 §14）**：
- `view.executeSingle` 只做 `IsDir` 判定（`view.go:218-243`），随后直接 `os.Stat` / `os.Open`（`view.go:194-201,518-523`）；
- Unix 上 `os.Open` 一个 FIFO 会**阻塞等待写端**（比 /dev/zero 更糟，Scanner 的 5 MiB 上限也救不了）；`/dev/zero`、`/proc/<pid>/fd/*` 会把无意义字节读进判定流程；
- Windows 上 `NUL`、`CON`、`COM1`、`\\.\` 设备命名空间语义特殊，`Stat` 结果不可信。

**设计**：新增共享守卫（建议 `backend/internal/toolkit/tools/path_guard.go`，供 view/write/edit 共用）：
1. **名字层**（在任何 stat/open 之前）：
   - Unix：`/dev/zero`、`/dev/urandom`、`/dev/stdin`、`/dev/fd/*`、`/proc/*/fd/*` 精确/前缀拒绝；
   - Windows：`NUL`、`CON`、`PRN`、`AUX`、`COM1..COM9`、`LPT1..LPT9`（大小写不敏感、忽略扩展名与尾随空格/点），以及 `\\.\`、`\\?\GLOBALROOT` 前缀；用 `filepath.VolumeName` + 段判定而非字符串匹配。
2. **类型层**（stat 之后、open 之前）：`ModeNamedPipe`、`ModeDevice`、`ModeCharDevice`、`ModeSocket` 一律拒绝；
3. 拒绝方式：`ToolResult{Success:false}` + 结构化 `error_code=TOOL_PATH_UNSUPPORTED` + `next_action`（"use ls/glob to pick a regular file"），**不进入 os.Open**；
4. 误伤控制：`/dev/null` 视实现决定（读它是合法 EOF，但读它没有信息量，建议一并拒绝并提示）；不拦普通符号链接（由 sandbox 边界决定）。

**改动点**：新 `path_guard.go`；接入 `view.go:191-201`（checkPath 之后、os.Stat 之前）；`write.go:117-141` 同理防止覆盖设备；`view_test.go` / `write_safety_test.go`。

**验证**：
- Unix（CI 容器）：`syscall.Mkfifo` 建管道 → view 立即返回结构化拒绝而不是挂住；`/dev/zero` 拒绝；
- Windows：`view NUL`、`view COM1`、`view "C:\\x\\con.txt"`（保留名）均结构化拒绝；普通 `C:\...\file.txt` 不受影响；
- 既有 sandbox 测试全绿。

#### 3.3 【P0-C】超大单行：从"报错"到"clamp + 恢复路径"

**问题**：`maxLineSize = 5 MiB`（`view.go:112`）是 Scanner 的硬上限，超过后 `bufio.Scanner` 返回 `ErrTooLong`，最终变成 `读取文件失败: bufio.Scanner: token too long`（`view.go:260-265`）。模型既看不到内容，也得不到下一步；而这类文件（压缩后的 bundle、单行 JSON 日志）恰恰是文章点名的"第三种动物"。

**设计（两步走）**：
1. **错误映射（当天可做）**：识别 `bufio.ErrTooLong`，转为结构化结果：
   - `Success:true`（这是世界事实，不是失败）或 `Success:false` + `next_action`，二者取一但要统一；建议与"line clamp"语义一致：**返回前 N 个 rune 的 clamp 内容 + hidden bytes**，metadata `long_line_exceeded_reader_max=true`、`line_bytes_ge=5MiB`；
   - 内容尾部追加：`Note: line 12 exceeds the 5 MiB reader limit; only the first 2000 characters are shown. To inspect the remainder, narrow the file with shell (Select-String / Get-Content -TotalCount) or download it.`
2. **reader 级 clamp（结构性，1-2 天）**：把 `bufio.Scanner` 换成 `bufio.Reader.ReadSlice('\n')` 循环：
   - 累计当前行字节；超过 `viewLineClampReadBytes`（如 64 KiB）后进入"丢弃剩余、只计数"模式，仍能继续读下一行；
   - 对 clamp 的行复用 `truncateLongLine` + `hidden_bytes` 记账；
   - 这样 >5 MiB 甚至 400 MB 的单行也不会被整体缓冲（对齐文章 §2 的 per-line clamp 语义与 §7 的 streaming 语义）。

**改动点**：`view.go:112,486-509,556-640`；`viewReadResult` 增 `LinesOverReaderMax`。

**验证**：`TestViewTool_SingleLineOverReaderMaxIsClampedNotFatal`（构造 8 MiB 单行）、`TestViewTool_LongLineFollowedByNormalLines`（clamp 后仍能读到后续行）、`TestViewTool_ByteBudgetStopsBeforeLineClamp`。

#### 3.4 【P1-D】负 offset 读尾（小而独立）

**设计**：`offset < 0` 表示"从倒数第 |offset| 行开始"（`-50` = 最后 50 行），对齐 CC 的 `offset=-50`。实现用 ring buffer（容量 = |offset|）流式扫描到 EOF，返回真实绝对行号（不要输出 1..N）。`offset=0` 语义不变；当前 `offset<0 → 0` 的行为（`view.go:182-184`）要移除并测试固定。
**元数据**：`tail=true`、`total_lines`、`offset_resolved`。
**验证**：`TestViewTool_NegativeOffsetReadsTail`（绝对行号断言）、`TestViewTool_NegativeOffsetBeyondFileReturnsAll`。

#### 3.5 【P1-E】空文件 / EOF 文案统一（dead-end 自带恢复）

**设计**：把 `readLines` 的三个边界（`view.go:572-576,630-637`）改成统一句式：
- 空文件：`Note: file is empty (0 lines).`（success）；
- offset 越界：`Note: offset N is beyond the end of the file (M lines). Retry with a smaller offset (0..M-1).`；
- offset 恰等于行数：`Note: offset N equals total lines M; use offset <= M-1 to read the last line.`
所有 Note 保持 `Success:true`、无 `Error:` 前缀、不涂红；`metadata.eof=true` 不变。同步检查 `view_test.go:393,503` 的既有断言，避免文案漂移导致回归。
**验证**：新增两条表驱动断言；既有 EOF 测试保持语义（可在断言里同时接受新句式的关键子串）。

### Wave 2（P1：会话状态与不可见失败修复，预计 3-5 天）

#### 3.6 【P0-F】unchanged-read dedup：命中即消费 + 与写侧不变量共存

这是 CommandCode 价格表里**我们完全缺失的最高价项（~26k/1M）**，也是风险最高的一项（文章 §4/§5 的两次事故都出在这里）。落地方案必须一次性定义三方语义。

**命中条件（严格）**：
- 同会话、同路径（规范化后的绝对路径，复用 `normalizeLedgerPath`）；
- 磁盘 `mtime + size` 与账本记录一致（账本当前只存 SHA-256/size/read_at，需要补 `mtime`；SHA-256 在 8 MiB 内可复用，但**去重判定不应重新读盘**，所以以 mtime+size 为快路径，哈希仅作陈旧写防护）；
- 精确 `(offset, limit)` 相同（含 compact 派生出的窗口）；
- 该窗口此前成功返回过完整内容（未被 clamp/字节预算截断）。

**行为**：
- 返回短存根：`unchanged: <path> offset X..Y was read at <time> and the file has not changed since; the content is already in the conversation. If it is no longer visible (e.g. after compaction), call again — the next call returns the full content.`
- **命中即消费**：消费后账本中该窗口的 dedup 资格被清除（标记 `consumed=true`），下一次同参调用返回真实内容。这是文章 §5 的核心：最坏浪费一个回合，而不是让模型永远指向一个被 compaction 吃掉的结果。
- **from-line-1 guard（文章 §4 修复 #3）**：`offset=0` 的全量重读，**只有账本已有完整视图时才允许发存根**；否则返回真实内容。原因：写侧需要"文件被完整看过"这一事实，去重不能让重读永远拿不到完整视图。
- **kill-switch**：`AICLI_VIEW_DEDUP=off|0` 关闭（`os.Getenv` 读一次或每次读，建议每次以便测试）；默认开。
- **与 compaction 的关系**：本项目有 `internal/compactruntime` / `contextmgr` 的上下文压缩；存根不能引用"可能被压缩掉的" tool result，因此恢复路径必须写进存根本身（上面那句），并且消费语义保证重试有效。
- **与写侧不变量共存**（必须与 3.8 一起做）：
  - `write` 在"账本哈希 == 磁盘哈希"时放行，即使视图被 clamp（现状已是如此：`read_ledger.go:174-179` + `write.go:185-191`）；
  - dedup 消费逻辑不得把账本记录整体删除到"unread"，否则写侧会退回误导性提示；只清除"去重资格"，保留哈希/窗口信息；
  - **测试必须覆盖三工具组合**：`read(clamped) → write(放行) → read(同窗) → dedup stub → 消费 → read(全文)`，以及 `read(partial) → write(哈希一致) → 放行`。

**改动点**：新增 `backend/internal/toolkit/tools/read_dedup.go`（或扩展 `read_ledger.go`：`fileReadRecord` 增 `ModTime`、`Windows []readWindowRecord`）；`view.go:251-298` 前查后记；`read_ledger.go` 增窗口读写 API。

**验证**：
- `TestViewDedupSameWindowReturnsStubThenConsumes`（两次调用，第二次 stub，第三次全文）；
- `TestViewDedupFromLine1RequiresFullLedgerView`（第一次 partial → offset=0 重读返回全文）；
- `TestViewDedupInvalidatedByModTime`（写盘改 mtime → 真实内容）；
- `TestViewDedupKillSwitch`（env off → 永不去重）；
- `TestViewDedupWithClampedLongLine`（clamp 窗口不参与去重）；
- 端到端：`read → write 拒 → 重读 → 不出现无限循环`（文章 §4 的死循环回归用例）。

**风险与回退**：若发现模型因存根产生误判（例如忽略 "content already in conversation" 而反复重试），优先保 kill-switch + 默认关灰度（`AICLI_VIEW_DEDUP=off` 默认、观察指标后开）——这比默认开再回滚安全。

#### 3.7 【P1-G】文件名不可见失败修复：拼写候选 + 边界复检

**设计**（对齐文章 §6，按 Windows 优先裁剪）：
1. 在 `view`（以及 write/edit 的路径解析）失败前生成有限候选：
   - 替换表：窄不换行空格 U+202F / 不换行空格 U+00A0 / 零宽字符 ↔ 普通空格；直引号 `'` `"` ↔ 弯引号 `’` `“` `”`；全角字符 ↔ 半角（可选）。**这张表的现成实现可直接复用**：`apply_patch.go:1226-1249` 的 `normalizePatchComparableLine` 已经做同类折叠（含 U+202F/U+00A0/各类空格与弯引号 → ASCII），抽成共享函数即可，避免维护两份表；
   - 大小写变体（Windows 不敏感，但跨平台路径可能来自 WSL/macOS）；
   - 完整 NFC/NFD 归一化需要 `golang.org/x/text/unicode/norm`（本仓目前不引入 x/text，见 `filebrowse/search.go:745-746`）。**建议先做不引依赖的替换表 + 大小写候选，把 NFC/NFD 列为可选依赖项**（团队若接受 x/text，可一次到位，也是 filebrowse 搜索侧的同一收益）。
2. 每个候选**必须重新过 sandbox 边界**（`checkPath`），修复不能变成逃逸通道；
3. 候选唯一命中 → 可像 `toolexec/preflight.go:207-235` 的 `path_auto_heal` 一样自动改写并在结果里说明（复用同一条 `NextAction` 语式）；
4. 候选多命中 → 拒绝并给候选列表；无候选 → 落到现有 did-you-mean（`preflight.go:262-320` + `runeEditDistance`），别重复造轮子。

**改动点**：新 `backend/internal/toolkit/tools/path_spelling.go` + `toolexec/preflight.go` 候选生成处接入（或作为 `suggestNearbyPathCandidatesForRequest` 的前置层）。

**验证**：`TestViewTool_UnicodeFilenameCandidates`（构造含 U+202F/弯引号的文件名，请求 ASCII 变体 → 自动治愈；断言边界复检被调用）、`TestPathSpellingRejectsEscapeCandidate`。

#### 3.8 【P1-H】读账本语义补齐：partial 视图说清楚，批量/图片边界固定

**问题**：`FullRead` 已记录但无消费者（`read_ledger.go:31,117`；`view.go:297`），写侧只有三级状态（fresh/unread/stale，`read_ledger.go:148-152,220-251`），没有"你只看到了 A..B 行"的准确 partial 提示。文章 §4 的修复 #2 明确指出：**误导性的 "has not been read yet" 会把模型送进小窗口重读循环**。

**设计**：
1. `fileReadRecord` 增加 `Windows []readWindowRecord{Offset,Limit,Full,ReadAt}`（或在 dedup 落地时合并为同一结构），供：partial 提示、dedup 资格、未来"读过了什么"审计三方复用；
2. 写侧提示分档（现状三级，`partial` 为拟新增档）：
   - `unread`：保持现有提示（允许覆盖）；
   - `partial`（有记录但无完整视图且哈希一致）：**放行**，但注明 `read_before_write=partial` + 已见窗口，建议先 view 全量；**不拒绝**（文章修复 #1：partial + 字节一致必须放行，否则与 clamp 组合死循环）；
   - `stale`：维持硬拒（来源 view）；
   - `fresh`：维持放行。
3. 批量 `view files[]` 已记账（`view.go:371→296-298`），把它写进现有分析文档的勘误；图片提前返回不记账的行为显式固定为契约（在 `view_image.go` 注释与测试里写明）。
4. 生命周期：账本目前是进程内 `sync.Map`，`sessionReadLedgers` 只有 `Load/LoadOrStore`（`read_ledger.go:42-51`），**没有会话结束清理路径**——单会话条目有 4096 上限，但会话条目本身会随长驻进程累积；本波顺带补一个会话结束（或空闲 TTL）清理 hook。跨进程持久化与 `sessionruntime` 会话记录挂钩属 P2，不在本波。

**验证**：`TestWriteAfterPartialViewIsAllowedWithPartialNote`、`TestWriteAfterClampedReadIsAllowed`（回归文章 §4 循环）、`TestViewBatchRecordsLedger`。

### Wave 3（P2：格式与可移植性，按需排期）

#### 3.9 【P2-A】文档抽取管线：渲染为 Markdown，共用窗口，降级为 MIME note

**设计**：
1. 定义接口（建议放 `backend/internal/toolkit/tools/document.go` 或独立 `internal/docread`）：
   ```go
   type DocumentRender struct {
       Markdown   string   // 标题/列表/表格保留
       Metadata   map[string]any // 页码/工作表/幻灯片数、转换器名、是否降级
   }
   type DocumentExtractor interface {
       Match(path string, head []byte) bool
       Render(path string) (DocumentRender, error)
   }
   ```
2. 抽取顺序：**magic bytes 优先**（`%PDF-`、`PK\x03\x04` + `[Content_Types].xml` 再细分 docx/pptx/xlsx/odt），扩展名仅兜底；`.svg` 走文本；纯二进制返回一行 MIME + 大小（消灭当前的"疑似二进制不支持显示"）。
3. **窗口复用**：渲染产物直接喂给现有 `readLines`，offset/limit/字节预算/行号语义全部继承——400 页 PDF = 一个窗口。
4. **转换器策略**：探测系统已有工具（`pdftotext`/`pandoc`/`soffice`），存在才启用；不存在时降级为 MIME note + "可用 download 拉取后用外部工具转换"（**不照搬 CC 的 7.8 MB 按需下载原生绑定**：本仓分发/离线环境约束不同）。若选纯 Go 实现，建议 `pdfcpu`/`unidoc` 评估后再定，不先引大依赖。
5. **扫描 PDF**：文本层为空时返回 `pages 3,4,9 have no text layer` + 恢复路径（下载后走图片通道/`pdftoppm`）；本仓已有图片注入链路（`agent/tool_result_images.go`），未来可让 view 直接渲染页面图片。
6. **写侧守卫**：view 的 Markdown 渲染结果**不得覆盖原文档**——在 `write`/`edit` 的类型拒绝里对 `.docx/.pptx/.xlsx/.pdf/...` 加扩展名守卫（article 明确点名的契约）；当前 `write.go:158-171` 只拦二进制，需补文档扩展名判定。

**验证**：`testdata/docread/` 小样本（文本层 PDF、扫描 PDF、docx、xlsx）；断言 Markdown 关键结构、窗口分页、降级分支、write 拒绝覆盖。

#### 3.10 【P2-B】notebook：cell 标签 + 输出折叠

- `.ipynb` 解析为 `# %% [markdown|code] cell N` 段落；`outputs` 中 `image/png` 走图片直通；文本输出 >10,000 字符折叠为 `{"jq": ".cells[3].outputs[0].text", "note": "output omitted; use shell/jq to inspect"}` 类指针。
- 依赖仅标准库 `encoding/json`，成本主要在渲染规则与测试。

#### 3.11 【P2-C】图片强化：格式嗅探、坐标乘数、批量注入

1. **显式 magic-byte sniff**：在扩展名初筛之外，用 `image.DecodeConfig` 已能拒绝假图，但建议把"真实格式"写进 metadata（当前 `view_image.go:65-70` 只是把 MIME 归一到 png/jpeg）；扩展 `imageprep` 支持 webp（`golang.org/x/image/webp`）与 bmp，先评估依赖。
2. **坐标乘数披露**：`imageprep` 的 Note 已给前后尺寸（`已压缩 3024x1964 → 1092x709`），补一句 `显示坐标 × 2.77 得到原图坐标`（比例从前后尺寸计算），对齐 CC §9 的截图点击场景。
3. **批量 items 图片注入**：注入侧目前只读顶层 metadata（现有文档 §9.7 边界）。要么把 batch 中第一张/多张图片的元数据提升到顶层列表，要么让注入侧遍历 `items[]`；建议后者（保持 items 结构语义），并加测试。

#### 3.12 【P2-D】schema 联合类型治理：把 Gemini 类 bug 挡在发布前

- 现状：`grep.go` 的两个 builder（:251-280，`anyOf` 在 :253 与 :270）派生出 10 个 union 参数（使用点 :298、:303、:308-311、:496-498、:559），`toolbroker/broker.go:219-227` 的 `plan_path` 也是 `string|array`。Codex 侧有专用 sanitizer（`llm/adapter/codex.go:3393+`，测试 `grep_test.go:1835` 固定"顶层 anyOf 被移除"），但**其他 provider 没有等价契约保障**。
- 方案（二选一）：
  1. **模型面拆字段**（CC 的结论）：`patterns`（array）+ `pattern`（string）已有先例（grep 同时具备单数与数组字段）；把 `paths/include/exclude/glob/...` 统一为"标量字段 + 复数数组字段"，在参数归一化层把两者合并。缺点是描述 token 增加，需要按 §4.2 的 schema 预算治理一起算账。
  2. **广告层转换**：保持内部单字段 union，在构建 provider 请求前统一把 union 参数展开为任一标量分支（Codex 已这么做），并为每个 provider 写转换契约测试。
- 无论选哪种，建议增加**契约测试**：遍历所有广告工具的 schema，断言不存在带兄弟字段的 `anyOf/oneOf` 属性（CC 的教训："这类 bug 只在生产环境暴露"）。

**验证**：`TestAdvertisedSchemasHaveNoUnionProperties`（toolschema/agent 侧）；`TestGrepUnionParamsRenderPerProvider`（provider 适配层）。

---

## 4. 不建议照搬 / 需要保留的差异

| CommandCode 做法 | 本项目建议 | 理由 |
|---|---|---|
| 行窗 2000 行、第二天花板 128 KB | 保持 2000 / 32 KiB，只补批量聚合 cap | 32 KiB 更省 token；差异在"批量必须自己有 cap"而不是数值对齐 |
| 7.8 MB 转换器首次使用时安装到用户缓存 | 改为"系统已有转换器探测 + MIME note 降级"，不引入按需下载 | 与离线/受控环境、可复现构建冲突；收益/成本比不高 |
| `VISION` 工具给非视觉模型 | 不做 | 与本仓 provider/model 路由和多模态注入链路耦合，收益不明确 |
| sticky 缓存 / sticky ledger | 不引入 | 文章已证明 stale hit 无界损失；本仓连 dedup 都还没有，直接上 consume-on-hit |
| 退役工具名"到达即别名执行" | 保持现状（仅诊断，不重写） | 本仓刻意保守：`read_file` 可能属于同名 MCP（`tool_vocabulary.go:83-89`） |
| 每文件 line range（Cline 形状） | 已具备（`files[].offset/limit`） | 表达力已覆盖，无需合并新工具 |
| macOS NFD/窄空格全家桶 | 先做无依赖替换表 + 大小写候选；NFC/NFD 作为可选依赖 | 本仓用户以 Windows 为主，但替换表成本低、收益清晰 |
| 行窗口拒绝空文件时的 "is empty" | 采纳（Wave 1.5） | 一句话省 1-3 个回合，属"最便宜的高价项" |

---

## 5. 量化、观测与优先级

### 5.1 优先级与收益/成本（以 CommandCode 价格为锚点，不承诺其数值）

| 项 | CC 价格锚（每 1M 读 token） | 本项目杠杆 | 实现成本 | 风险 | 建议波次 |
|---|---|---|---|---|---|
| unchanged-read dedup | ~26k | **高**（完全缺失 + 高频重复读场景） | 中 | 高（关系不变量） | Wave 2（kill-switch + 灰度） |
| 批量聚合 cap | "many files ~20k" + 防一次调用吃掉上下文 | **高** | 低 | 低 | Wave 1 |
| 超大单行 reader/clamp | per-line clamp ~24k（我们已有 clamp，缺"超 reader 上限"分支） | 中 | 中 | 低 | Wave 1 |
| 设备/特殊路径拒绝 | ~2k + DoS/挂起防护 | 中（安全） | 低 | 低 | Wave 1 |
| 空文件/EOF 恢复文案 | ~2k（"最便宜的高价项"） | 中 | 低 | 低 | Wave 1 |
| 负 offset 读尾 | 长尾 | 低-中（读日志尾部很常见） | 低 | 低 | Wave 1 |
| 文件名不可见失败修复 | 未单列价格，属"看不见的失败" | 中 | 中 | 中 | Wave 2 |
| 账本 partial 语义 | 属于 §4 关系不变量 | 中（防重读循环） | 低 | 中 | Wave 2 |
| 文档抽取 / notebook | 长尾能力，演示价值高 | 高（数据/合同类工作流） | 高 | 中 | Wave 3 |
| 图片强化（乘数/批量/格式） | 已有图片直通的补齐 | 中 | 低-中 | 低 | Wave 3 |
| schema union 治理 | 可移植性（防整请求 400） | 中（按 provider） | 中 | 中 | Wave 3 |

### 5.2 观测埋点（先有度量再做优化）

已有：`view` 已在 `observability.RecordToolOutputBytes`（原始窗口字节，`view.go:307`）与 `RecordToolOutputTruncation`（截断原因：bytes/lines，`view.go:594,603,620`）上打了点。

建议新增（命名仅供参考）：
- `view_dedup_hit_total` / `view_dedup_consumed_total` / `view_dedup_bypass_reason`（未完整视图 / mtime 变化 / kill-switch）；
- `view_batch_aggregate_skipped_files` / `view_batch_aggregate_skipped_bytes`；
- `view_device_path_refused_total{kind}`（unix-device / windows-device / fifo / proc-fd）；
- `view_long_line_clamped_total` / `view_reader_max_exceeded_total`；
- `view_eof_note_total{empty|beyond|equals}`；
- `view_tail_read_total`（负 offset 使用率）；
- `view_path_spelling_healed_total{variant}`。

用现有 usage ledger（`internal/usageledger`、`internal/usageanalytics`）聚合"读调用次数 / 去重率 / 平均窗口字节 / 重复读比例"，为 Wave 2 的 dedup 灰度提供开关依据。

### 5.3 验收指标（建议）

1. **不回归**：`view` 单文件默认窗口的可见字节始终 ≤ 32 KiB + 提示；批量可见字节 ≤ 聚合 cap + 提示（新增契约测试固定）。
2. **重复读下降**：开启 dedup 后，同会话同文件同窗口的重复读请求中，真实内容返回次数显著下降（以 `view_dedup_consumed_total` 为分子）；且不出现"存根后模型重试仍拿不到内容"的 case（消费语义保证）。
3. **不可见失败清零**：FIFO/设备名/超长单行三类输入均返回结构化、带恢复路径的结果，而不是挂起或裸错误。

---

## 6. 验证计划

### 6.1 单元 / 契约测试（`cd backend`）

```powershell
# 读链核心
go test ./internal/toolkit/tools/ -run 'View'
# 渲染层契约（skip_render_truncation / 声明预算）
go test ./internal/output/ -run 'Truncat|Budget'
# 预检与路径修复
go test ./internal/toolexec/ -run 'Path|Preflight'
# 写侧不变量（读账本 / 陈旧写 / partial）
go test ./internal/toolkit/tools/ -run 'Write|Ledger|Partial'
# schema 可移植性（新增）
go test ./internal/toolschema/ ./internal/agent/ -run 'Schema|Union'
```

新增测试清单（与各节对应）：
- 3.1 `TestViewBatchAggregateCapStopsAtBudget`、`TestViewBatchAggregateKeepsPartialFailureSemantics`；
- 3.2 `TestViewRejectsDevicePathsBeforeIO`（Unix FIFO/`/dev/zero`；Windows `NUL`/`COM1`/保留名）；
- 3.3 `TestViewTool_SingleLineOverReaderMaxIsClampedNotFatal`、`TestViewTool_LongLineFollowedByNormalLines`；
- 3.4 `TestViewTool_NegativeOffsetReadsTail`；
- 3.5 `TestViewTool_EmptyFileNote`、`TestViewTool_OffsetBeyondEOFIncludesRetryHint`；
- 3.6 `TestViewDedupSameWindowReturnsStubThenConsumes`、`TestViewDedupFromLine1RequiresFullLedgerView`、`TestViewDedupInvalidatedByModTime`、`TestViewDedupKillSwitch`、`TestReadWriteDedupNoDeadlock`（端到端循环回归）；
- 3.7 `TestViewTool_UnicodeFilenameCandidates`、`TestPathSpellingRejectsEscapeCandidate`；
- 3.8 `TestWriteAfterPartialViewIsAllowedWithPartialNote`、`TestViewBatchRecordsLedger`；
- 3.9-3.12 文档/notebook/图片/union 各自契约测试。

### 6.2 手工 e2e（建议在 Windows 与 Linux CI 各跑一遍）

1. **批量上限**：构造 20 个 200 行文件，一次 `view files[]` → 观察结果被聚合 cap 截断、摘要给出跳过数量与恢复建议；
2. **特殊路径**：Windows `view NUL` / `COM1` / `con.txt`；Linux `mkfifo /tmp/p; view /tmp/p`、`view /dev/zero` → 均为结构化拒绝、无挂起；
3. **长行**：`python -c "open('big.txt','w').write('x'*20_000_000)"` → view 返回 clamp 内容 + hidden bytes + 恢复路径；
4. **dedup**：同一 `view` 调用重复三次 → 第二次 stub、第三次（消费后）全文；中途 `write` 修改 → 立即失效；
5. **读-写-读循环**：对含超长行的文件 `view`（被 clamp）→ `write` 覆盖 → 不应出现"拒写→重读→不变"循环；
6. **长会话**：在一次真实编码会话中统计读调用、重复读、截断次数，对比 Wave 2 前后。

### 6.3 兼容性注意

- `view` 的错误/metadata 字段是**模型面契约**：新增字段可以，改名/删除要走兼容期（`error_code`、`next_action`、`suggested_next_offset`、`is_truncated`、`eof` 等已被测试与提示词引用）。
- 新增的 Note 文案要避免 `Error:` 前缀与红色语义（文章 §3），并保持"世界事实"与"失败"的既有分界。
- 批量 cap 上线后，若模型仍习惯一次 `files[]` 读全部计划文件，应在系统提示/工具描述里同步更新引导（描述改动会带来每请求 token 成本，按 §4.2 的预算治理一并评估）。

---

## 附录 A：CommandCode read tool 能力对照全表（37 项）

状态口径：**具备**（语义等价/更强）、**部分**、**缺失**、**不适用/不照搬**。CC 取值来自其文章对照表（2026-09-04 版）。

| # | 能力 | CC 取值 | 本项目状态 | 证据 / 落点 |
|---|---|---|---|---|
| 1 | 默认行窗口 | 2,000 行 | 具备 | `view.go:40-45`（默认 400、上限 2000） |
| 2 | 第二天花板 | 128 KB | 具备 | `tool_output_budget.go:30-34`（32 KiB，更保守） |
| 3 | 单行 clamp | 2,000 字符 | 具备 | `view.go:486-509` + `hidden_bytes` |
| 4 | 1-indexed 行前缀 | 是 | 具备 | `view.go:643-655` |
| 5 | 截断时的续读偏移 | 是 | 具备 | `view.go:318-328` |
| 6 | 空文件说明 | 是 | 部分 | `view.go:630-637`（Wave 1.5 统一文案） |
| 7 | offset 越界是 note | 是 | 具备 | `view.go:572-576` |
| 8 | 流式、内存有界 | 是 | 具备 | `view.go:556-560`（Scanner；BOM/UTF-16 整文件 ≤8 MiB） |
| 9 | chunk 边界延迟截断判断 | 是 | 不适用（已由 Scanner 语义覆盖） | `view.go:617-628`（limit 后再 Scan 一次确定 EOF） |
| 10 | unchanged-read dedup（命中即消费） | 是 | **缺失** | Wave 2.6（`read_dedup.go`） |
| 11 | read-before-write 账本 | 是 | 具备 | `read_ledger.go`；`write.go:185-191` |
| 12 | 账本跟踪 partial view | 是 | 部分 | `read_ledger.go:31,117`（FullRead 已记录未消费）→ Wave 2.8 |
| 13 | did-you-mean 建议 | 是（Levenshtein） | 具备 | `toolexec/preflight.go:262-320,2210-2250` |
| 14 | Unicode 文件名重试 | 7 种拼写 | **缺失** | Wave 2.7（替换表子集；NFC/NFD 可选依赖） |
| 15 | 内容里的 unicode confusables | 否（CC 也没有） | 缺失 | 不做（CC 自认欠 2k 行） |
| 16 | 图片送 vision | 是 | 具备 | `view_image.go:25-95` + `agent/tool_result_images.go` |
| 17 | 缩放坐标映射披露 | 是 | 部分 | `imageprep/imageprep.go:141-143`（有尺寸，无乘数）→ Wave 3.11 |
| 18 | magic bytes 判定（不信扩展名） | 是 | 部分 | `view_image.go:26-28` 初筛 + `:32` `imageprep` 真实解码；无显式 sniff → Wave 3.11 |
| 19 | notebook 渲染 | 是 | **缺失** | Wave 3.10 |
| 20 | Word → markdown | 是 | **缺失** | Wave 3.9 |
| 21 | PPT + speaker notes | 是 | **缺失** | Wave 3.9 |
| 22 | Excel → 表格 | 是 | **缺失** | Wave 3.9 |
| 23 | ODT/ODS/ODP | 是 | **缺失** | Wave 3.9 |
| 24 | RTF / EPUB | 是 | **缺失** | Wave 3.9 |
| 25 | PDF 文本层 inline | 是 | **缺失** | Wave 3.9 |
| 26 | 扫描 PDF 命名页 + 恢复命令 | 是 | **缺失** | Wave 3.9 |
| 27 | 文档走 offset/limit 窗口 | 是 | **缺失**（管线不存在） | Wave 3.9（复用 `readLines`） |
| 28 | 批量读中的文档 | 是 | **缺失** | 随 Wave 3.9 |
| 29 | 转换器首次使用时获取 | 是 | 不照搬 | 改为"系统转换器探测 + MIME 降级"（§4） |
| 30 | 一次调用读多文件 | 是（read_file 内） | 具备 | `view.go:67-90,353-438`（`files[]`） |
| 31 | 读里带 glob 模式 | 是 | 缺失（组合替代） | 可 `glob` 后 `view files[]`；如需单调用见 Wave 3 评估 |
| 32 | 批量中每文件行范围 | 否（Cline 有） | **强于 CC** | `ViewFileRequest{file_path,offset,limit}`（`view.go:135-139`） |
| 33 | 排除 + gitignore | 是 | 缺失 | 由 `glob` 的 exclude 承担（跨工具组合） |
| 34 | 匹配合计 cap（~100 KB） | 是 | **缺失** | Wave 1.1（96 KiB 聚合 cap） |
| 35 | 宽松/别名输入 | 10 别名 | 部分（可见性更强） | `toolargs/toolkit_args.go:35`；repair note `internal/tools/argument_aliases.go:146-175` |
| 36 | 负 offset 读尾 | 是 | **缺失** | Wave 1.4 |
| 37 | 设备路径黑名单 | 是 | **缺失** | Wave 1.2 |

---

## 附录 B：关键代码索引

| 文件 | 职责 | 关键位置 |
|---|---|---|
| `backend/internal/toolkit/tools/view.go` | view 工具主体：窗口/预算/clamp/批量/目录/图片钩子 | 常量 :40-56；schema :67-102；单文件 :178-313；advisory :318-351；批量 :353-438；readLines :556-640；行号 :643-655 |
| `backend/internal/toolkit/tools/tool_output_budget.go` | 工具自持窗口与"渲染层不得二次折叠"契约 | 预算常量 :30-55；stamp/declare :62-104 |
| `backend/internal/toolkit/tools/read_ledger.go` | 会话级读账本 + 陈旧写判定 | 记录结构 :27-34；写读记录 :99-136；陈旧判定 :138-180；失败文案 :184-202；metadata :220-251 |
| `backend/internal/toolkit/tools/view_image.go` | 图片直通（扩展名初筛 + imageprep 校验 + 稳定落盘） | :14-95；persist :97-116 |
| `backend/internal/toolkit/tools/file_encoding.go` | BOM/UTF-16 检测、解码、按原编码回写、二进制嗅探 | :41-67；:84-112；:114-137 |
| `backend/internal/toolkit/tools/write.go` | 写侧联动：二进制拒绝、幂等、陈旧拒写、原子写、记账 | :141-172；:175-191；:203-216 |
| `backend/internal/toolkit/tools/sandbox_support.go` | 路径解析（会话 workspace 锚定）与 sandbox 边界 | :87-105；:135-147 |
| `backend/internal/tools/argument_aliases.go` | 工具参数别名归一 + repair note | :16-33；:146-175 |
| `backend/internal/toolargs/toolkit_args.go` | 别名表（file_path: path/file/filename/filePath） | :35；:43-49 |
| `backend/internal/toolexec/preflight.go` | 路径存在性/自动治愈/候选建议/类型强转 | 自动治愈 :207-260；候选 :262-320；数字语义 :884-916；编辑距离 :2210-2250 |
| `backend/internal/output/tool_result_content.go` | 渲染层折叠与工具自持窗口的判定 | `toolTruncatedUpstream` :535-553 |
| `backend/internal/output/gateway.go` | 归档分层跟随工具声明预算 | :326-335 |
| `backend/internal/imageprep/imageprep.go` | 图片解码/缩放/质量与 Note | 默认长边 :32-34；Prepare :72-145 |
| `backend/internal/agent/tool_vocabulary.go` | 退役工具名仅诊断（read_file→view 等） | :83-117 |
| `backend/internal/toolkit/tools/grep.go` | 含 union 参数（`anyOf`）的对照样本 | builder :251-280（anyOf :253,:270）；使用点 :298,303,308-311,:496-498,:559 |
| `backend/internal/toolbroker/broker.go` | `plan_path` 的 `string|array` union | :219-227 |
| `backend/internal/toolkit/tools/view_test.go` | 读链既有测试基线（窗口/批量/截断/EOF/图片） | :16-503 |
| `backend/internal/toolkit/tools/view_image_test.go` | 图片直通/假图回退/二进制拒绝 | :32-88 |

---

## 附录 C：实施切片清单（可直接开 issue）

| 切片 | 交付物 | 主要文件 | 依赖 | 估时 |
|---|---|---|---|---|
| W1-1 批量聚合 cap | 累计裁剪 + 跳过摘要 + metadata + 测试 | `view.go`, `tool_output_budget.go`, `view_test.go` | 无 | 0.5-1d |
| W1-2 特殊路径前置拒绝 | `path_guard.go` + view/write 接入 + 跨平台测试 | 新文件, `view.go`, `write.go` | 无 | 0.5-1d |
| W1-3 超大单行恢复 | ErrTooLong 映射（先）→ Reader 级 clamp（后） | `view.go` | 无 | 1-2d |
| W1-4 负 offset 读尾 | ring buffer + 绝对行号 + 测试 | `view.go` | 无 | 0.5d |
| W1-5 EOF/空文件文案 | 统一 Note 句式 + 测试更新 | `view.go`, `view_test.go` | 无 | 0.5d |
| W2-1 unchanged dedup | `read_dedup.go` + 消费语义 + kill-switch + 三工具回归 | 新文件, `view.go`, `read_ledger.go` | W1-3（clamp 语义） | 1.5-2.5d |
| W2-2 Unicode 名修复 | 候选替换表 + 边界复检 + 自动治愈复用 | 新文件, `view.go`, `toolexec/preflight.go` | 无 | 1d |
| W2-3 账本 partial 语义 | FullRead/窗口消费 + 写侧分档提示 + 批量记账勘误 | `read_ledger.go`, `write.go`, `edit.go` | W2-1 | 1d |
| W3-1 文档抽取 | 接口 + PDF/docx/xlsx 最小实现 + 降级 + write 守卫 | 新包, `view.go`, `write.go` | 依赖评估 | 3-5d |
| W3-2 notebook | cell 渲染 + 输出折叠 | 新文件 | W3-1 接口 | 1-2d |
| W3-3 图片强化 | 乘数披露 + 批量注入 + 格式扩展 | `view_image.go`, `imageprep`, `agent/tool_result_images.go` | 无 | 1-2d |
| W3-4 union 治理 | 契约测试 + provider 转换/字段拆分 | `grep.go`, `broker.go`, 各 adapter | schema 预算治理 | 2-3d |

---

## 附：文档来源与可信度

- **一手材料**：CommandCode 官方文档页（2026-09-26 抓取，全文转纯文本后逐节核对；官方页面自述其数字为 modeled estimates，"be read that way"）。
- **代码证据**：本仓库 `backend/` 工作区实测（未运行构建；引用均为静态阅读所得的行号）。若代码随后演进，行号可能漂移，请以符号名/小节描述为准。
- **与既有文档的关系**：`commandcode-tools-design-borrowing-20260926.md` §3.2/§4.3/§9.2/§9.7 是本文的输入；本文修正了其中两处过期结论（批量读已写账本；图片直通已落地），并在 read 维度上做了深挖与量化排序。
- **未覆盖**：CommandCode 文章的 37 项价格完整明细（页面折叠块）未逐条摘录；本文只引用其公开的 top-6 价格与总量，用于排序，不作为验收承诺。
---

## 附录 D：实施状态（2026-09-26 更新）

本次已按报告 **Wave 1 全部 + Wave 2 全部** 落地，Wave 3 仍为待排期（文档抽取/notebook/图片强化/schema union 治理需要依赖评估或 provider 面变更）。所有改动在 `backend/` 工作区实测通过；Wave 1/2 的验收测试均新增并全绿。

### 已完成

| 切片 | 状态 | 代码落点 | 测试 |
|---|---|---|---|
| W1-1 批量聚合 cap + 顶层 limit 提示 | ✅ | `view.go`（`executeBatch` 累计 `emittedBytes`、跳过摘要、`attachBatchWindowMetadata`、`annotateIgnoredBatchWindow`）；`tool_output_budget.go` 新增 `viewBatchAggregateBudgetBytes = 96 KiB` | `view_read_hardening_test.go`：`TestViewBatchAggregateCapStopsAtBudget`、`TestViewBatchAggregateKeepsPartialFailureSemantics`、`TestViewBatchIgnoresTopLevelWindowWithNote` |
| W1-2 特殊路径前置拒绝 | ✅ | `path_guard.go`（名字层 + mode 层 + `devicePathRefusalError`）；`sandbox_support.go` 的 `checkPath` 中心化守卫（view/write/edit/glob/grep 共用）；`view.go`/`write.go` 各自仍有防御性检查 | `path_guard_test.go`（Windows 保留名、Unix 设备名、FIFO 跳过策略）、`device_path_deny_test.go` |
| W1-3 超大单行 reader 级 clamp | ✅ | `view.go`：`readViewLine`（256 KiB 缓冲上限 + 排空计数）、`buildViewLine`、`viewReaderClampedMarker`、`reader_clamped_lines/bytes` 元数据 | `TestViewTool_LongLineFollowedByNormalLines`、`TestViewTool_SingleLineOverReaderCapIsClampedNotFatal`（8 MiB 单行） |
| W1-4 负 offset 读尾 | ✅ | `view.go`：`readTailLines`（环形缓冲 + 从头部裁剪以保住真尾部）、`orderedTailLines`、`trimTailToByteBudget` | `TestViewTool_NegativeOffsetReadsTail`（绝对行号断言） |
| W1-5 空文件 / EOF 文案 | ✅ | `view.go`：`file is empty (0 lines)`、`equals total lines ... use offset N-1`、`beyond ... retry with a smaller offset (0..N-1)` | `TestViewTool_EmptyFileNote` + 更新后的 `TestViewTool_OffsetBeyondEOFReturnsExplicitMessage` / `OffsetAtEOFReturnsExplicitMessage` |
| W2-1 unchanged-read dedup（命中即消费） | ✅ | 新 `read_dedup.go`（mtime+size 快路径、精确窗口键、consume-on-hit、from-line-1 需账本完整视图守卫、`AICLI_VIEW_DEDUP` kill-switch）；`view.go` 读取前后接入 | `read_dedup_test.go`：stub→消费→真实内容、账本缺失时不 stub、mtime 失效、kill-switch |
| W2-2 Unicode 文件名修复 | ✅ | 新共享 `internal/pathrepair`（折叠表 + `FindSpellingMatches`）；`tools/path_spelling.go` 委托；`view.go` `healSpellingPath`（候选重过 sandbox 边界）；`toolexec/preflight.go` 折叠名评分 100，支持唯一自动治愈 | `view_spelling_heal_test.go`、`preflight_spelling_test.go`、`path_spelling_test.go` |
| W2-3 账本 partial 语义 | ✅ | `read_ledger.go`：`fileReadWindow`、`recordFileReadFromDisk` 带窗口、`sessionHasFullRead`、`staleWriteVerdict.Partial`、`staleWriteMetadata` 输出 `read_before_write=partial` + `seen_offset/seen_lines` | `read_dedup_test.go`：partial 视图允许覆盖并带 note、clamp 读后覆盖不进入拒写循环 |

批量读账本原本就被记录（`view.go:371 → executeSingle`），本次未改行为，仅补了回归测试 `TestViewBatchRecordsLedger`。

### 关键设计取舍（与报告略有出入处）

1. **dedup 与写账本分离存储**：窗口去重状态放在独立 `sessionViewDedups`，不改写账本的 `SHA-256` 语义；两表通过 `sessionHasFullRead` 建立唯一耦合点，便于 kill-switch 与回退。
2. **聚合 cap 取值 96 KiB**（3× 单文件预算），而不是照搬 CommandCode 的 ~100 KB；跳过项以 `batch_skipped_files` 明确列出，且**第一条 section 永远保留**（防"整批都被跳过"）。
3. **reader 级 clamp 的诚实标记按字节计**（`showing first N chars of a M-byte line`）：超长行只缓冲前 256 KiB，不再为了精确 rune 总数扫描隐藏部分；`hidden_bytes`/`reader_clamped_bytes` 仍精确。
4. **尾部读取受 2000 行上限与字节预算双重约束**，字节裁剪从最旧行开始，保证返回的仍是"文件的最后一段"。
5. **设备守卫中心化**：并行实施把名字层守卫放进了 `sandboxPolicy.checkPath`（对所有文件工具生效，早于 sandbox nil 检查），`view`/`write` 内保留同函数作为纵深防御；FIFO/设备/socket 由 `unsupportedFileModeReason` 在 stat 后、open 前拦截。
6. **Unicode 折叠表共享**：`internal/pathrepair` 同时服务 view 自愈与 preflight 评分，避免两层对"同名"的判断漂移。

### 验证基线（本次实测）

```text
go test ./internal/toolkit/tools/ -count=1      ok  (~35s)
go test ./internal/toolexec/ -count=1           ok
go test ./internal/output/ -count=1             ok
go test ./internal/agent/ -run 'Tool|View|Write|Read|Dedup' -count=1   ok
go test ./internal/toolbroker/ -count=1         ok
```

### 仍未实施（Wave 3）

- 文档抽取管线（pdf/docx/pptx/xlsx/odt/rtf/epub + 转换器探测 + 扫描件提示 + write 类型守卫）；
- notebook 渲染；
- 图片强化（显式 magic-byte sniff / webp / 坐标乘数披露 / 批量 items 图片注入）；
- schema union 治理与"广告属性不得为 union"的全 provider 契约测试。

以上四项依赖外部转换器评估或 provider 面变更，建议按报告 §3.9-3.12 单独排期。

## 附录 E：Wave 3 实施状态（2026-09-26 续）

Wave 3 中**不引入外部依赖/不做 provider 面大改**的四项已落地并全绿；文档抽取管线（3.9）单独推进中。

| 切片 | 状态 | 代码落点 | 测试 |
|---|---|---|---|
| 3.10 notebook | ✅ | 新 `internal/ipynb`（cell 标签、stream/execute_result/error 输出、>10000 字符折叠为 jq 指针、base64 图片解码，仅标准库）；`tools/view_notebook.go` 复用 `readLines` 窗口 + 多图持久化 + `image_paths` 契约；`view.go` 在图片判定后接入 | `ipynb/ipynb_test.go`（5 例）、`view_notebook_test.go`（标签/图片/分页/坏 JSON） |
| 3.11a 显式 magic-byte sniff | ✅ | `tools/view_image.go`：`sniffImageFormat`（png/jpeg/gif/webp/bmp/tiff）、`readViewFileHead`；扩展名不再参与放行，真实格式写入 `image_detected_format` | `TestViewImageSniffIgnoresExtension`、`TestViewImageUnsupportedFormatReturnsMIMENote`、`TestViewFakeImageFallsBackToTextPath` |
| 3.11b 坐标乘数 | ✅ | `imageprep/imageprep.go` Note 追加 `显示坐标 ×N.NN 得到原图坐标`（比例=原宽/缩放宽） | `imageprep_multiplier_test.go` |
| 3.11c 批量 items 图片注入 | ✅ | `agent/tool_result_images.go`：`collectImagePassthroughs` 遍历 `items[]`（含 JSON 解码态），并新增 `toolresult.MetadataImagePathsKey` 多图清单（notebook 输出） | `agent/tool_result_images_batch_test.go`（3 例） |
| 3.12 schema union 治理（方案 2：广告层转换） | ✅ | 新 `toolschema/unions.go` `ScalarizeUnions`（递归、优先 string 分支、$ref 保守跳过、不改内部 schema）；`llm/mcp_meta_tools_convert.go` 对所有非 codex 协议应用（ProviderWrapper/GatewayClient 共用此通道，Codex 保留自身 sanitizer） | `toolschema/unions_test.go`（5 例）、`tools/union_contract_test.go`：`TestAdvertisedSchemasHaveNoUnionProperties`（遍历全部注册工具 ×3 协议）、`TestGrepUnionParamsRenderPerProvider` |
| 3.9-2 二进制 MIME note | ✅ | `view.go binaryNoteResult`：一行 MIME（magic bytes + 扩展名兜底）+ 大小 + 恢复路径，替换"疑似二进制文件，不支持显示" | `view_binary_note_test.go` + 更新 `view_image_test.go` 旧断言 |
| 3.9-6 写侧文档护栏 | ✅ | 新 `tools/document_guard.go`（.pdf/.docx/.pptx/.xlsx/.odt/.ods/.odp/.epub/.rtf）；write/append_write/edit/multiedit 四工具在读取前拒绝 | `document_guard_test.go`（5 例） |

### 3.9 文档抽取（已完成，真实转换器冒烟待补）

- 独立包 `internal/docread`（仅标准库，覆盖率 82.3%）：`Detect`（magic bytes 优先，zip 内 `[Content_Types].xml` 细分 docx/pptx/xlsx、`mimetype` 细分 odt/epub、svg 走内建文本、`\f` 分页统计）、`Render`（`pdftotext <path> -`；`pandoc -f <fmt> -t gfm`；`soffice --headless --convert-to csv|txt` 兜底，60s ctx 超时、stderr 摘要）、`MIMENote`；`lookPath`/`runCommand` 为包级可替换变量，测试全部注入假实现。
- view 侧 `tools/view_document.go`：`docreadDetect/docreadRender` 同样可注入；渲染产物直接进 `readLines` 窗口（offset/limit/行号/字节预算全部继承），Metadata 合并 `doc_kind/doc_mime/doc_converter/doc_pages/doc_pages_without_text/doc_text_layer`；无转换器或 zip/binary 型容器降级为 MIME note（`doc_reason=no_converter|unsupported_kind`）；扫描 PDF 追加 `pages 2, 3 have no text layer` 与 `pdftoppm`/OCR 恢复路径；转换失败给出转换器错误 + download 恢复路径。
- 写侧守卫（3.9-6）已覆盖 `.pdf/.docx/.pptx/.xlsx/.odt/.ods/.odp/.epub/.rtf`，view 不再把"疑似二进制"当死路。
- 测试：`docread` 包 19 个顶层用例（细分/降级/分页/假转换器）；`view_document_test.go` 覆盖窗口分页、无转换器 note（且断言不调用渲染器）、扫描件页列表、渲染失败恢复；真实 `pdftotext/pandoc/soffice` 本机不在 PATH，建议在有 LibreOffice/Poppler 的机器上补一次冒烟（soffice pptx→txt 过滤器尤其值得实测）。

### 验证基线（本次增量）

```text
go test ./internal/toolkit/tools/ -count=1                     ok (~30s)
go test ./internal/imageprep/ ./internal/ipynb/ ./internal/toolresult/ ./internal/toolschema/ -count=1   ok
go test ./internal/llm/ -count=1                               ok (~22s)
go test ./internal/tools/ -count=1                             ok
go test ./internal/agent/ -run 'CollectImage' -count=1         ok
```

### 说明

- 3.12 采用方案 2（广告层转换）而非拆字段：内部描述符/preflight 继续接受 `string|array`，只在构建 provider 请求前统一折叠为标量分支，避免拆字段带来的 schema token 预算与参数归一化面扩大。
- Codex 路径有意不经过 `ScalarizeUnions`：其 adapter 自带 sanitizer 且有既有契约测试（`grep_test.go:1835`），双重改写没有收益。
- webp/bmp/tiff 能嗅探但当前解码链路不支持：返回真实 MIME + 转换建议，不引第三方依赖（报告要求"先评估依赖"）。

## 附录 F：代码审查记录（2026-09-26，read-tool 报告 Wave 1–3）

审查方式：两名只读审查子代理分别静态审查 Wave 1/2（view 窗口/去重/账本/路径守卫/Unicode 治愈/preflight）与 Wave 3（docread/ipynb/图片强化/schema union），父会话复核交叉面（多图契约、union 折叠调用链、BMP 嗅探、内存上限、报告一致性）并执行全量测试。**范围仅限本报告对应实现**；同仓其他实施线（`commandcode-tools` 报告、subagent 运行时 postmortem）不在本次审查内。审查未修改任何文件；下列修复均由父会话完成并附测试。

### 已修复（含回归测试）

**Wave 3（本线程实现）**

| 编号 | 问题 | 修复 |
|---|---|---|
| F2 | `ScalarizeUnions` 克隆失败时静默原地改写调用方 schema（会污染内部描述符的 union 语义） | Clone 失败即原样返回，不再别名改写 |
| F3 | union 改写：节点级 `$ref` 未跳过；同节点 `anyOf+oneOf` 只改一个；`dependencies`/`contentSchema` 未遍历 | 三处修复 + 契约测试 walker 同步 + 3 个单测 |
| F4 | 文档写守卫漏宏/旧格式；Windows 尾随点/空格与 NTFS ADS（`report.docx::$DATA`）可绕过 | 扩展名集补齐（doc/docx/docm、ppt/pptx/pptm、xls/xlsx/xlsm 等）；判定前剥离 ADS 与尾随 `" ."`；测试覆盖 13 种拼写 |
| F5 | notebook 多图 `paths[0]` 与 `render.Images[0].MIME` 可能不是同一张；重复内容图计数虚高 | `persistNotebookImages` 返回 `(Path,MIME)` 对并按路径去重，note 报实际张数 |
| F6 | `collectImagePassthroughs` 只排序 `paths` 不同步 `notes`，破坏成对下标语义 | 去掉排序，明确"声明顺序、notes 非严格配对"（消费者按 join 使用） |
| F7 | 文档/notebook 窗口缺 `byte_budget_applied`/`total_lines`/clamp 字段，tail 的 `offset` 是负数 | 新增 `viewWindowMetadata` 共享组装函数并接入两条派生路径 |
| F8 | 内层 `docread.Render` 报 `ErrUnsupported` 时降级 note 丢恢复路径 | 该分支强制 `probe.Supported=false`，MIMENote 保留 download/convert 提示 |
| F9 | 折叠输出的 jq 指针可能指向空的 `text/plain`；阈值按字节；错误信息按字节截断 | `outputTextAndPointer` 统一"文本+指针"来源；`utf8.RuneCountInString` 计数；rune 截断 |
| F10 | 内容嗅探误判（普通 JSON 含 `cells`+`nbformat`）会渲染失败；UTF-8 BOM 解析失败 | 非 `.ipynb` 的解析失败回退普通文本路径；`RenderBytes` 去 BOM |
| F12 | `update_display_data` 输出被整体丢弃；notebook 图片只解码 png/jpeg，漏 gif | 输出类型并入 display_data 分支；图片解码与持久化补 `image/gif` |
| F13 | `ImagePassthroughPathsFromMetadata` 不校验 `image_passthrough` 标志，未声明可发送的结果仍会被附加图片 | 补充清单与主路径同受标志约束 + 单测（flag/无 flag/嵌套 tool_metadata 三种形态） |
| F1（部分） | 渲染路径在窗口化前全量读内存，无上限 | notebook 64 MiB 上限；图片先按 base64 估算再解码；图片总量 64 MiB 上限；docread 转换器输出 32 MiB `boundedBuffer`。残余：docread `svg/text` 直接 API 仍 `os.ReadFile` 全量（view 不走该路径，已记录） |

**Wave 1/2（并行实现，审查后由本线程修复）**

| 编号 | 问题 | 修复 |
|---|---|---|
| B1（blocker） | `view {"offset": -MinInt}`：`readTailLines` 只做上限 clamp，`-offset` 溢出为负 → `make([]string, 负数)` panic（整数形态入参可达） | 下限 clamp 到 1；回归测试覆盖 int 与 float64 两种入参形态（float 形态在参数解析层结构化失败） |
| M1（major） | `staleWriteFailure` 文案统一建议 `expected_sha256=`，但 edit/multiedit 不支持该参数 → 模型按提示重试必然死循环 | 提示按工具裁剪：write 保留显式确认，edit/multiedit 只给 re-view 恢复路径；双向测试 |
| m12 | 空 session ID 共享 dedup 桶（跨调用串扰） | 无会话标识时跳过去重与记账；测试断言第二次调用返回真实内容 |

### 未修复（建议单独排期）

| 编号 | 问题 | 建议 |
|---|---|---|
| M2 | 账本对 >8 MiB 文件不记账：外部改动后 re-view 也无法刷新哈希，edit/multiedit 永久拒写（只剩 write/shell） | 超限文件改采样/分段哈希，或对"刚 view 过"降级为告警 |
| M3 | 保留名判定无 `runtime.GOOS` 分支：Linux/macOS 上 `src/con/readme.txt`、`aux/` 等合法路径被全工具拒绝 | 产品裁决：保留名段仅在 Windows 宿主生效（`\\?\GLOBALROOT`、`/dev/*` 各归各平台） |
| M4 | 原子 `rename` 覆盖 symlink/hard link：写符号链接会把链接替换成普通文件、真实目标未更新 | symlink 解析真实目标后原子替换；`nlink>1` 回退原地写入 |
| m5 | 批量 cap 丢弃的 section 仍计入 `succeeded`，摘要自相矛盾 | 仅真正 append 后计数，并断言 `succeeded+skipped==len(requests)` |
| m6 | 批量结果声明 32 KiB 预算、实际窗口 96 KiB，与下游 64 KiB ceiling 不一致 | 批量声明 `viewBatchAggregateBudgetBytes`，或把批量 cap 收敛到 ceiling |
| m7 | 账本单窗口：先全读后局部读会把 `FullRead` 归零（dedup 守卫放行、partial 提示只反映最后一次） | 按路径累积窗口，`FullRead` 只增不减 |
| m8 | 账本/去重两张 `sync.Map` 无会话清理或 TTL hook（报告 §3.8 明确要求） | 会话结束删除或空闲清扫 |
| m9 | 名称层漏 ADS/内嵌空格变体（`NUL:stream`、`NUL .txt`、COM¹⁻³） | 先剥 `:...` 与扩展名前空格再比对；当前 mode 层兜底不可安全绕过 open |
| m10 | UTF-16 BOM 开头的二进制绕过 binary 拒绝（`isBinaryBytes` 被 encoding 判定短路） | binary 判定独立于 BOM 检测执行 |
| m11 | 负 offset 元数据漂移：`offset_resolved` 未实现、tail 忽略 `limit`、被 clamp/裁剪时无"继续读更早"提示 | 补 `offset_resolved` 与 `windowStart-limit` 续读提示 |
| m13 | 批量路径 dedup consume 发生在聚合 cap 丢弃 section 之前（模型只看到 skipped，条目已消费） | 先预留预算再 `executeSingle`，或把消费延后到 section 真正 append |
| F11 | notebook/document 渲染路径不记账本、不参与 dedup（提前 return 位于记账/去重之前）；`.ipynb` 不在写侧文档守卫内；`doc_degraded` 恒 `false`（即使有折叠/跳过的输出） | 需契约裁决：要么显式声明"渲染读不记账"为契约并加测试（对齐图片路径做法），要么按窗口补记账；`doc_degraded` 与 `outputs_omitted`/跳图计数联动 |
| n1 | `buildMetaToolsForProtocol` 全仓无调用者（死代码），其路径不经过 `ScalarizeUnions` | 删除或接入并纳入 union 契约测试，避免"以为覆盖了 meta 广告"的假象 |

### 验证基线（审查修复后重跑）

```text
go test ./internal/toolkit/tools/ -count=1                              ok (~33s)
go test ./internal/toolschema/ ./internal/docread/ ./internal/ipynb/ \
         ./internal/agent/ ./internal/tools/ ./internal/toolexec/ ./internal/output/ -count=1   ok
go build ./...                                                          ok
go vet ./internal/toolkit/tools/                                        ok
```

新增测试：`ipynb`（BOM/指针对齐/rune 阈值/图片预算/总预算/rune 截断/update_display_data+gif 7 例）、`view_notebook`（尺寸降级/空 notebook/窗口 meta 对齐/图片去重/嗅探回退 5 例）、`view_document`（空正文/内层 Unsupported/尾读绝对 offset 3 例）、`view_image_bmp`（文本不被误判/真 BMP 2 例）、`document_guard`（尾随点+ADS+宏格式）、`toolschema`（双 union 键/dependencies+contentSchema/节点级 $ref 3 例）、`docread`（boundedBuffer 2 例）、`toolresult`（image_paths 标志门控 1 例）、`review_wave12_hardening`（B1/M1/m12 4 例）。

### 审查方法说明与残余风险

- 两个审查子代理均以**只读**方式工作（不写工作区、不跑测试）；所有修复与验证由父会话执行，避免与并行实现冲突。
- 残余风险：真实 `pdftotext/pandoc/soffice` 端到端冒烟仍缺（本机 PATH 无转换器，全部以注入假实现验证）；M2–M4 与 m5–m11、m13 未修；工作区仍在并行演进，引用请以符号名而非行号为准。
- 明确未审：`internal/toolbroker`、`internal/supervision`、`internal/runtimeserver`、`internal/background`、`internal/policy` 等属于其他报告线的改动。
