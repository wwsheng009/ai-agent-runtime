# FR-14 分阶段实施计划：workspace/project profile 只读发现与显式应用

> 日期：2026-09-25  
> 状态：**第一阶段已实施并验证**（2026-09-25：只读发现 + 显式应用落地；自动默认激活 / 多工作区自动解析 / 默认开启策略仍后置，见 §8 实施状态与偏差记录）  
> 范围：FR-14 / Q12 的第一阶段  
> 关联：`docs/plan/profile-scenario-implementation-plan-20260924.md`、`docs/plan/profile-scenario-context-pruning-plan-20260924.md`

## 1. 决策与范围冻结

FR-14 分阶段实施：**项目绑定的发现和显式应用可解冻；自动默认激活、多工作区自动解析和默认开启策略仍后置。**

本阶段必须满足：

1. runtime-server 按请求/会话的真实 workspace 发现 project profile，而不是使用 runtime-server 进程 cwd。
2. workspace 的项目绑定是一个只读、pointer-only 的 profile ref 指针；发现不会改变 default、session metadata 或 actor。
3. 用户通过现有显式 profile apply/session switch 入口应用绑定 profile；应用只作用于指定 session。
4. 用户显式选择优先于项目绑定：runtime request profile、session 已绑定 profile、CLI `--profile` 与 `/profile use` 的语义不被覆盖。
5. 无绑定文件时保持既有行为；绑定文件存在但格式错误、引用非法或目标缺失时显式报告错误，不静默回退到 user/default/其他同名 profile。
6. `.aicli/profiles` 的 profile 解析、merge、validate、D29 prompt 门控继续复用 `internal/profile` 与现有 server profile support。
7. 未信任 workspace 时，project profile 的 prompt/agent prompt 仍受 D29 门控；发现和显式 apply 的报告/UI 必须能表达扣留状态。
8. 旧后端响应没有新增 binding 字段时，前端不注册/不显示新能力，不影响既有 Profiles 页面和 `/profile` 命令。

本阶段明确不做：

- 不把 project binding 自动作为新会话默认 profile；
- 不在 `/api/agent/chat` 或 actor bootstrap 中隐式读取并激活 binding；
- 不实现多个 workspace 的自动选择/猜测、workspace 列表解析或 cwd fallback；
- 不修改 `profiles.default_profile`；
- 不让项目绑定覆盖 `--profile`、`/profile use` 或请求级 `profile`；
- 不引入第二套 profile resolver、merge、validation、写回或 trust 判定。

## 2. 绑定文件契约

### 2.1 位置

项目绑定文件固定为：

```text
<workspace>/.aicli/profile
```

它与项目 profile 目录 `<workspace>/.aicli/profiles/<ref>/` 并列。文件不存在表示“无项目绑定”，不得把不存在视为错误，也不得改变既有 profile 选择。

### 2.2 格式

采用最小 YAML pointer-only 文档：

```yaml
profile: coding
```

允许字段只有：

- `profile`：非空、单段 profile ref/name。

拒绝：

- 空 ref；
- 绝对路径、Windows 驱动器路径、UNC 路径、包含 `/` 或 `\\` 的值；
- `.`、`..`、含路径穿越的 ref；
- 嵌入完整 profile（例如 `profile: { ... }`）；
- 未知顶层字段、重复字段、非 mapping YAML、空文档；
- 绑定文件指向 workspace 之外的自定义 root；本阶段绑定只能解析为该 workspace 的 project layer profile。

绑定解析只负责读取、语法/形状/ref 安全校验和返回 metadata；目标 profile 的内容仍由 `profilesys.Resolve` / `profilesys.ResolveRef` 以及已有 `ValidateProfileReference` 解析。绑定 helper 不复制 merge 逻辑。

### 2.3 错误语义

- 文件不存在：`present=false`、无 error，保持零变化；
- 文件不可读、YAML 非法、字段非法：`present=true`、`valid=false`，返回 400/可显示领域错误；
- ref 对应 project profile 不存在或 `profile.yaml` 不存在：`present=true`、`valid=false`，显式返回目标缺失；不尝试 user 层、config root 或 default profile；
- profile.yaml 存在但内容无效：保留目标 metadata 并返回现有 resolver/validator 的错误；不回退；
- 绑定 API 发现本身不得因为“无绑定”返回 404。

## 3. 后端契约与实现分解

### 3.1 单一绑定 helper

在 `backend/internal/profile` 增加 workspace binding 的最小 helper（建议 `binding.go`）：

- `ProjectProfileBinding`：`Present/Valid/Workspace/Path/Ref/Root/Layer/Error` 等只读 metadata；
- `LoadProjectProfileBinding(workspace string)`：
  - 要求 workspace 非空并清理为绝对/规范 workspace 路径；
  - 只读取 `<workspace>/.aicli/profile`；
  - 用现有 yaml v3 解码并限制字段；
  - 校验 ref 为单段安全名字；
  - 用 `LayerRootForWorkspace("project", workspace)` 计算唯一 project root；
  - 只在 `<projectRoot>/<ref>/profile.yaml` 存在时形成 target；
  - 不调用 user/default fallback；
  - 不自动 Resolve 或激活 profile。
- 暴露 `ResolveProjectProfileBinding` 或等价的“绑定目标 + 现有 resolver 调用”薄封装时，内部仍只调用 `Resolve`/`ResolveRef`，不复制 profile 解析。
- 单元测试覆盖不存在、合法 ref、空 ref、绝对/驱动器/UNC/分隔符/`..`、嵌入对象、未知字段、重复字段、非法 YAML、目标缺失、workspace-specific 同名 profile。

workspace 规范化和 containment 检查必须使用 `filepath.Abs/Clean/Rel`；不能用字符串前缀。不要解析符号链接来扩大绑定范围，也不要让 ref 参与 `Join` 前未经安全校验。

### 3.2 runtime profile 列表/发现

扩展 `backend/internal/api/skills/profiles_store.go` 的列表结果，新增可选的 binding metadata（字段保持 snake_case）：

```json
{
  "project_binding": {
    "present": true,
    "valid": true,
    "ref": "coding",
    "source": "project_binding",
    "layer": "project",
    "workspace_path": "...",
    "path": ".../.aicli/profile",
    "profile_root": ".../.aicli/profiles/coding",
    "error": "",
    "prompt_suppressed": false,
    "prompt_suppression_reason": ""
  }
}
```

约定：

- 无 `workspace`/`workspace_path` 查询参数时，不读取绑定，响应新增字段可省略或返回 `null`，旧调用零变化；
- 有 workspace 参数时，调用 `LoadProjectProfileBinding`；绑定错误保留在 `project_binding`，不得把整个 profile 列表替换成 fallback；若是不可恢复的 workspace 参数错误，返回 400；
- workspace-aware 的 project profile discovery 使用 `LayerRootForWorkspace("project", workspace)`，不能调用无参 `LayerProfiles()`；
- 在现有 config/root/user 条目去重链路中，将 workspace project layer 按既有 project 优先级加入；无 workspace 时继续调用既有 `LayerProfiles()`；
- 每个发现条目保留 `source/layer/path/valid/error`，避免把绑定 ref 伪装成 default；
- 对绑定目标生成 `bound`/`is_bound` 只读标志（名称最终在实现中统一），但不改变 `is_default`。

为避免第二套 target resolver，给现有 `resolveRuntimeProfileTarget` 增加 workspace-aware 变体/参数：绑定 target 只允许 project root；普通 API ref 仍保持原有 config/root/registry/layer 语义。绑定 target 解析失败必须携带绑定错误，不能调用普通 resolver 继续找 user profile。

### 3.3 发现端点与显式 apply

优先复用 `GET /api/runtime/profiles?workspace=...` 作为发现端点，不新增重复的 `/binding` 解析 API。若现有前端/调用方需要更小响应，可增加只读 `GET /api/runtime/profiles/binding?workspace=...`，但实现必须调用同一 helper，不能形成第二个读取口径；默认优先列表字段，减少 API 面。

显式应用复用已有：

```text
POST /api/runtime/profiles/{ref}/apply
```

请求继续要求显式 `session_id`。前端“应用项目绑定”动作只在拿到 `project_binding.valid=true` 后，以 binding ref 调用既有 apply；后端 apply 继续走 `applySessionProfileSwitch`，而不是调用 default 端点或增加另一份 actor/session 变更逻辑。

如果实现需要从 binding 直接应用，必须要求请求同时携带 `workspace` 和 `session_id`，并先验证该 workspace 的 binding ref 与请求 ref 完全相同；不匹配返回 409/400，防止 UI 或恶意调用把任意 profile 冒充项目绑定。该校验只增加绑定声明一致性检查，实际切换仍调用 `applySessionProfileSwitch`。

### 3.4 precedence 与请求路径

选择语义按“显式选择不被项目绑定覆盖”冻结：

1. runtime request `profile`；
2. session metadata/profile 已有显式绑定；
3. CLI `--profile`；
4. `/profile use`（写入 session binding）；
5. 用户在 UI/CLI 触发的 project binding 显式 apply；
6. 既有 config default/无 profile 行为。

列表/发现只提供候选和状态，不参加第 1-6 项的隐式解析。`resolveProfileReference`、`applyProfileFallback`、`resolveProfileSessionState` 的既有调用链保持原语义：本阶段不在 `profileRef==""` 时偷偷插入 binding ref。`--profile` 和 `/profile use` 的代码不改成“绑定优先”。无绑定文件与现有 HEAD 行为一致。

### 3.5 workspace 与 D29

- session apply 使用 session metadata 中已有 workspace/worktree path；不得用服务进程 cwd 替代；
- list/discovery 使用请求 `workspace`/`workspace_path`；必须验证路径存在且为目录，避免把任意字符串当工作区；
- project layer root 由 `LayerRootForWorkspace` 唯一计算；
- 解析后的 profile 继续经 `profilesys.ApplyProjectPromptGate`，信任结论继续由 `workspaceFolderTrust` 提供；
- 发现层的 `prompt_suppressed` 和 apply report 的 warning 必须与同一 `EvaluateProjectPromptGate` 口径一致；
- 若 D29 的 profiles marker 扩展尚未在当前工作树落地，FR-14 实现必须一并确认/补齐 `.aicli/profiles` 的 `foldertrust.ConfigKindProfiles` 检测，否则 project-only workspace 会被误判 trusted；该修改仅限 foldertrust 与 FR-14 相关测试。

## 4. 前端实现分解

### 4.1 类型与 API 归一化

修改：

- `frontend/src/types/runtime/profiles.ts`
- `frontend/src/api/runtime/profiles.ts`
- 相关 fixtures/tests

新增可选 `projectBinding` 类型及 list response 字段。API 归一化必须对旧响应缺失字段使用 `null`/`undefined` 的兼容值，不把缺失当成“无效绑定错误”，也不把 binding ref 填入 `defaultProfile`。

`projectBinding` 至少包括：`present`、`valid`、`ref`、`workspacePath`、`path`、`profileRoot`、`error`、`promptSuppressed`、`promptSuppressionReason`。后端新字段缺失时：

- Profiles 列表照常渲染；
- 不显示项目绑定卡片/应用按钮；
- 不触发额外 binding 请求；
- composer 既有 `/profile` 能力广告仍以 `sessionSwitch` 为准。

### 4.2 设置页

复用：

- `sections/modes/profiles.tsx`
- `profile-list-header.tsx` / `profile-list-row.tsx`
- `profiles-trust-notice.tsx`
- 现有 `listRuntimeProfiles`、`applyRuntimeProfile` 与刷新/错误反馈。

在 workspace 参数明确可用且后端返回 `projectBinding` 时显示只读项目绑定卡片：workspace、ref、目标 profile 状态、source/layer、错误和 D29 prompt warning。列表加载、workspace 切换、刷新和选择 profile 都不得自动调用 apply/default。

**实施偏差（2026-09-25，已按实际约束修正）**：原计划在卡片上提供“应用到当前会话”按钮；实施时确认**设置页没有会话上下文**（`/runtime-config` 不持有 session_id，而 apply 明确要求显式 session_id、服务端不推断），因此按钮只能永远禁用或以报错收场。第一阶段改为**不提供按钮**，卡片给出可执行指引「在会话内执行 `/profile <ref>`」（既有显式切换核心，与 composer 同一语义）。若将来设置页获得会话上下文，再按下列按钮行为接线：

1. `valid=false` 禁用并展示明确错误；
2. `valid=true` 调用既有 apply，并传显式 session id；
3. 成功显示已有 switch report，刷新列表/当前 session；
4. 失败保留错误，不改变本地 default/selection；
5. 绑定 ref 与当前 workspace 不匹配时前端不发请求，或后端返回一致性错误。

不要把项目绑定显示为全局 default；不要为旧后端猜测 project binding 状态。

## 5. 文件范围（实施时严格控制）

预计仅修改/新增以下 FR-14 相关文件；若实际需要扩展，必须先确认同一职责：

### 后端

- `backend/internal/profile/binding.go`、`binding_test.go`；
- `backend/internal/profile/layer.go`、`layer_test.go`（workspace-aware discovery helper/测试）；
- `backend/internal/api/skills/profiles_store.go`、`profiles_handlers.go`、`profiles_handlers_test.go`；
- `backend/internal/api/skills/profile_support.go`（仅 workspace/binding consistency 适配，禁止隐式激活）；
- `backend/internal/api/skills/session_profile_switch.go`（仅复用/暴露既有 apply，不复制切换）；
- `backend/internal/api/skills/profiles_store_layer_test.go` 或新增绑定集成测试；
- `backend/internal/foldertrust/*` 仅在确认 profiles marker 缺口仍存在时修改，并补对应测试。

### 前端

- `frontend/src/types/runtime/profiles.ts`；
- `frontend/src/api/runtime/profiles.ts`；
- `frontend/src/components/workspace/settings/backend-config-settings-page/sections/modes/profiles.tsx`；
- `profile-list-row.tsx`、`profiles-trust-notice.tsx` 或同职责组件；
- 对应 profiles API/UI tests 与 fixtures。

不触碰当前工作树中已有的 chat/web/resume/mesh 等无关改动。

## 6. 测试矩阵与验收标准

### 6.1 profile/helper

- 无绑定文件：`present=false`，旧列表/解析路径不变；
- 合法 `profile: coding`；
- 空、标量、数组、非法 YAML、重复/未知字段；
- 绝对路径、驱动器、UNC、`/`、`\\`、`.`、`..` 和路径穿越拒绝；
- 内嵌 profile/spec 字段拒绝；
- project target 缺失显式错误且不命中 user 同名 profile；
- 两个 workspace 同名 profile 互不串用；
- project layer 优先于 user layer，但绑定只接受目标 workspace project layer。

### 6.2 API/server

- `GET /api/runtime/profiles?workspace=...` 返回 workspace-specific project 条目和 binding metadata；
- 不带 workspace 的列表保持旧字段/旧发现语义；
- 非法 workspace 参数、绑定 YAML 错误、绑定 target missing 都显式返回/展示错误，不 fallback；
- binding discovery 不修改 default/session/actor；
- apply 需要显式 session id，只影响目标 session，不改 default；
- binding-ref/workspace 一致性检查；
- runtime request profile、session explicit profile、`--profile`/`/profile use` 优先于 binding；
- 未信任 workspace 的 project prompt 被 suppression，trusted workspace 恢复；
- 多 workspace 隔离；
- 旧后端/旧响应兼容。

### 6.3 frontend

- 旧 list response 无 binding 字段正常渲染；
- 合法 binding 显示 ref/source/workspace/status；
- 无 binding 不显示错误；
- invalid/missing target 显示错误且按钮禁用；
- 页面加载和刷新不自动 apply；
- 显式 apply 成功/失败状态；
- default 标记不因 binding 改变；
- D29 trust notice 与现有字段兼容。

### 6.4 验证命令

先保留现有工作树快照并记录 FR-14 文件 diff，再运行：

```powershell
cd E:\projects\ai\ai-agent-runtime\backend
go test ./internal/profile ./internal/api/skills
```

若涉及 foldertrust，再增加其定向包测试；前端运行相关 Vitest 文件/项目现有测试命令。最后检查：

```powershell
git diff -- <FR-14 files>
git status --short
```

不得用 `git reset --hard`、`git checkout --` 或覆盖其它未提交文件；任何失败需区分 FR-14 回归与工作树既有变更导致的基线问题。

### 6.5 测试矩阵落地对照（2026-09-25 复核）

§6.1-§6.3 逐条对应关系（"后置"= 本阶段有意不做，见 §4.2 偏差与 §8.4）：

| 计划项（§6.x） | 落地证据 / 状态 |
|---|---|
| 6.1 无绑定文件 `present=false`、旧路径不变 | `binding_test.go:TestLoadProjectProfileBindingAbsent` + API `...WithoutBindingIsNotAnError` |
| 6.1 合法 `profile: coding` | `binding_test.go:TestLoadProjectProfileBindingValid` |
| 6.1 空/标量/数组/非法 YAML/重复/未知字段/内嵌 spec | `TestLoadProjectProfileBindingRejectsBadDocuments`（13 类，含"数组值"“嵌入 mapping”“未知字段”） |
| 6.1 绝对路径/驱动器/UNC/分隔符/`.`/`..`/穿越拒绝 | `TestLoadProjectProfileBindingRejectsUnsafeRefs`（14 类） |
| 6.1 target 缺失显式错误且不命中 user 同名 | `TestLoadProjectProfileBindingTargetMissingDoesNotFallBack` + API `...BindingTargetMissingDoesNotFallBack` |
| 6.1 双 workspace 同名互不串用 | `TestLoadProjectProfileBindingKeepsWorkspacesSeparate` + API `...WorkspaceIsolation` |
| 6.1 project layer 优先于 user layer，绑定只接受本工作区项目层 | `layer_test.go:TestRegisterLayerFallbacksPrecedence` + binding 目标 containment 校验 |
| 6.2 workspace-specific 项目条目 + binding metadata | API `...ReportsWorkspaceProjectBinding` |
| 6.2 不带 workspace 保持旧字段/旧语义 | `profiles_handlers_test.go` 既有用例 + `LayerProfiles()` 逐字不变 |
| 6.2 非法 workspace / 绑定 YAML 错误 / target missing 显式报错不 fallback | API `...InvalidWorkspaceParameterIsBadRequest`（400）/ `...BindingDocumentErrorsAreReported`（200+error）/ `...BindingTargetMissingDoesNotFallBack` |
| 6.2 discovery 不改 default/session/actor | 同上用例断言 `is_default=false` + 磁盘无写入；`...BindingIsNotImplicitlyActivated` 反证 |
| 6.2 未信任 prompt suppression / trusted 恢复 | API `...BindingPromptSuppressionFollowsTrust`（与 `ApplyProjectPromptGate` 同源） |
| 6.2 runtime 请求级 / session 显式 / `--profile` / `/profile use` 优先于 binding | 结构性成立（绑定不参与任何解析链路）+ `...BindingIsNotImplicitlyActivated` 反证 |
| 6.2 apply 需要显式 session id、binding-ref/workspace 一致性检查 | **后置**：不新增绑定 apply 端点/校验（§3.3 已定的接入条件见 §8.4），显式应用沿用会话内 `/profile <ref>` |
| 6.3 旧响应无 binding 字段正常渲染 / 无 binding 不显示错误 | `profiles.test.tsx`（旧后端缺字段、`present=false` 两例） |
| 6.3 合法 binding 显示 ref/source/workspace/status | `profiles.test.tsx`（只读卡片 + 徽标） |
| 6.3 invalid/missing target 显示错误且按钮禁用、显式 apply 成功/失败 | **部分后置**：错误展示已落地；按钮不存在（§4.2 偏差记录），apply 走会话内 `/profile` |
| 6.3 加载/刷新不自动 apply、default 标记不因 binding 改变 | `profiles.test.tsx`（断言 apply/default 未被触发） |
| 6.3 D29 trust notice 兼容 | `profiles.test.tsx`（未信任扣留警告）+ 既有 trust 用例 |

## 7. Definition of Done

- 绑定文件格式、错误语义、workspace 计算和 precedence 有代码测试锁定；
- project binding 只读发现可见，显式 apply 可用，且 apply 复用 `applySessionProfileSwitch`；
- 无绑定与旧后端行为兼容；
- D29 prompt 门控保持 fail-closed、发现与实际解析同源；
- 自动默认激活、多工作区自动解析、默认开启 project profile 策略仍未实现且有测试/文档断言；
- 生产代码只在上述 FR-14 文件范围内变更；
- 定向后端/前端测试通过，diff 审查确认没有第二套 resolver/merge/activation 逻辑。

## 8. 实施状态（2026-09-25 第一阶段落地）

### 8.1 交付物（代码）

后端：

- `backend/internal/profile/binding.go`：`ProjectProfileBinding` 与 `LoadProjectProfileBinding(workspace)`（单一绑定 helper）——只读 `<workspace>/.aicli/profile`；pointer-only（唯一字段 `profile`，必须字符串标量）；ref 必须单段安全名（拒绝绝对路径 / 驱动器 / UNC / `/`、`\`、`:` / `.`、`..` / 路径穿越 / 非法名）；目标固定为 `<workspace>/.aicli/profiles/<ref>/profile.yaml` 且必须存在。`present` 与 `valid` 分开表达"没有绑定"与"绑定坏了"，两者都不触发 user/config/default 回退。
- `backend/internal/profile/layer.go`：新增 `LayerProfilesForWorkspace(workspace)`；层扫描口径抽为 `layerProfilesWith(rootFor)` 单点，`LayerProfiles()` 行为逐字不变（project 层仍按进程 cwd）。
- `backend/internal/api/skills/profiles_store.go`：列表端点按 `workspace` 枚举项目层；回填 `project_binding`；给绑定目标标注只读 `is_bound`（不改 `is_default`）；新增 `errRuntimeProfileWorkspaceInvalid` 与 `projectPromptSuppression`（与运行期 `ApplyProjectPromptGate` 同一函数）。
- `backend/internal/api/skills/profiles_handlers.go`：workspace 参数不可用 → 400（`ErrValidationFailed`），绑定文件问题仍 200。

前端：

- `frontend/src/types/runtime/profiles.ts`：`RuntimeProfileProjectBinding`、`RuntimeProfileListResponse.projectBinding`、条目 `isBound`。
- `frontend/src/api/runtime/profiles/normalize.ts`：`normalizeProfileProjectBinding`（字段缺失 → `null`；不把旧后端当成"绑定无效"）。
- `frontend/src/components/.../modes/profiles-project-binding.tsx`：只读绑定卡片（引用 / 工作区 / 指针文件 / 目标目录 / 状态 / 错误 / D29 提示 / 会话内应用指引）。
- `profiles.tsx` 接线（仅 `present=true` 挂载）、`profile-list-row.tsx` 绑定徽标、i18n zh/en 对称新增键。

### 8.2 契约要点（与本文档 §2/§3 的差异以本节为准）

| 场景 | 行为 |
|---|---|
| 未声明 workspace | 不读绑定、不按工作区枚举；响应无 `project_binding` / `workspace_path`（旧调用零变化） |
| 无绑定文件 | `project_binding.present=false`，无 error（不是错误） |
| 绑定文档非法 | 200；`present=true`、`valid=false`、`error` 可显示；列表其余条目照常返回 |
| 目标缺失 / 目标不是文件 | 200；`valid=false`；**不**回退到 user/config/default |
| workspace 参数不存在 / 不是目录 | 400（调用方输入错误） |
| 未信任工作区 | 绑定目标 `prompt_suppressed=true` + reason（与运行期门控同源）；信任后重载即消失 |
| 多工作区 | 每个 workspace 只看到自己的项目层与绑定（`LayerProfilesForWorkspace`） |
| 绑定目标条目 | `is_bound=true`，`is_default` 不变（绑定 ≠ 默认） |

### 8.3 测试与验证证据

- `backend/internal/profile/binding_test.go`：缺文件 / 坏文档（13 类）/ 不安全 ref（14 类）/ 目标缺失不回退 user 同名 / 目标非文件 / 双工作区隔离 / 相对路径归一化。
- `backend/internal/profile/layer_test.go`：`LayerProfilesForWorkspace` 含工作区项目层与 user 层、不含进程 cwd 项目层；空工作区退化为 `LayerProfiles()`。
- `backend/internal/api/skills/profiles_binding_handlers_test.go`（8 例）：合法绑定元数据 + `is_bound` + `is_default=false` + 只读（文件与目录清单不变）；无绑定文件不是错误；非法 YAML 200 + error；目标缺失不回退 user 同名且不标注；非法 workspace 400；双工作区隔离；D29 扣留随信任授予消失；**隐性激活反证**（工作区有绑定时未显式指定 profile 仍不落 profile）。
- `frontend/.../modes/profiles.test.tsx` +5 例：旧后端无字段不渲染 / `present=false` 不渲染 / 合法绑定只读卡片 + 徽标且不触发 apply·default / 绑定不可用显示错误且隐藏应用指引 / 未信任显示扣留警告。

命令与结果：

```powershell
cd backend
go build ./internal/profile/... ./internal/api/skills/... ./internal/foldertrust/...   # 退出 0
go test ./internal/profile/ -count=1                                                  # ok（约 3s）
go test ./internal/api/skills/ -run "TestRuntimeProfilesAPI(ReportsWorkspaceProjectBinding|WithoutBindingIsNotAnError|BindingDocumentErrorsAreReported|BindingTargetMissingDoesNotFallBack|InvalidWorkspaceParameterIsBadRequest|WorkspaceIsolation|BindingPromptSuppressionFollowsTrust|BindingIsNotImplicitlyActivated)$" -count=1 -v   # 8/8 PASS
cd ..\frontend
npx vitest run src/components/workspace/settings/backend-config-settings-page/sections/modes/profiles.test.tsx   # 12/12 passed
npm run verify:lines                                   # 0 个 > 500 非空行（1325 文件）
npm run lint:i18n                                      # scanned=906, violations=0
npx tsc -b                                             # 退出 0
npm run test                                           # 333/333 文件、2773/2773 用例通过（360.1s）
```

> 说明：全包 `go test ./internal/api/skills/ ./internal/profile/ -count=1` 已通过（38.1s / 3.0s，含包内全部 Trust/Workspace 用例）；迭代期曾观测到宽选择器（`-run "Binding|Workspace|Trust"`）在同树其它重型构建并行时长时间不收敛——**负载抖动而非用例问题**，定向验证仍建议用上面的精确选择器以缩短反馈环。

### 8.4 本阶段仍然不做（有测试/文档断言）

- 不把 `.aicli/profile` 自动作为新会话默认 profile（无任何 bootstrap/actor 读取路径）；
- 不做多工作区自动选择/猜测、不做 workspace 列表解析、不做 cwd fallback（空 workspace 明确不启用项目绑定）；
- 不修改 `profiles.default_profile`；
- 不让项目绑定覆盖 `--profile` / `/profile use` / runtime 请求级 `profile`；
- 不新增绑定专属 apply 端点：显式应用沿用会话内 `/profile <ref>`（复用既有 `applySessionProfileSwitch` 语义与会话工作区解析）。若将来新增"从绑定直接 apply"，按 §3.3 要求同时携带 `workspace` + `session_id` 并做一致性校验。
- CLI/TUI 的绑定**展示**不在本切片（发现面已通过只读 API + 设置页可见；`/profile` 的显式切换沿用既有按会话工作区解析的层兜底，无需改动）。
- **遗留风险（登记，2026-09-25 完成度审计发现）**：`{ref}` 类端点的普通解析仍按**进程 cwd** 解析 project 层（`resolveRuntimeProfileTarget` → `LayerProfiles()`，§3.2 明确的既有语义），而列表在带 `workspace` 时按**该工作区**枚举（`LayerProfilesForWorkspace`）→ 当 server cwd ≠ 会话工作区时，工作区项目层条目可能“列表可见、详情/编辑不可达”；若 cwd 侧存在同名项目层 profile，还会命中另一份（**跨工作区写入风险**，非安全绕过）。本阶段不改 `{ref}` 解析语义（避免扩大 diff 并保持旧调用零变化）；后续项：给 `resolveRuntimeProfileTarget` 增 workspace 变体（或列表侧标注口径差异），并把 `workspace` 透传到详情/写端点后再解除登记。
