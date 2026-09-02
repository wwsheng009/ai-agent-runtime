package sshclient

import (
	"time"
)

// StrictHostKeyChecking 模式常量。
const (
	StrictModeYes       = "yes"
	StrictModeAcceptNew = "accept-new"
	StrictModeNo        = "no"
)

// Options 汇集 SSH 连接与认证的完整参数，由 CLI 解析 + 配置文件合并后填充。
// 零值使用默认值，通过 Merge 方法合并文件配置。
type Options struct {
	// User 登录用户名。默认 OS 当前用户。
	User string
	// Host 目标主机名或 IP（不含端口）。
	Host string
	// Port SSH 端口，默认 22。
	Port int

	// Password 认证密码。
	Password    string
	PasswordSet bool

	// IdentityFiles 私钥文件路径列表（CLI -i 或 config IdentityFile）。
	IdentityFiles []string
	// IdentitiesOnly 是否仅使用显式指定的密钥（跳过 ssh-agent 默认 key 提供）。
	IdentitiesOnly bool
	// CertificateFiles 显式证书文件路径列表（对应 OpenSSH CertificateFile 指令）。
	CertificateFiles []string

	// PreferredAuthentications 认证方法优先级（逗号分隔，如 "publickey,password"）。
	PreferredAuthentications string

	// StrictHostKeyChecking known_hosts 校验模式。
	//   yes         — 严格，拒绝未知主机密钥
	//   accept-new  — 默认，首次自动添加并记录
	//   no          — 跳过校验（输出警告）
	StrictHostKeyChecking string
	// UserKnownHostsFile known_hosts 文件路径，默认 ~/.ssh/known_hosts。
	UserKnownHostsFile string

	// ConnectTimeout 拨号超时，默认 30s。
	ConnectTimeout time.Duration
	// ServerAliveInterval 保活间隔，0 表示不启用。
	ServerAliveInterval time.Duration
	// ServerAliveCountMax 保活失败最大次数，默认 3。
	ServerAliveCountMax int

	// HostKeyAlgorithms 主机密钥算法白名单，空表示使用 x/crypto 默认。
	HostKeyAlgorithms []string

	// Compression 是否请求压缩。
	Compression bool

	// LogLevel 日志级别（QUIET/FATAL/ERROR/INFO/VERBOSE/DEBUG），默认 INFO。
	LogLevel string

	// ConfigFile 显式指定的 ssh_config 路径，空表示 ~/.ssh/config。
	ConfigFile string

	// Network 拨号网络（"" 自动 / "tcp4" / "tcp6"），对应 -4/-6。
	Network string

	// ProxyCommand 代理命令（OpenSSH ProxyCommand 指令），非空时通过子进程建立连接。
	ProxyCommand string

	// OriginalHost 是命令行传入的原始主机名（或别名），用于 ProxyCommand 令牌 %n 展开。
	// 在 ApplyConfig 之前由 main 设置。
	OriginalHost string

	// Verbose 是否输出调试信息（-v）。
	Verbose bool
	// Quiet 是否抑制警告与横幅（-q）。
	Quiet bool
}

// Defaults 返回一个填充了合理默认值的 Options。
func Defaults() *Options {
	return &Options{
		Port:                    22,
		StrictHostKeyChecking:   StrictModeAcceptNew,
		ConnectTimeout:          30 * time.Second,
		ServerAliveCountMax:     3,
		LogLevel:                "INFO",
		UserKnownHostsFile:      "~/.ssh/known_hosts",
	}
}

// ApplyConfig 将 ssh_config 解析结果应用到 Options 中未显式设置（零值/默认）的字段。
// 优先级：CLI 已设置 > config 文件 > 默认值。
func (o *Options) ApplyConfig(c *ResolvedConfig) {
	if o.User == "" && c.User != "" {
		o.User = c.User
	}
	// HostName 覆盖连接目标（OpenSSH 语义）：config 中 Host 模式匹配后，实际连接目标是
	// HostName；命令行/配置文件中的原始别名保留在 o.OriginalHost（供 ProxyCommand %n 令牌展开）。
	if c.HostName != "" && o.Host != c.HostName {
		o.Host = c.HostName
	}
	if o.Port == 22 && c.Port != 0 {
		o.Port = c.Port
	}
	if len(o.IdentityFiles) == 0 && len(c.IdentityFiles) > 0 {
		o.IdentityFiles = c.IdentityFiles
	}
	if !o.IdentitiesOnly && c.IdentitiesOnly {
		o.IdentitiesOnly = true
	}
	if len(o.CertificateFiles) == 0 && len(c.CertificateFiles) > 0 {
		o.CertificateFiles = make([]string, len(c.CertificateFiles))
		copy(o.CertificateFiles, c.CertificateFiles)
	}

	if o.PreferredAuthentications == "" && c.PreferredAuthentications != "" {
		o.PreferredAuthentications = c.PreferredAuthentications
	}
	if o.StrictHostKeyChecking == StrictModeAcceptNew && c.StrictHostKeyChecking != "" {
		o.StrictHostKeyChecking = c.StrictHostKeyChecking
	}
	if o.UserKnownHostsFile == "~/.ssh/known_hosts" && c.UserKnownHostsFile != "" {
		o.UserKnownHostsFile = c.UserKnownHostsFile
	}
	if o.ConnectTimeout == 30*time.Second && c.ConnectTimeout > 0 {
		o.ConnectTimeout = c.ConnectTimeout
	}
	if o.ServerAliveInterval == 0 && c.ServerAliveInterval > 0 {
		o.ServerAliveInterval = c.ServerAliveInterval
	}
	if o.ServerAliveCountMax == 3 && c.ServerAliveCountMax > 0 {
		o.ServerAliveCountMax = c.ServerAliveCountMax
	}
	if len(o.HostKeyAlgorithms) == 0 && len(c.HostKeyAlgorithms) > 0 {
		o.HostKeyAlgorithms = c.HostKeyAlgorithms
	}
	if !o.Compression && c.Compression {
		o.Compression = true
	}
	if o.LogLevel == "INFO" && c.LogLevel != "" {
		o.LogLevel = c.LogLevel
	}
	if o.ProxyCommand == "" && c.ProxyCommand != "" {
		o.ProxyCommand = c.ProxyCommand
	}
}

// IsVerbose 是否应输出详细日志。
func (o *Options) IsVerbose() bool { return o.Verbose }

// IsQuiet 是否应抑制非关键输出。
func (o *Options) IsQuiet() bool { return o.Quiet }