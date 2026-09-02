// Package sshclient 提供与 OpenSSH 客户端兼容的 SSH 连接、认证与传输能力。
//
// 该包被 cmd/ssh-client 与 cmd/sftp-client 两个 CLI 程序共享，职责包括：
//   - 认证策略编排（密码 / 公钥 / ssh-agent / keyboard-interactive）
//   - OpenSSH ~/.ssh/config 解析（常用指令子集）
//   - known_hosts 主机密钥校验（strict / accept-new / insecure）
//   - 连接建立、远程命令执行、SFTP 会话封装
//
// 设计约束：
//   - 不引入完整 OpenSSH 实现，仅依赖 golang.org/x/crypto/ssh 与 github.com/pkg/sftp。
//   - 交互式 PTY 逻辑不在此包实现，由调用方（cmd/ssh-client）负责。
package sshclient
