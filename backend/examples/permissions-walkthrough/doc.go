// Package permissionswalkthrough 是「可执行示例工程」的包壳。
//
// 示例主体：
//
//   - .aicli/permissions.yaml —— 覆盖手册关键能力的项目级权限文件；
//   - walkthrough_test.go     —— 用真实 internal/policy.Engine 装载上面的文件
//     并逐条断言决策表（Type/Stage/Reason）；
//   - README.md               —— 一分钟上手、示例↔手册对照表、亲手验证与截图步骤。
//
// 本目录位于 backend 模块内，才能合法导入 internal/policy（Go 的 internal
// 可见性规则）；运行方式见 README。
package permissionswalkthrough
