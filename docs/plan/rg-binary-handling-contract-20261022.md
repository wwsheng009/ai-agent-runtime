rg 二进制处理实测（rg 14.1.0 / WSL Ubuntu）

目录与文件（每个文件都含 "needle"，除特别标注）：
  a_before.dat   "needle before\nBIN\0DATA\n"     匹配在 NUL 之前的独立行
  b_after.dat    "BIN\0DATA\nneedle after\n"      匹配在 NUL 之后的独立行
  c_sameline_before.dat  "needle\0DATA\n"          匹配与 NUL 同一行、匹配在前
  d_sameline_after.dat   "DATA\0needle\n"          匹配与 NUL 同一行、匹配在后
  e_nomatch.dat  "DATA\0DATA\n"                    无匹配
  f_plain.txt    "needle plain\n"                  纯文本对照

=== 默认（无 --text / --binary）===
--files-with-matches : a_before.dat, c_sameline_before.dat, f_plain.txt
JSON                 : a_before.dat(1 行), c_sameline_before.dat("needle"), f_plain.txt
结论：默认把 NUL 视作文件结束。只能看到 NUL 之前那一行；
      匹配在 NUL 之后的文件完全不可见（b_after/d_sameline_after 消失）。

=== --binary（rg -uuu 语义）===
--files-with-matches : a_before, b_after, c_sameline_before, d_sameline_after, f_plain
JSON                 : 同上五个文件都有 match；
                       但 c_sameline_before 只输出 "needle"（截到 NUL），
                       d_sameline_after 的行号是 2 且只输出 "needle"
                       （NUL 之后的内容被当成下一行）
结论：--binary 让文件在 NUL 之后继续可搜索，但输出仍在 NUL 处截断。
      它解决的是「漏文件」，不是「拿到二进制内容」。

=== --text ===
JSON : a_before, b_after, c_sameline_before("needle\u0000DATA"),
       d_sameline_after("DATA\u0000needle"), f_plain
结论：--text 才真正把 NUL 当普通字节，整行原样输出。

=== 组合 ===
--binary + --text : 与单独 --text 相同（NUL 当字节，整行输出）
--text + --binary : 与 --binary 相同（后出现的 --binary 覆盖 --text）
结论：--binary 与 --text 是同一开关的两种取值，**后写者生效**，不是叠加。

=== 与 --json 的相互作用 ===
--json 不改变上述任何一个结论；--json 只是输出编码。
（注意：这与 files-without-match 不同——那边 rg 直接无视 --json。）

=== 对 files/count 模式 ===
--files-with-matches / --count 默认：只统计 NUL 之前的匹配
--count + --binary：bin.dat 也计入（a.txt:1 / bin.dat:1 / sub/s.txt:1）

=== rg 与 walker 的现状对照（本项目实测）===
legacy walker（builtin）：无论 unrestricted_level 为 0 还是 3，都把 NUL 当普通字节，
                          因为 searchcore/toolkit 的行读取器不检查 NUL。
legacy rg（rg 引擎）    ：level 0..3 都不加 --binary/--text，所以都只看 NUL 之前；
                          也就是说 -uuu 在 rg 路径上**也没有**放宽二进制。
core（当前）            ：无条件加 --text，等价于「永远把二进制当文本」。
结论：core 当前行为与两条 legacy 路径都不一致，是翻转时引入的回归。

---

## 补充实测（同一轮，决定最终实现）

### rg 默认对二进制文件有「第三种状态」

默认模式下，含 NUL 的文件既不算 match，也不算 without-match：

    files-with-matches  : 看不到它
    files-without-match : 也看不到它

而 files_without 的语义是「已证明该文件没有任何匹配行」。一个被整体跳过的
二进制文件从未被证明过任何事，所以它**不属于任何一页**。实现里必须显式排除，
否则会凭空断言一个搜索从未得出的结论。

### rg 的二进制判定依赖读缓冲，不是固定规则

`--binary` 下同一批文件（第 1 行是 needle，NUL 在第 N 个字节后），
默认模式能否看到第 1 行的匹配，取决于 N 与 rg 内部缓冲的关系：

    m001…m64k  默认看不到，--binary 能看到
    m128k      默认也能看到（匹配在首次缓冲内）
    m256k      默认也能看到

也就是说「默认能不能搜到 NUL 之前的行」不是文件的属性，而是
「匹配是否落在 rg 第一个读缓冲内」。这不能作为对外契约。

→ 因此 core 采用**固定 8 KiB 头部探测**：文件前 8 KiB 内有 NUL 即视为二进制。
   同一个文件无论页大小、搜索深度、匹配位置，分类都一致。

### rg --binary 会把 NUL 当作换行

    "DATA\0needle after"  →  line 1: DATA, line 2: needle after
    walker（本项目）      →  line 1: DATA␀needle after

同一份文件、同一个匹配，两个引擎给出的**行号不同**。`--binary` 只保证
「文件能被搜到」，不保证行结构一致。这不是我们要的契约。

→ 最终实现：`-uuu` 时给 rg 传 `--text`（保留原始行结构），
   引擎随后把输出里的 NUL 替换为 ␀。两个引擎因此逐字节一致：
   `after.dat:1: DATA␀needle after`。

### 修正前的真实状态（回归来源）

    legacy walker : 任何 level 都把 NUL 当普通字节（无条件读整行）
    legacy rg     : 任何 level 都不加 --binary/--text（只看 NUL 之前）
    core（翻转后）: 无条件 --text，等价于「永远把二进制当文本」

三者互不相同，且 core 的那一条是翻转时引入的回归——remote 从此会把
二进制内容当文本吐给模型。本轮把它收敛为一条契约，两个引擎共同遵守。
