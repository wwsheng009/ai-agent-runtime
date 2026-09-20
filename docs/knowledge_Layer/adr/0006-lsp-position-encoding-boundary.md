# ADR-0006: LSP 位置编码转换边界与缓存键

- **Status**: Proposed
- **Date**: 2026-09-20
- **Deciders**: 项目 owner
- **Gate**: `Phase4-start`
- **Reversibility**: moderate（新增一列 + 转换函数；无对外协议变更，无历史数据）
- **Supersedes**: 澄清 `03` §5.3 的三条规则，并补齐 `03` §5.2 的 `lsp_diagnostics` / `lsp_servers` 列定义
- **Related**: `03_agent_harness_supplement.md` §5.1（L491–508）、§5.2（L512–560）、§5.3（L562–568）、§5.4（L674）、L1275；`supplement/05` 附录 F；`04` §4.3 推迟清单

---

## 1. Context

### 1.1 已核实的证据

**证据 1 — `03` §5.3 只给了三条规则，没有边界定义。**

`03` §5.3（L564–568）：

```text
内部统一使用 UTF-8 byte offset + line/column。
与 LSP 交互时转换为 UTF-16 code unit。
所有缓存键必须包含 document_version。
```

**证据 2 — `03` §5.1 把"UTF-16 / byte offset 统一"列为必须补充项。**

`03` §5.1（L491–508）的清单中明确列有 `UTF-16 / byte offset 统一`。
说明它是**已识别但未定义**的缺口，不是遗漏。

**证据 3 — `03` 的 LSP 表没有编码标记。**

`03` §5.2（L546–559）：

```sql
CREATE TABLE lsp_diagnostics (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    severity TEXT,
    message TEXT,
    source TEXT,
    start_line INTEGER,
    start_column INTEGER,
    end_line INTEGER,
    end_column INTEGER,
    document_version INTEGER,
    created_at INTEGER NOT NULL
);
```

`start_column` / `end_column` **没有配套的编码字段**。
`lsp_servers`（L513–528）同样只有 `capabilities_json`，
**没有记录协商后的 `positionEncoding`**。

即：按现有 DDL，一条诊断一旦落库，**消费方无法判断该列是 UTF-16 code unit 还是 byte**。

**证据 4 — `supplement/05` 附录 F 已承认这是未验证的高风险点。**

> Windows 进程树、UTF-16 position 映射、`.cmd` shim 三项是**已知高风险点**，但未做原型验证。

**证据 5 — LSP 协议本身允许协商，且默认不是 UTF-8。**

LSP 3.17 引入 `general.positionEncodings`：client 在 `initialize` 中列出可接受编码，
server 选一个并在 `InitializeResult.capabilities.positionEncoding` 返回。
**未协商时默认为 `utf-16`。**

因此"内部 UTF-8 / 边界 UTF-16"这一假设**只在 server 不支持 utf-8 时成立**，
不能当成协议常量写死。

### 1.2 问题陈述

四类输入会让"UTF-8 字节 / UTF-16 code unit / 码点"三者全部不等：

| 输入 | UTF-8 bytes | UTF-16 units | 码点 |
|---|---|---|---|
| `a` | 1 | 1 | 1 |
| `中` | 3 | 1 | 1 |
| `😀`（U+1F600） | 4 | 2（代理对） | 1 |
| `e` + U+0301（组合） | 3 | 2 | 2 |

若不做显式转换与标记，会出现：

- **列号漂移**：含 emoji 的行之后，UTF-16 列号小于字节列号；
- **代理对切断**：把 UTF-16 列号当字节用，会在 emoji 中间切分，产生非法 UTF-8；
- **静默错误**：`lsp_diagnostics` 无编码标记，错值入库后**无法事后判别**；
- **缓存串味**：同一符号在不同编码下位置不同，缓存键不含版本/编码则会命中错误条目。

### 1.3 风险不对称

| 方向 | 失败形态 | 代价 |
|---|---|---|
| 不定义边界，各处自行转换 | 漂移、代理对切断、无法事后判别 | **高**（静默污染持久化数据） |
| 显式定义 canonical + 边界转换 + 标记 | 一次性转换代码与测试 | **低** |

---

## 2. Decision Drivers

| # | 判据 | 可检验形式 |
|---|---|---|
| D1 | 内部必须只有一种 canonical 位置表示 | 全库不存在第二种列语义 |
| D2 | 必须按协议协商编码，不得硬编码 UTF-16 | initialize 请求含 `positionEncodings` |
| D3 | 落库前必须已转换 | 任一 `*_column` 均可与 canonical 定义对齐 |
| D4 | 必须能事后判别历史数据 | 存在 `position_encoding` 列 |
| D5 | 非 BMP 字符不得切断 | emoji 测试通过 |
| D6 | BOM 与行尾不得造成偏移漂移 | BOM / CRLF 测试通过 |
| D7 | 结果必须与 `document_version` 对齐 | 版本不匹配结果被丢弃 |
| D8 | 缓存键不得缺版本 | 缓存键含 `document_version` |

---

## 3. Considered Options

| 选项 | 描述 | 优点 | 代价 |
|---|---|---|---|
| **A** | 全库统一用 UTF-16 code unit | 与 LSP 默认一致，零转换 | 与 `03` §5.3 冲突；与 `03` §1 的 stable_key（基于 `rel_path+kind`，非位置）无冲突但需改 `03`；UTF-16 在 FTS/正则侧不便 |
| **B** | 全库统一用 UTF-8 byte offset（canonical），边界转换 | 与 `03` §5.3 一致；字节语义在切片/哈希上无歧义 | 需实现双向转换 |
| **C** | 全库统一用码点（code point）索引 | 人类直观 | 与 `03` §5.3 冲突；每次切片都要扫描 |
| **D** | 两套并存，靠列名区分 | 无转换 | 违反 D1；两套会漂移 |
| **E** | 不做转换，只在文档里警告 | 零成本 | 违反 D3/D5，是本 ADR 要消除的失败模式 |

---

## 4. Decision

采纳 **选项 B**，并做七项配套。

### 4.1 Canonical 定义（唯一内部表示）

```text
offset  = 从文件起始（不含 BOM）算起的 UTF-8 字节数
line    = 0-based 行号（LF 计数；CRLF 的 CR 计入该行）
column  = 该行起始到该位置的 UTF-8 字节数
```

- 与 `03` §5.3 第一句一致（证据 1）。
- 区间一律用半开 `[start, end)`。
- **全库只允许这一种语义**（D1）。`03.lsp_diagnostics` 的 `*_column` 按本定义落库。

### 4.2 协议协商

`initialize` 请求必须携带：

```json
{ "capabilities": { "general": { "positionEncodings": ["utf-8", "utf-16"] } } }
```

- **优先 `utf-8`**；server 若返回 `capabilities.positionEncoding`，以之为准。
- server 未返回该字段 → 按协议默认 `utf-16` 处理，**不得假设 utf-8**（证据 5）。
- 协商结果写入 `lsp_servers.position_encoding`（新增列，见 4.3）。

### 4.3 `lsp_servers` 新增一列（D4）

```sql
ALTER TABLE lsp_servers ADD COLUMN position_encoding TEXT NOT NULL DEFAULT 'utf-16';
```

- 取值 `utf-8 | utf-16 | utf-32`。
- 默认 `utf-16` 是**协议默认值**，不是我们的选择（证据 5）。
- 用途：事后判别、诊断报告、以及排查"同一文件两次索引结果不一致"类问题。

> `lsp_diagnostics` **不再新增编码列**：因为 §4.4 强制落库前已转换为 canonical，
> 表中所有列语义唯一。若再加一列反而会暗示"可能存 LSP 原生值"（违反 D1）。

### 4.4 转换只发生在边界（D3）

```text
LSP response ──(utf16/utf8 → canonical)──→ 内存模型 ──→ DB（canonical）
DB（canonical）──→ 内存模型 ──(canonical → negotiated)──→ LSP request
```

**硬约束**：`internal/knowledge/` 内不得出现"把 LSP 返回的 `character` 直接赋给 DB 列"的代码路径。
该约束可用一条测试或 review 规则检查。

### 4.5 文档内容与 BOM / 行尾（D6）

| 项 | 规则 | 理由 |
|---|---|---|
| 行尾 | **原样发送**文件字节，不把 CRLF 归一化为 LF | 归一化会让所有列偏移与磁盘文件不再对应 |
| BOM | `didOpen` 前**剥离 BOM**；映射回文件时加上 `bom_bytes`（3）偏移 | BOM 不是文本内容，但占文件字节；显式记账好过隐式漂移 |
| 编码 | 非 UTF-8 文件先转 UTF-8 再送 LSP，并记录原始编码 | LSP `textDocument` 是 UTF-8 文本 |
| 空文件 | `line=0, column=0`，区间 `[0,0)` | 边界值必须定义 |

### 4.6 `document_version` 对齐（D7）

- 每次 `didChange` 递增 `document_version`；与 `03.lsp_documents.document_version` 同步。
- LSP 返回结果携带的 version ≠ 当前 version → **丢弃**，不落库，不返回给模型。
- 这与 `03` §5.3 第三句一致，本 ADR 只把它从"缓存键要求"提升为**结果有效性要求**。

### 4.7 缓存键（D8）

```text
cache_key = H(file_id, document_version, canonical_start, canonical_end, adapter_version)
```

- **不含 `position_encoding`**：因为 §4.4 保证缓存的值已是 canonical，
  编码不影响 canonical 值。加入它只会制造无效缓存分片。
- **必须含 `document_version`**（`03` §5.3 要求）与 `adapter_version`（`03` §6.2 版本向量要求）。

---

## 5. Rationale

逐条回应 Decision Drivers：

- **D1**：选项 D 被否决的**真实缺陷**是两套语义会漂移，而漂移的表现是"某些文件正确、某些文件差几列"——最难定位的一类 bug。选项 A/C 被否决是因为它们要改 `03` §5.3 且收益不明确。
- **D2**：证据 5 是关键——`utf-16` 是**协议默认值而非协议常量**。硬编码它会在 server 支持 utf-8 时产生不必要的转换，也会掩盖"未协商"这一事实。
- **D3**：§4.4 把转换点收敛到一处。选项 E 被否决的**真实缺陷**是：它的失败是**静默且持久**的（错值入库），比抛错难处理得多。
- **D4**：§4.3 新增一列，成本一行 DDL。
- **D5**：§1.2 的表格给出必须覆盖的测试向量。
- **D6**：§4.5 把两个最易被忽略的偏移源（BOM、CRLF）显式记账。
- **D7**：§4.6。这条把"缓存键要求"升级为"正确性要求"，因为陈旧 version 的结果不只污染缓存，还会直接误导模型。
- **D8**：§4.7 明确缓存键组成，并**刻意排除** `position_encoding` 并说明理由。

**关于为何不选 UTF-16 作为 canonical（选项 A）**：
`03` §1 的 `stable_key` 基于 `rel_path + kind`，不含位置，因此两者对 stable_key 无差别。
但 canonical 还要服务于**字节切片、内容哈希、FTS 偏移**三处，
这三处天然是字节语义。选 UTF-8 可以避免在三个热点路径上各做一次转换。
**代价集中在 LSP 边界一处，收益分散在三处**——这是选 B 的核心理由。

---

## 6. Consequences

### 6.1 Positive

- 位置语义唯一，消费方无需猜测。
- 非 BMP / 组合字符 / BOM / CRLF 四个已知陷阱被显式覆盖。
- 协商结果可观测，便于诊断。
- 缓存键无冗余分片。

### 6.2 Negative / Accepted trade-offs

- **需要实现并测试双向转换。** 主动接受：这是 D1 的必要成本，且可被表格化测试穷举。
- **非 UTF-8 源文件需先转码**，转码后的字节偏移与磁盘文件不同。主动接受，但必须**记录原始编码**并在诊断里标注，否则会与 `grep` 的行号不一致。
- **`lsp_servers` 多一列**。主动接受：代价一行 DDL，收益是可事后判别。

---

## 7. Reversal Plan

- 想改用 UTF-16 canonical：改 §4.1 定义 + 转换方向反转 + 全量重建索引（Phase 0 无数据，成本为零）。
- 想放弃协商：删 §4.2，固定一个编码。**不推荐**（违反 D2）。
- 想移除 `position_encoding` 列：`ALTER TABLE ... DROP COLUMN`（SQLite 3.35+）。
- **无数据迁移**：`03.lsp_*` 三张表在 `04` §4.3 中属 v1 推迟项。

---

## 8. Validation

| 检查 | 形式 | 门槛 |
|---|---|---|
| 协商发出 | 断言 `initialize` 含 `positionEncodings: ["utf-8","utf-16"]` | 必须通过 |
| 默认不假设 | 伪造 server 不返回 `positionEncoding`，断言按 utf-16 处理 | 必须通过 |
| 非 BMP | 含 `😀` 的行，断言 canonical column 与 LSP column 互相可逆转换 | 必须通过 |
| CJK | 含 `中文` 的行 | 必须通过 |
| 组合字符 | `e` + U+0301 | 必须通过 |
| CRLF | CRLF 文件的第二行位置 | 必须通过 |
| BOM | 带 BOM 文件，断言映射回文件时偏移 +3 | 必须通过 |
| 空文件 / 单字符 | 边界值 | 必须通过 |
| 版本对齐 | version 不匹配的结果被丢弃 | 必须通过 |
| 无原生落库 | 代码审查/grep 确认无 LSP `character` 直赋 DB 列 | 必须通过 |
| 缓存键 | 断言键含 `document_version` 与 `adapter_version`，不含编码 | 必须通过 |

---

## 9. Alternatives Rejected (and why)

| 选项 | 否决理由（一句话） |
|---|---|
| A（UTF-16 canonical） | 与 `03` §5.3 冲突，且需在切片/哈希/FTS 三个热点路径各做一次转换 |
| C（码点 canonical） | 与 `03` §5.3 冲突，且每次切片都要扫描 |
| D（两套并存） | 会漂移，且漂移表现为"部分文件差几列"，最难定位 |
| E（不转换只警告） | 失败静默且持久（错值入库），正是本 ADR 要消除的 |
| 硬编码 UTF-16 | 把协议默认值当协议常量 |
| 给 `lsp_diagnostics` 加编码列 | 会暗示"可能存 LSP 原生值"，削弱 §4.4 的硬约束 |

---

## 10. Open Follow-ups

| 项 | Gate |
|---|---|
| `gopls` / `volar` / `typescript-language-server` 各自支持的 `positionEncodings` 实测 | `Phase4-start` |
| 非 UTF-8 源文件的转码与行号对齐策略（与 `grep` 行号一致性） | `Phase4-start` |
| 是否需要在 `files` 表记录 `source_encoding` | `Phase4-start` |
| 与 `03` §6.2 版本向量中 `lsp_server_version` 的联动 | `Phase4-start` |
| `03.lsp_servers` / `lsp_documents` / `lsp_diagnostics` 从 v2+ 提前到 v1 的取舍 | `Phase4-start` |
| `supplement/05` 附录 F 中"UTF-16 position 映射"高风险条目的状态更新 | `Phase4-start` |
