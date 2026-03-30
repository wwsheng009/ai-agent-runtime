# System Skills

此目录只放 **系统级 / 平台级** skills。

适合放在这里的 skill：

- 通用文件查看
- 通用 shell 执行
- 通用 URL 获取
- runtime / monitoring / smoke 验证

不适合放在这里的 skill：

- ABAP / ERP
- coding pack
- 企业流程专用组件
- 项目私有 skill pack

这些垂直或业务域 skills 应放在外部目录，并通过以下方式加载：

- 配置：`skills_runtime.extra_skill_dirs`
- CLI：`aicli chat --skills-dir <dir>`

默认系统 skills 目录由 `skills_runtime.skill_dir` 指定。

更完整的来源、优先级和持久化规则见：

- `docs/skill_runtime/skill_sources_and_persistence.md`
