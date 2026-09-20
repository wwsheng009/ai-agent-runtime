# Agent Harness 代码智能感知方案补充规格

> 版本：v1.0-supplement  
> 日期：2026-09-20  
> 适用：`agent_harness_technical_design_spec_sqlite.md`  
> 与：`low_token_multilanguage_ai_agent_harness_design.md`  
> 定位：对原方案进行完整性补充，不替代原文。  
> 核心目标：让 Code Knowledge Runtime 从“架构完整”提升到“可实施、可验证、可演进”。

---

# 0. 补充范围

本补充文件主要覆盖以下缺口：

1. 稳定符号 ID 与重命名追踪
2. 项目、模块、构建与依赖模型
3. 类型、继承、实现、重载、泛型
4. 名称解析与导入解析
5. LSP 工程化规格
6. 版本向量、缓存失效与一致性
7. 安全、隐私、沙箱与审计
8. 评估基准与黄金任务集
9. 动态候选与不确定图
10. 测试智能
11. 跨语言与 IDL
12. 运行时证据接口
13. FTS5 多语言检索
14. Context Compiler 防注入、冲突解决与可解释性
15. 变更管理边界补充
16. 建议新增 API
17. 事件模型补充
18. Telemetry 补充
19. 实施优先级
20. 验收标准

---

# 1. 稳定符号 ID 与重命名追踪

## 1.1 问题

原方案中 `symbols.id` 是 TEXT PRIMARY KEY，但没有定义生成策略。  
文件修改、符号移动、重命名后，引用、缓存、Context 容易失效或错乱。

## 1.2 设计原则

稳定符号 ID 不应只依赖行号，也不应只依赖 `qualified_name`。

推荐：

```text
stable_key =
  language
+ kind
+ namespace/package/module
+ owner_qualified_name
+ qualified_name
+ signature_hash
```

对于支持重载的语言，必须把 `signature_hash` 纳入。

## 1.3 建议 DDL

```sql
ALTER TABLE symbols ADD COLUMN stable_key TEXT;
ALTER TABLE symbols ADD COLUMN signature_hash TEXT;
ALTER TABLE symbols ADD COLUMN owner_symbol_id TEXT;
ALTER TABLE symbols ADD COLUMN content_hash TEXT;
ALTER TABLE symbols ADD COLUMN language_extension_json TEXT;

CREATE UNIQUE INDEX idx_symbols_stable_key
ON symbols(workspace_id, stable_key);

CREATE TABLE symbol_aliases (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    alias_key TEXT NOT NULL,
    alias_type TEXT NOT NULL,
    valid_from_version INTEGER,
    valid_to_version INTEGER,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_symbol_aliases_symbol
ON symbol_aliases(symbol_id);

CREATE INDEX idx_symbol_aliases_key
ON symbol_aliases(alias_key);

CREATE TABLE symbol_renames (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    old_symbol_id TEXT,
    new_symbol_id TEXT,
    old_qualified_name TEXT,
    new_qualified_name TEXT,
    file_id TEXT,
    detected_by TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_symbol_renames_workspace
ON symbol_renames(workspace_id);
```

## 1.4 规则

```text
1. 符号内容变化但语义身份未变：stable_key 不变，version 增加。
2. 符号重命名：新增 symbol_aliases，必要时写 symbol_renames。
3. 符号移动：stable_key 尽量不变，file_id 更新。
4. 符号删除：不立即物理删除，标记 deleted_at / status。
5. 引用和缓存必须绑定 symbol_version，而不是只绑定 symbol_id。
```

---

# 2. 项目、模块、构建与依赖模型

## 2.1 问题

Tree-sitter 只能提供语法，LSP 需要项目配置。  
原方案缺少项目发现、构建配置、依赖解析、Monorepo 边界、生成代码来源。

## 2.2 建议 DDL

```sql
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL,
    project_type TEXT,
    language TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE modules (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    parent_module_id TEXT,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    root_path TEXT,
    language TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE packages (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    module_id TEXT,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    package_manager TEXT,
    version TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE build_configs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    module_id TEXT,
    config_path TEXT NOT NULL,
    config_type TEXT NOT NULL,
    content_hash TEXT,
    parsed_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE dependency_versions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    package_id TEXT,
    dependency_name TEXT NOT NULL,
    dependency_version TEXT,
    dependency_scope TEXT,
    source TEXT,
    resolved_path TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES projects(id)
        ON DELETE CASCADE
);

CREATE TABLE generated_sources (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    generated_file_id TEXT NOT NULL,
    source_file_id TEXT,
    generator TEXT,
    generator_version TEXT,
    source_relation TEXT,
    is_generated INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE ignore_rules (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    pattern TEXT NOT NULL,
    rule_type TEXT NOT NULL,
    reason TEXT,
    priority INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 2.3 项目发现优先级

```text
1. go.mod
2. package.json / pnpm-workspace.yaml / yarn.lock
3. pom.xml / build.gradle / settings.gradle
4. Cargo.toml
5. CMakeLists.txt / compile_commands.json
6. pyproject.toml / setup.py / requirements.txt
7. BUILD / WORKSPACE
8. SAP package / ABAP project metadata
```

---

# 3. 类型、继承、实现、重载、泛型

## 3.1 问题

原方案提到 `inheritance`、`type_graph`，但缺少完整数据模型。  
缺少这些，`code.impact`、`code.trace`、`find_implementations` 会不准。

## 3.2 建议 DDL

```sql
CREATE TABLE type_relations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT NOT NULL,
    to_symbol_id TEXT,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE INDEX idx_type_relations_from
ON type_relations(from_symbol_id);

CREATE INDEX idx_type_relations_to
ON type_relations(to_symbol_id);

CREATE TABLE inheritance_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    child_symbol_id TEXT NOT NULL,
    parent_symbol_id TEXT,
    parent_name TEXT,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE implementation_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    implementation_symbol_id TEXT NOT NULL,
    interface_symbol_id TEXT,
    interface_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE overloads (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    overload_group_key TEXT NOT NULL,
    signature_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE generic_params (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    name TEXT NOT NULL,
    bounds_json TEXT,
    variance TEXT,
    ordinal INTEGER,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE parameters (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    name TEXT,
    type_text TEXT,
    type_symbol_id TEXT,
    default_value TEXT,
    ordinal INTEGER,
    is_variadic INTEGER NOT NULL DEFAULT 0,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE local_variables (
    id TEXT PRIMARY KEY,
    symbol_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    name TEXT NOT NULL,
    type_text TEXT,
    start_line INTEGER,
    end_line INTEGER,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE annotations (
    id TEXT PRIMARY KEY,
    symbol_id TEXT,
    file_id TEXT,
    name TEXT NOT NULL,
    arguments_json TEXT,
    start_line INTEGER,
    end_line INTEGER
);
```

## 3.3 关系类型

```text
EXTENDS
IMPLEMENTS
IMPLEMENTS_INTERFACE
OVERRIDES
OVERLOADS
TYPE_ALIAS
GENERIC_BOUND
RETURNS
PARAMETER_TYPE
FIELD_TYPE
ANNOTATED_BY
```

---

# 4. 名称解析与导入解析

## 4.1 问题

动态语言和跨文件符号解析必须定义作用域、导入、包解析、重载消歧和动态候选。

## 4.2 建议 DDL

```sql
CREATE TABLE import_bindings (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    import_id TEXT,
    local_name TEXT NOT NULL,
    imported_name TEXT,
    module_path TEXT,
    resolved_file_id TEXT,
    resolved_symbol_id TEXT,
    resolution_status TEXT NOT NULL DEFAULT 'UNKNOWN',
    confidence REAL NOT NULL DEFAULT 0.5,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE name_resolutions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    source_symbol_id TEXT,
    reference_id TEXT,
    raw_name TEXT NOT NULL,
    resolved_symbol_id TEXT,
    candidate_symbol_ids_json TEXT,
    resolution_status TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,
    resolver TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE scope_bindings (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL,
    scope_symbol_id TEXT,
    name TEXT NOT NULL,
    symbol_id TEXT,
    binding_type TEXT NOT NULL,
    start_line INTEGER,
    end_line INTEGER
);
```

## 4.3 解析状态

```text
RESOLVED
CANDIDATE
AMBIGUOUS
UNRESOLVED
DYNAMIC
EXTERNAL
GENERATED
```

## 4.4 解析来源优先级

```text
LSP
Custom Semantic Adapter
Tree-sitter + Scope Resolver
FTS5
Regex / Text Search
```

---

# 5. LSP 工程化规格

## 5.1 必须补充

```text
LSP 生命周期
能力协商
初始化参数
workspace root
document sync
UTF-16 / byte offset 统一
请求超时
取消
并发限制
崩溃恢复
重启策略
多 root workspace
结果版本对齐
LSP 不可用降级
```

## 5.2 建议表

```sql
-- ⚠️ 待补列（ADR-0006 §4.3）：position_encoding TEXT NOT NULL DEFAULT 'utf-16'
--    用途：记录 initialize 协商得到的编码（general.positionEncodings），供事后判别与诊断。
--    注意：默认值 'utf-16' 是 **LSP 协议默认值**，不是本方案的选择。
--    在 ADR-0006 被 Accept 之前不执行本改动。见 adr/0006-lsp-position-encoding-boundary.md。
CREATE TABLE lsp_servers (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    language TEXT NOT NULL,
    command TEXT NOT NULL,
    args_json TEXT,
    root_path TEXT,
    capabilities_json TEXT,
    status TEXT NOT NULL DEFAULT 'STOPPED',
    last_started_at INTEGER,
    last_error TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE lsp_documents (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    lsp_uri TEXT NOT NULL,
    language_id TEXT,
    document_version INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT,
    opened_at INTEGER,
    updated_at INTEGER,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

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

## 5.3 位置编码规则

```text
内部统一使用 UTF-8 byte offset + line/column。
与 LSP 交互时转换为 UTF-16 code unit。
所有缓存键必须包含 document_version。
```

> **⚠️ 本节规则不完整（[ADR-0006](adr/0006-lsp-position-encoding-boundary.md)，2026-09-20）**
>
> 上述三条**方向正确但缺边界定义**，且第 2 条把协议默认值当成了协议常量。ADR-0006 补充以下缺口：
>
> | 缺口 | ADR-0006 的结论 |
> |---|---|
> | 编码协商 | `initialize` 必须携带 `general.positionEncodings: ["utf-8","utf-16"]`；**未协商时协议默认 `utf-16`**，不得假设 utf-8 |
> | canonical 精确定义 | `line` 0-based；`column` 是**行内 UTF-8 字节数**；区间半开 `[start, end)`；BOM 不计入 offset |
> | 非 BMP / 组合字符 | `😀`（代理对）、`e`+U+0301 必须可逆转换，不得切断代理对 |
> | BOM / CRLF | 行尾**原样发送**不归一化；`didOpen` 前剥离 BOM 并记 `bom_bytes` 偏移 |
> | `document_version` | 从"缓存键要求"**升级为结果有效性要求**：版本不匹配的结果必须丢弃 |
> | `lsp_servers` 列 | 需补 `position_encoding`（见 §5.2 该表的标注） |
>
> **在 ADR-0006 被 Accept 之前不改写本节的三条规则。**

---

# 6. 版本向量、缓存失效与一致性

## 6.1 问题

原方案有 cache key 和 dependency，但缺少统一版本向量、事务边界、幂等索引、事件 outbox、消费者 offset。

## 6.2 版本向量

```text
version_vector =
  workspace_version
+ repository_version
+ commit_hash
+ working_tree_hash
+ file_content_hash
+ symbol_version
+ parser_version
+ adapter_version
+ lsp_server_version
+ build_config_hash
+ ignore_rules_hash
```

## 6.3 建议 DDL

```sql
CREATE TABLE version_vectors (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repository_id TEXT,
    commit_hash TEXT,
    working_tree_hash TEXT,
    parser_version TEXT,
    adapter_versions_json TEXT,
    lsp_versions_json TEXT,
    build_config_hash TEXT,
    ignore_rules_hash TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE cache_dependencies (
    id TEXT PRIMARY KEY,
    cache_key TEXT NOT NULL,
    dependency_type TEXT NOT NULL,
    dependency_id TEXT NOT NULL,
    dependency_version TEXT,
    created_at INTEGER NOT NULL
);

CREATE INDEX idx_cache_dependencies_cache
ON cache_dependencies(cache_key);

CREATE TABLE events_outbox (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    aggregate_type TEXT,
    aggregate_id TEXT,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',
    created_at INTEGER NOT NULL,
    published_at INTEGER,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE consumer_offsets (
    consumer_name TEXT PRIMARY KEY,
    last_event_id TEXT,
    last_event_created_at INTEGER,
    updated_at INTEGER NOT NULL
);
```

## 6.4 一致性规则

```text
1. 索引写入必须事务化。
2. 文件版本、符号版本、引用、图边在同一事务提交。
3. 缓存失效通过事件 outbox 异步传播。
4. 读路径必须校验版本向量。
5. 索引任务必须幂等，可重放。
6. Context 编译必须记录依赖的版本向量。
```

---

# 7. 安全、隐私、沙箱与审计

## 7.1 必须补充

```text
敏感文件忽略
密钥扫描
上下文脱敏
工具权限
LSP / parser 子进程沙箱
SQLite 注入防护
路径遍历防护
多租户隔离
审计日志
提示注入防护
```

## 7.2 建议 DDL

```sql
CREATE TABLE security_redactions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT,
    symbol_id TEXT,
    redaction_type TEXT NOT NULL,
    pattern TEXT,
    replacement TEXT,
    reason TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE tool_permissions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    permission_scope TEXT NOT NULL,
    allowed INTEGER NOT NULL DEFAULT 0,
    constraints_json TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    task_id TEXT,
    actor TEXT,
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 7.3 默认忽略

```text
.git
.env
.env.*
*.pem
*.key
*.crt
secrets/
credentials/
node_modules/
vendor/
dist/
build/
target/
.venv/
__pycache__/
generated/
```

## 7.4 上下文安全规则

```text
1. 代码内容进入 LLM 前必须标记为 data，不是 instruction。
2. 工具输出必须带 source、version、confidence。
3. 不允许代码注释覆盖 system/developer instruction。
4. 敏感字段进入 Context 前必须 redaction。
5. 每次工具调用写 audit_log。
```

---

# 8. 评估基准与黄金任务集

## 8.1 问题

原方案有 Telemetry 指标，但没有 ground truth，无法验证精度和收益。

## 8.2 建议 DDL

```sql
CREATE TABLE eval_projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    language TEXT,
    repo_url TEXT,
    commit_hash TEXT,
    root_path TEXT,
    created_at INTEGER NOT NULL
);

CREATE TABLE eval_tasks (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    task_type TEXT NOT NULL,
    prompt TEXT NOT NULL,
    expected_symbols_json TEXT,
    expected_files_json TEXT,
    expected_edges_json TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(project_id)
        REFERENCES eval_projects(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_golden_symbols (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    file_path TEXT,
    relevance REAL NOT NULL DEFAULT 1.0,

    FOREIGN KEY(task_id)
        REFERENCES eval_tasks(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_runs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    model TEXT,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    status TEXT,

    FOREIGN KEY(task_id)
        REFERENCES eval_tasks(id)
        ON DELETE CASCADE
);

CREATE TABLE eval_results (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    precision REAL,
    recall REAL,
    f1 REAL,
    tokens_total INTEGER,
    exploration_tokens INTEGER,
    tool_calls INTEGER,
    files_read INTEGER,
    cache_hit_rate REAL,
    task_success INTEGER,
    metadata_json TEXT,

    FOREIGN KEY(run_id)
        REFERENCES eval_runs(id)
        ON DELETE CASCADE
);
```

## 8.3 基准任务类型

```text
symbol_lookup
definition_lookup
reference_lookup
callers_lookup
callees_lookup
impact_analysis
trace_path
cross_language_trace
test_discovery
edit_timeout
rename_symbol
```

## 8.4 对比模式

```text
A: 传统 grep/read_file Agent
B: Harness + Code Knowledge + Context Compiler
```

---

# 9. 动态候选与不确定图

## 9.1 问题

动态语言、反射、依赖注入、回调、事件、RPC 无法静态确定调用目标。

## 9.2 建议 DDL

```sql
CREATE TABLE call_candidates (
    id TEXT PRIMARY KEY,
    call_id TEXT NOT NULL,
    candidate_symbol_id TEXT,
    candidate_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    reason TEXT,

    FOREIGN KEY(call_id)
        REFERENCES calls(id)
        ON DELETE CASCADE
);

CREATE TABLE reference_candidates (
    id TEXT PRIMARY KEY,
    reference_id TEXT NOT NULL,
    candidate_symbol_id TEXT,
    candidate_name TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    reason TEXT,

    FOREIGN KEY(reference_id)
        REFERENCES references(id)
        ON DELETE CASCADE
);
```

## 9.3 解析状态

```text
RESOLVED
CANDIDATE
AMBIGUOUS
DYNAMIC
EXTERNAL
UNRESOLVED
```

## 9.4 置信度示例

```json
{
  "call": "obj.execute()",
  "resolved_target": null,
  "candidates": [
    {"symbol": "Foo.execute", "confidence": 0.63},
    {"symbol": "Bar.execute", "confidence": 0.41}
  ],
  "resolution_status": "CANDIDATE"
}
```

---

# 10. 测试智能

## 10.1 建议 DDL

```sql
CREATE TABLE tests (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    file_id TEXT NOT NULL,
    symbol_id TEXT,
    test_name TEXT NOT NULL,
    test_framework TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE test_symbols (
    test_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    relation_type TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 0.5,

    PRIMARY KEY(test_id, symbol_id, relation_type),

    FOREIGN KEY(test_id)
        REFERENCES tests(id)
        ON DELETE CASCADE,

    FOREIGN KEY(symbol_id)
        REFERENCES symbols(id)
        ON DELETE CASCADE
);

CREATE TABLE test_runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    task_id TEXT,
    test_id TEXT,
    status TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    output_hash TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE coverage_evidence (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT,
    file_id TEXT,
    test_run_id TEXT,
    covered INTEGER NOT NULL DEFAULT 0,
    coverage_count INTEGER,
    evidence_source TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

## 10.2 API

```text
code.tests(symbol)
code.impact(symbol, include_tests=true)
code.diff(include_tests=true)
```

---

# 11. 跨语言与 IDL

## 11.1 建议 DDL

```sql
CREATE TABLE cross_language_links (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT,
    to_symbol_id TEXT,
    from_file_id TEXT,
    to_file_id TEXT,
    relation_type TEXT NOT NULL,
    protocol TEXT,
    contract_id TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    source TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE idl_contracts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_type TEXT NOT NULL,
    name TEXT NOT NULL,
    file_id TEXT,
    content_hash TEXT,
    metadata_json TEXT,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE api_endpoints (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_id TEXT,
    method TEXT,
    path TEXT,
    handler_symbol_id TEXT,
    request_schema TEXT,
    response_schema TEXT
);

CREATE TABLE rpc_methods (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    contract_id TEXT,
    service_name TEXT,
    method_name TEXT,
    handler_symbol_id TEXT
);

CREATE TABLE message_topics (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    topic_name TEXT,
    producer_symbol_id TEXT,
    consumer_symbol_id TEXT,
    schema_id TEXT
);

CREATE TABLE db_schema_links (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT,
    table_name TEXT,
    column_name TEXT,
    relation_type TEXT,
    confidence REAL NOT NULL DEFAULT 0.5
);
```

## 11.2 关系类型

```text
HTTP_CALL
RPC_CALL
MESSAGE
DATABASE
FILE
PROCESS
CLI
GRAPHQL
PROTOBUF
OPENAPI
```

---

# 12. 运行时证据接口

## 12.1 定位

v1 可不实现，但 UCM 和 API 应预留。

## 12.2 建议 DDL

```sql
CREATE TABLE runtime_evidence (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    evidence_type TEXT NOT NULL,
    source TEXT,
    file_id TEXT,
    symbol_id TEXT,
    payload_json TEXT,
    confidence REAL NOT NULL DEFAULT 0.5,
    observed_at INTEGER,
    created_at INTEGER NOT NULL,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE runtime_edges (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    from_symbol_id TEXT,
    to_symbol_id TEXT,
    relation_type TEXT NOT NULL,
    call_count INTEGER,
    latency_ms REAL,
    confidence REAL NOT NULL DEFAULT 0.5,
    evidence_id TEXT
);

CREATE TABLE runtime_symbol_stats (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    symbol_id TEXT NOT NULL,
    call_count INTEGER,
    error_count INTEGER,
    avg_latency_ms REAL,
    last_seen_at INTEGER,
    evidence_source TEXT
);
```

## 12.3 合并策略

```text
Static Evidence
+ Runtime Evidence
+ Git Evidence
= Ranked Code Knowledge
```

静态图与运行时图冲突时：

```text
1. 保留两者。
2. 标记 source。
3. 运行时证据提高候选边 confidence。
4. 不直接删除静态边。
```

---

# 13. FTS5 多语言检索

## 13.1 问题

SQLite FTS5 默认分词对中文、下划线、驼峰不友好。

## 13.2 建议

```sql
CREATE VIRTUAL TABLE symbol_fts
USING fts5(
    name,
    qualified_name,
    signature,
    summary,
    normalized_name,
    content='symbols',
    content_rowid='rowid'
);
```

## 13.3 预处理

```text
原始：ToolExecutor.execute
normalized：tool executor execute

原始：getHTTPResponse
normalized：get http response

原始：用户服务
normalized：用户 服务
```

## 13.4 查询语法

```text
kind:method language:go scope:backend timeout
symbol:ToolExecutor.execute
file:executor.go
```

## 13.5 触发器

```sql
CREATE TRIGGER symbols_ai AFTER INSERT ON symbols BEGIN
  INSERT INTO symbol_fts(rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES (new.rowid, new.name, new.qualified_name, new.signature, NULL, new.qualified_name);
END;

CREATE TRIGGER symbols_ad AFTER DELETE ON symbols BEGIN
  INSERT INTO symbol_fts(symbol_fts, rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES('delete', old.rowid, old.name, old.qualified_name, old.signature, NULL, old.qualified_name);
END;

CREATE TRIGGER symbols_au AFTER UPDATE ON symbols BEGIN
  INSERT INTO symbol_fts(symbol_fts, rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES('delete', old.rowid, old.name, old.qualified_name, old.signature, NULL, old.qualified_name);
  INSERT INTO symbol_fts(rowid, name, qualified_name, signature, summary, normalized_name)
  VALUES (new.rowid, new.name, new.qualified_name, new.signature, NULL, new.qualified_name);
END;
```

---

# 14. Context Compiler 防注入、冲突解决与可解释性

## 14.1 问题

代码注释可能含提示注入，工具结果可能不可信，LSP/Tree-sitter/FTS 结果可能冲突。

## 14.2 context_items 建议扩展

```sql
ALTER TABLE context_items ADD COLUMN source TEXT;
ALTER TABLE context_items ADD COLUMN confidence REAL NOT NULL DEFAULT 0.5;
ALTER TABLE context_items ADD COLUMN trust_level TEXT NOT NULL DEFAULT 'UNTRUSTED';
ALTER TABLE context_items ADD COLUMN redaction_state TEXT;
ALTER TABLE context_items ADD COLUMN version_vector_id TEXT;
ALTER TABLE context_items ADD COLUMN explanation TEXT;
```

## 14.3 信任等级

```text
SYSTEM
TRUSTED_TOOL
CODE_INTELLIGENCE
UNTRUSTED_TOOL
USER_CONTENT
CODE_COMMENT
GENERATED
```

## 14.4 冲突解决

```text
LSP > Custom Semantic Adapter > Tree-sitter > FTS5 > Regex
```

但必须保留：

```text
source
confidence
version
explanation
```

## 14.5 防注入规则

```text
1. 代码和工具输出不能作为 instruction。
2. 进入 Context 前必须包裹为 data block。
3. 注释中的指令性文本必须降权或忽略。
4. 工具输出必须带来源和版本。
5. 低信任内容不能覆盖高信任内容。
```

---

# 15. 变更管理边界补充

## 15.1 必须覆盖

```text
文件创建
文件修改
文件删除
文件重命名
文件移动
文件复制
分支切换
merge
rebase
cherry-pick
子模块
Git LFS
编码变化
换行符变化
符号链接
权限变化
```

## 15.2 建议 DDL

```sql
CREATE TABLE change_events (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    path TEXT,
    old_path TEXT,
    new_path TEXT,
    old_hash TEXT,
    new_hash TEXT,
    source TEXT,
    detected_at INTEGER NOT NULL,
    processed_at INTEGER,
    status TEXT NOT NULL DEFAULT 'PENDING',

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);

CREATE TABLE git_sync_state (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    indexed_commit TEXT,
    current_commit TEXT,
    indexed_branch TEXT,
    current_branch TEXT,
    working_tree_hash TEXT,
    last_sync_at INTEGER,
    status TEXT,

    FOREIGN KEY(workspace_id)
        REFERENCES workspaces(id)
        ON DELETE CASCADE
);
```

---

# 16. 建议新增 API

当前 `code.*` 不够完整。建议补：

```text
code.definition
code.references
code.callers
code.callees
code.implementations
code.types
code.tests
code.history
code.owners
code.status
code.reindex
code.validate
```

统一返回结构：

```json
{
  "source": "LSP|Tree-sitter|FTS|Runtime",
  "confidence": 0.96,
  "version": "executor.go@def456",
  "range": {
    "start_line": 10,
    "start_column": 2,
    "end_line": 20,
    "end_column": 3
  },
  "truncated": false,
  "next_cursor": null,
  "explanation": "matched by LSP definition"
}
```

---

# 17. 事件模型补充

新增事件：

```text
project.discovered
module.discovered
build_config.changed
dependency.changed

symbol.renamed
symbol.moved
symbol.deleted

type_relation.changed
implementation.changed
overload.changed

name_resolution.changed
import_binding.changed

lsp.started
lsp.stopped
lsp.crashed
lsp.diagnostics.updated

cache.dependency.changed
version_vector.changed
events_outbox.pending
events_outbox.published

security.redaction.applied
tool.permission.denied
audit.log.created

eval.run.started
eval.run.completed
eval.result.recorded

runtime.evidence.ingested
runtime.edge.updated
```

---

# 18. Telemetry 补充

新增指标：

```text
symbol_resolution_precision
symbol_resolution_recall
reference_resolution_rate
call_resolution_rate
ambiguous_resolution_ratio
lsp_fallback_ratio
lsp_error_ratio
cache_invalidation_latency
context_stale_detection_count
context_recompile_count
redaction_count
permission_denied_count
eval_precision
eval_recall
eval_f1
cross_language_link_count
runtime_evidence_coverage
```

核心 KPI 补充：

```text
Code Retrieval Precision
Code Retrieval Recall
Impact Analysis Accuracy
Trace Accuracy
Test Discovery Accuracy
Cache Invalidation Correctness
Context Reuse Correctness
```

---

# 19. 实施优先级

## P0：必须补

```text
稳定符号 ID
项目/构建模型
类型/继承/实现
名称解析
LSP 工程化
版本向量与缓存一致性
安全隐私沙箱
评估基准
```

## P1：强烈建议补

```text
动态候选与不确定图
测试智能
跨语言与 IDL
运行时证据接口
FTS5 多语言检索
Context 防注入与冲突解决
变更管理边界
```

## P2：后置预留

```text
向量检索
Git 历史智能
代码所有权
分布式 PostgreSQL + Vector DB
多 Agent 共享知识
SAP Runtime / ABAP Compiler 深度集成
```

---

# 20. 验收标准

## 20.1 功能验收

```text
1. 符号重命名后，引用和缓存可追踪。
2. 项目/模块/构建配置可发现。
3. 类型、继承、实现、重载可查询。
4. LSP 不可用时自动降级。
5. 文件变更后缓存正确失效。
6. 敏感信息不会进入 LLM Context。
7. 评估基准可重复运行。
```

## 20.2 指标验收

```text
重复探索 Token < 20%
Context Cache Hit > 50%
常见 Symbol 查询 < 100ms
Light Index < 300ms / file
Context Compiler 缓存命中 < 200ms
符号解析精度 > 90%
引用解析召回 > 85%
```

具体阈值应根据项目规模、语言和硬件调整。

---

# 21. 与原文档整合建议

建议将本补充文件拆分为：

```text
docs/
  agent_harness_technical_design_spec_sqlite.md
  low_token_multilanguage_ai_agent_harness_design.md
  supplement/
    01_symbol_identity.md
    02_project_model.md
    03_type_system.md
    04_name_resolution.md
    05_lsp_engineering.md
    06_cache_consistency.md
    07_security.md
    08_evaluation.md
    09_dynamic_graph.md
    10_test_intelligence.md
    11_cross_language.md
    12_runtime_evidence.md
    13_fts.md
    14_context_safety.md
    15_change_management.md
```

如果只想保留一个文件，则保存为：

```text
agent_harness_supplement.md
```

---

# 附录 A：新增表清单

```text
symbol_aliases
symbol_renames
projects
modules
packages
build_configs
dependency_versions
generated_sources
ignore_rules
type_relations
inheritance_edges
implementation_edges
overloads
generic_params
parameters
local_variables
annotations
import_bindings
name_resolutions
scope_bindings
lsp_servers
lsp_documents
lsp_diagnostics
version_vectors
cache_dependencies
events_outbox
consumer_offsets
security_redactions
tool_permissions
audit_log
eval_projects
eval_tasks
eval_golden_symbols
eval_runs
eval_results
call_candidates
reference_candidates
tests
test_symbols
test_runs
coverage_evidence
cross_language_links
idl_contracts
api_endpoints
rpc_methods
message_topics
db_schema_links
runtime_evidence
runtime_edges
runtime_symbol_stats
change_events
git_sync_state
```

---

# 附录 B：新增 API 清单

```text
code.definition
code.references
code.callers
code.callees
code.implementations
code.types
code.tests
code.history
code.owners
code.status
code.reindex
code.validate
```

---

# 附录 C：新增事件清单

```text
project.discovered
module.discovered
build_config.changed
dependency.changed
symbol.renamed
symbol.moved
symbol.deleted
type_relation.changed
implementation.changed
overload.changed
name_resolution.changed
import_binding.changed
lsp.started
lsp.stopped
lsp.crashed
lsp.diagnostics.updated
cache.dependency.changed
version_vector.changed
events_outbox.pending
events_outbox.published
security.redaction.applied
tool.permission.denied
audit.log.created
eval.run.started
eval.run.completed
eval.result.recorded
runtime.evidence.ingested
runtime.edge.updated
```

---

# 结论

原方案的三层架构、UCM、增量索引、Exploration Graph、Context Compiler 方向正确。  
本补充文件主要补齐：

```text
稳定身份
项目模型
类型系统
名称解析
LSP 工程化
缓存一致性
安全隐私
评估基准
动态图
测试智能
跨语言
运行时证据
FTS 多语言
Context 安全
变更边界
```

补完这些后，Code Knowledge Runtime 才能从“能设计”进入“能实现、能验证、能演进”。