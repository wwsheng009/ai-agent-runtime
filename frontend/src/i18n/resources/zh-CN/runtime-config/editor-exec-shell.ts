// Shell 执行环境配置域的文案（`editor.shell.*`）。
//
// 与 editor-shell.ts 区分：那个文件是「后端配置工作台」页面外壳的文案，
// 这里说的是「命令用哪个 shell 跑」这件事。

export const zhRuntimeConfigEditorExecShell = {
  shell: {
    title: "Shell 执行环境",
    description:
      "决定工具执行命令时用哪个 shell。Windows 上选中 POSIX shell 后，模型可以写 bash 语法而不是 PowerShell，跨平台脚本无需分叉。",
    enabled: "已启用",
    disabled: "已关闭",
    autoDetectHint:
      "开启时按候选链逐个探测，第一个探针通过的即为生效 shell；关闭后只认下方「强制路径」。",
    strictProbeHint:
      "严格模式下探针失败不会退回上一个候选，而是直接报错；建议保持开启，避免静默降级成错误方言。",
    badges: {
      autoOn: "自动检测",
      autoOff: "手动指定",
      candidateCount: "{{count}} 个候选",
      pinned: "固定路径",
      builtin: "内置链",
    },
    fields: {
      autoDetect: "自动检测",
      strictProbe: "严格探针",
      path: "强制路径",
      probeTimeout: "探针超时",
      scriptDelivery: "脚本传递",
    },
    placeholders: {
      path: "C:\\Program Files\\Git\\bin\\bash.exe",
      probeTimeout: "3s",
    },
    deliveryOptions: {
      stdin: "stdin：脚本写入标准输入，不受命令行长度限制",
      argv: "argv：脚本直接拼进命令行参数",
    },
    prefer: {
      title: "候选顺序",
      hint: "由上到下依次尝试",
      empty: "尚未配置候选链，将使用内置缺省链（git-bash → msys-bash → busybox → pwsh）。",
      addLabel: "可加入：",
      moveUp: "上移",
      moveDown: "下移",
      remove: "移出候选链",
      current: "生效链：{{chain}}",
      builtin: "内置缺省链",
    },
    probe: {
      title: "实际探测",
      description:
        "用当前草稿的配置真跑一次探针，看机器上到底哪个 shell 能用。不会保存配置。",
      action: "检测",
      running: "检测中…",
      idle: "还没有探测结果，点右上角「检测」跑一次。",
      effective: "生效：{{shell}}（{{path}}）",
      effectiveMeta: "方言 {{dialect}} · 来源 {{source}} · 耗时 {{ms}}ms",
    },
  },
} as const;
