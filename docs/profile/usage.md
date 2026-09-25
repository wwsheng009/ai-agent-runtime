# Profile 使用手册（CLI / TUI / Web）

> 状态：已实施（2026-09-25）。配置字段参考见 [configuration.md](./configuration.md)；行为契约见 [design.md](./design.md)。
> 记住一句话：**创建与编辑是显式动作，切换是显式动作，发现永远只读。**

---

## 1. 三条入口与命令语法差异（先看这张表）

| 入口 | 切换会话 profile | 说明 |
| --- | --- | --- |
| CLI（非交互） | `aicli chat --profile <ref>` | 启动期选择；也可 `--profile auto` |
| 终端 TUI（`aicli chat` 内） | `/profile use <ref>` | 子命令式；裸 ref 会报「未知子命令」 |
| Web composer（会话输入框） | `/profile <ref>` | 参数即候选 ref；`use` **不是**它的子命令 |
| Web 设置页 | 无切换动作 | 只读列表/编辑器 + 项目绑定卡片，卡片给出会话内命令指引 |

`<ref>` 三种写法通用：注册名、`profiles.root` 下的目录名、profile 目录路径。

---

## 2. 选择与生效

| 优先级 | 来源 | 例子 |
| --- | --- | --- |
| 高 | CLI `--profile` | `aicli chat --profile coding` |
| | 会话内显式切换 | TUI `/profile use coding` / composer `/profile coding` |
| | 项目绑定（**显式应用**） | 同上两条命令，只是 ref 来自 `.aicli/profile` |
| 低 | `profiles.default_profile`（env `DEFAULT_PROFILE`） | 配置文件或环境变量 |
| 兜底 | 无 profile | 全量行为（与未引入 profile 时一致） |

- `--profile auto`：按**首轮提示词**路由（`write/implement/fix/...` → `executor`、`plan/design/...` → `planner`、
  `search/inspect/...` → `explore`，未命中 → `executor`；可用 config `profiles.auto` 整体替换）。仅启动期解析，
  适合 `chat --prompt/--message` 与 `exec`；纯交互式 `chat`、`agent stdio` 没有首轮提示词，启动即报错并给出替代。
- `--agent <id>`：有 profile 时选 profile 内 agent；无 profile 时加载 portable agentdef（builtin / 项目 `.agents/agents`）。
- 切换**下一轮生效**：写入会话身份 + 删除 prompt 冻结锚点 + 驱逐空闲 actor；在途 turn 仍走旧面（回执里会说明）。
- 全新环境的可用集来自**首启播种**：首次引导把内置模板写进 user 层（仅当该层还没有任何 profile），
  来源标注是 `user`、与手工 `aicli profile create` 产物一致；它们只是"可用"，不写 `profiles.default_profile`，
  也不会自动生效（播种纪律见 [configuration.md §7](./configuration.md#7-参考样例与内置模板)）。

---

## 3. CLI 命令参考

```powershell
# 列表：标注来源与默认生效项（config 注册项 / profiles.root / project 层 / user 层 / 显式路径）
# 首次引导已把内置模板播种进 user 层（仅当该层还没有任何 profile）
aicli profile list
aicli profile list .\profiles\review --output json

# 解析后视图：工具/skills/mcp/prompts/paths + token 估算（与真实会话同一解析逻辑）
aicli profile show coding
aicli profile show coding --agent explore --output json

# 校验（error 级退出码 1，可直接做 CI 门禁）
aicli profile validate coding

# 生成（内置模板：coding | review | minimal | docs）
aicli profile create my-coding --template coding --dry-run
aicli profile create my-coding --template coding --use --set-default
aicli profile create ./profiles/team --template docs --root .\profiles\team --force

# 分享闭环
aicli profile export coding                      # 缺省写 ./coding.zip
aicli profile export coding --out dist\coding.zip --dry-run
aicli profile import dist\coding.zip --to project
aicli profile import .\incoming\coding --name coding --dry-run
```

| 命令 | 关键 flag |
| --- | --- |
| `profile list [path]` | `--output text\|json`、`-j/--json` |
| `profile show <ref>` | `--agent <id>`、`--output`、`-j` |
| `profile validate <ref>` | 无额外 flag（退出码即结论） |
| `profile create <name>` | `--template coding\|review\|minimal\|docs`、`--root <dir>`、`--use`、`--set-default`、`--dry-run`、`--force`、`--agent` |
| `profile export <ref>` | `--out <zip\|dir>`、`--dry-run`、`--output`、`-j` |
| `profile import <path>` | `--to user\|project`（默认 user）、`--name <名称>`、`--dry-run`、`--output`、`-j` |

写回边界：`--root` 缺省 `profiles.root/<name>`，再缺省 `./profiles/<name>`；`--use` / `--set-default` 会额外更新
配置文件（共用写锁与分层写路由），失败时状态零改动。`export` 只读（不写 profile/配置/会话；符号链接与 `*.tmp`
不进包；上限 256 文件 / 单文件 2MiB / 总 8MiB），`import` 先校验、不覆盖同名、绝不自动激活。

---

## 4. 终端 TUI：`/profile` 命令面

```
/profile [status]                                  当前 profile：引用、来源、生效摘要
/profile list                                      可用 profile 列表
/profile show <name>                               只读预览：该 profile 将带来什么（不切换）
/profile diff [<name>]                             与当前对比：tools/skills/mcp/prompt 变化
/profile use <name>                                热切换（下一轮生效；含 cache_notice）
/profile pick                                      交互选择
/profile reload                                    重新解析当前 profile（磁盘编辑后）
/profile off                                       回到无 profile 基线（完整失效）
/profile save [--to session|workspace|config] [--yes]   持久化默认 profile
/profile create <name> [--template coding|review|minimal|docs] [--to user|project] [--force]
/profile duplicate <ref> <name> [--to user|project]     复制（不覆盖同名）
/profile save-as <name> [--to user|project]        从当前会话固化差分（D24；不覆盖同名）
/profile edit [<ref>] [--open]                     打印 profile.yaml 路径；--open 拉起 $EDITOR
/profile rename <ref> <new-name>                   重命名（同层；含配置引用改写）
/profile move <ref> --to user|project              层级移动（跨层；同层拒绝）
/profile delete <ref> [--force]                    删除（引用检查 + 文件清单；--force 清空 default）
/profile export [<ref>] [--out <file|dir>]         导出 zip（默认 ./<name>.zip）
/profile import <包路径|目录> [--to user|project] [--name <名字>] [--dry-run]
/profile help                                      显示用法
```

要点：切换只收窄安全基线、不会放宽权限；`save-as` 固化的是与内置默认面的**差分**（prompt / 权限模式不在
`profile.yaml` 字段内，报告里逐项明示哪些没被包含）；复杂编辑建议走前端 Profiles 页。

---

## 5. Web：设置页与 composer

**设置页（Runtime Config → Profiles）**

- 列表：来源（`config` 注册项 / `root` / `layer`(user|project) / `default`，后端不产出 `builtin` 层）、状态（可解析 / 不可用 + 原因）、默认标注、行内「项目绑定」徽标。
- 编辑器：基础字段 / agent / overrides / 范围（层）四张卡片；保存走 PUT（带 `expected_mtime`，冲突 409，不合并）。
- 生命周期：创建（模板/复制/从会话固化）、导入导出、重命名、移动、删除——与 CLI/TUI 同一后端实现。
- 信任提示：工作区未信任时展示项目级提示词被扣留的说明与「信任并重载」动作。
- 项目绑定卡片（只读）：ref、工作区、来源、指针文件、目标目录、目标可用性、提示词扣留提示，以及会话内应用指引。

**composer**

- `/profile`（无参数）：弹出候选选择；`/profile <ref>`：直接切换（下一轮生效）。
- `/profile save-as <名称> [--to user|project]`：从当前会话固化差分。
- 只有宿主从 `GET /api/runtime/profiles` 的 `session_switch` 能力广告确认后端支持后，`/profile` 才注册；旧后端不注册，
  而不是注册后执行时报错。

---

## 6. 典型流程

### A. 从零建一个 profile 并设为默认

```powershell
aicli profile create coding --template coding --dry-run      # 1. 先看将生成的文件
aicli profile create coding --template coding --use          # 2. 落盘并注册到 config profiles.items
aicli profile validate coding                                # 3. error 级问题退出码 1
aicli profile show coding                                    # 4. 只读预览生效面与 token 估算
aicli profile create coding --template coding --set-default  # 5.（可选）设为默认，新会话生效
```

### B. 会话内临时切换 / 看差异 / 回基线

```
/profile status            # 现在是谁、来源、生效摘要
/profile diff review       # 切过去会变什么（tools/skills/mcp/prompt）
/profile use review        # 切换（下一轮生效；回执含 cache_notice 与 warnings）
/profile off               # 回到无 profile 基线
```

Web composer 里第 3 步写作 `/profile review`。切换不会改 `profiles.default_profile`，也不影响别的会话。

### C. 从当前会话固化一个 profile（save-as）

```
/profile save-as team-review --to project
```

差分与落盘在后端完成；报告会说明写了哪些字段、哪些内容没有固化（prompt、权限模式等不在 `profile.yaml` 字段内），
并且**不会**自动激活新 profile——需要切换时再 `/profile use team-review`（或 composer `/profile team-review`）。

### D. 分享给同事

```powershell
aicli profile export coding --out dist\coding.zip     # 只读导出（符号链接/*.tmp 不进包，有大小上限）
aicli profile import dist\coding.zip --to user        # 先 validate；不覆盖同名；不自动激活
```

### E. 项目绑定：仓库里留一个指针，协作者显式采用

```yaml
# 仓库内：<workspace>/.aicli/profile
profile: coding                                       # 只允许单段安全 ref；不得写路径
```

协作者的路径：设置页 Profiles → 「项目绑定（本工作区）」卡片（ref/来源/目标状态/未信任提示）→ 在会话内
显式应用（TUI `/profile use coding`、composer `/profile coding`）。绑定**不会**自动生效，也不会把
`coding` 变成默认 profile；指针文件非法或目标缺失时会明确报错，不会回退到 user/config/default。

### F. 未信任工作区的项目 profile

看到「项目级提示词未应用」提示时，收窄类声明（tools/skills/MCP）其实已经生效，只有 prompt/agent prompt 被扣留：

1. 设置页点「信任并重载」（写本机信任清单），或在 CLI 走 `/trust`；
2. 已在运行的会话执行 `/profile reload` 完整应用；
3. 撤销信任同样走 CLI `/trust`。

### G. CI 门禁

```powershell
aicli profile validate coding   # error 级问题退出码 1；warning 不影响退出码
```

---

## 7. 排错表

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| TUI 里 `/profile coding` 报「未知子命令」 | TUI 的 `/profile` 是子命令式，裸 ref 不是合法子命令 | 用 `/profile use coding`（composer 里才是裸 ref） |
| composer 里 `/profile use coding` 报「没有 coding」 | composer 把整串当 ref 解析（`use coding` 不是 ref） | 用 `/profile coding`；不确定就 `/profile` 打开候选 |
| `profile show <x>` 报未知 profile | ref 不在任何来源，或同名被更高优先级来源覆盖 | 先 `profile list` 看 `source`；同名优先级：config 注册项 > `profiles.root` > project 层 > user 层 |
| 切换了但行为没变 | 切换下一轮生效；在途 turn 仍走旧面 | 看回执 `effective_at`/`in_flight_turn`；必要时 `/profile reload` |
| 启动摘要说提示词被扣留 | 工作区未信任（D29） | 信任工作区 + `/profile reload` |
| 项目绑定卡片显示「绑定不可用」 | 指针文件非法（绝对路径/`..`/未知字段/嵌入对象）或目标 profile 不存在 | 按卡片 error 修正 `.aicli/profile`；不会自动回退 |
| 保存 profile 返回 409 | `expected_mtime` 与磁盘不一致（别处改过） | 刷新列表后重试（不合并、不静默覆盖） |
| 「写入未落在预期路径」 | 配置分层把 `profiles` 段路由到归属层 | `aicli config path` 确认实际文件 |
| 两个工作区互相串 profile | 服务端用了进程 cwd 而不是会话 workspace 计算 project 层 | 检查是否走 `LayerRootForWorkspace("project", workspace)`；列表端点需带 `workspace` |
| `agent.yaml` 里的 `tools` 不生效 / validate 报冲突 | 两个消费者形状冲突（解析器要 mapping、portable agentdef 要 list） | 工具策略改写到 `profile.yaml` 的 `agents.<id>.tools` 或 `agents/<id>/tools/policy.yaml` |
| `--profile auto` 启动即报错 | 纯交互式 `chat`/`agent stdio` 没有首轮提示词，不猜 | 用具体 profile，或改用 `chat --prompt/--message`、`exec` |

---

## 8. 常见误解

- **profile ≠ subagent 难度档位**：后者是 subagent 路由机制，与 `profile.yaml` 无关。
- **发现不是启用**：`profile list` 看到项目绑定、设置页卡片显示它可用，都不代表它已生效。
- **`--profile auto` 不是模糊匹配**：只按首轮提示词与规则表路由；命中不存在的 profile 按未知 profile 报错。
- **切换不会换模型/放宽权限**：provider/model/permission_mode 差异只报告（D30），需要显式改配置。
- **删掉绑定文件不会“回退到某个 profile”**：只表示无绑定；有会话已显式应用过的话，那个绑定仍在会话身份里，
  可用 `/profile off` 回到基线。
