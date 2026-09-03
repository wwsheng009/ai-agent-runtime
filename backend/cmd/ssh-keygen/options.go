package main

import (
	"fmt"
	"strings"
)

// 本文件实现 -O 证书选项解析，语义与 OpenSSH ssh-keygen.c 的
// add_cert_option / finalise_cert_exts 对齐：
//   - permit-* / no-* 标志在 finalise 阶段转换为 extensions；
//   - force-command / source-address / verify-required 转换为 critical options；
//   - extension:name[=value] / critical:name[=value] 添加任意选项。

// certOptionFlags 对应 OpenSSH 的 certflags_flags 位。
type certOptionFlags uint32

const (
	flagX11Forwarding certOptionFlags = 1 << iota
	flagAgentForwarding
	flagPortForwarding
	flagPTY
	flagUserRC
	flagNoTouchRequired
	flagVerifyRequired
)

// certOptions 累积 -O 解析结果。
type certOptions struct {
	flags        certOptionFlags
	forceCommand string
	sourceAddr   string
	extensions   map[string]string
	criticals    map[string]string
}

func newCertOptions() *certOptions {
	return &certOptions{
		// 与 OpenSSH 一致：默认启用 X11/agent/port forwarding、pty、user-rc
		flags:      flagX11Forwarding | flagAgentForwarding | flagPortForwarding | flagPTY | flagUserRC,
		extensions: map[string]string{},
		criticals:  map[string]string{},
	}
}

// addCertOption 解析单个 -O 选项。
func (c *certOptions) addCertOption(opt string) error {
	switch {
	case strings.EqualFold(opt, "clear"):
		c.flags = 0
	case strings.EqualFold(opt, "permit-x11-forwarding"):
		c.flags |= flagX11Forwarding
	case strings.EqualFold(opt, "no-x11-forwarding"):
		c.flags &^= flagX11Forwarding
	case strings.EqualFold(opt, "permit-agent-forwarding"):
		c.flags |= flagAgentForwarding
	case strings.EqualFold(opt, "no-agent-forwarding"):
		c.flags &^= flagAgentForwarding
	case strings.EqualFold(opt, "permit-port-forwarding"):
		c.flags |= flagPortForwarding
	case strings.EqualFold(opt, "no-port-forwarding"):
		c.flags &^= flagPortForwarding
	case strings.EqualFold(opt, "permit-pty"):
		c.flags |= flagPTY
	case strings.EqualFold(opt, "no-pty"):
		c.flags &^= flagPTY
	case strings.EqualFold(opt, "permit-user-rc"):
		c.flags |= flagUserRC
	case strings.EqualFold(opt, "no-user-rc"):
		c.flags &^= flagUserRC
	case strings.EqualFold(opt, "touch-required"):
		c.flags &^= flagNoTouchRequired
	case strings.EqualFold(opt, "no-touch-required"):
		c.flags |= flagNoTouchRequired
	case strings.EqualFold(opt, "verify-required"):
		c.flags |= flagVerifyRequired
	case strings.EqualFold(opt, "no-verify-required"):
		c.flags &^= flagVerifyRequired
	case strings.HasPrefix(opt, "force-command="):
		v := strings.TrimPrefix(opt, "force-command=")
		if v == "" {
			return fmt.Errorf("empty force-command option")
		}
		if c.forceCommand != "" {
			return fmt.Errorf("force-command already specified")
		}
		c.forceCommand = v
	case strings.HasPrefix(opt, "source-address="):
		v := strings.TrimPrefix(opt, "source-address=")
		if v == "" {
			return fmt.Errorf("empty source-address option")
		}
		if c.sourceAddr != "" {
			return fmt.Errorf("source-address already specified")
		}
		if err := validateSourceAddressList(v); err != nil {
			return fmt.Errorf("invalid source-address list %q: %v", v, err)
		}
		c.sourceAddr = v
	case strings.HasPrefix(opt, "extension:"):
		name, val, err := splitCertOptionValue(strings.TrimPrefix(opt, "extension:"))
		if err != nil {
			return fmt.Errorf("invalid extension option %q: %v", opt, err)
		}
		if c.extensions[name] != "" {
			return fmt.Errorf("extension %q already specified", name)
		}
		c.extensions[name] = val
	case strings.HasPrefix(opt, "critical:"):
		name, val, err := splitCertOptionValue(strings.TrimPrefix(opt, "critical:"))
		if err != nil {
			return fmt.Errorf("invalid critical option %q: %v", opt, err)
		}
		if c.criticals[name] != "" {
			return fmt.Errorf("critical option %q already specified", name)
		}
		c.criticals[name] = val
	default:
		return fmt.Errorf("unsupported certificate option %q", opt)
	}
	return nil
}

// splitCertOptionValue 拆分 "name" 或 "name=value"。
func splitCertOptionValue(s string) (name, value string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("empty option name")
	}
	if idx := strings.Index(s, "="); idx >= 0 {
		name, value = s[:idx], s[idx+1:]
	} else {
		name = s
	}
	if name == "" {
		return "", "", fmt.Errorf("empty option name")
	}
	return name, value, nil
}

// finalise 把标志转换为 extensions/criticals（对应 OpenSSH finalise_cert_exts）。
func (c *certOptions) finalise() {
	if c.forceCommand != "" {
		c.criticals["force-command"] = c.forceCommand
	}
	if c.sourceAddr != "" {
		c.criticals["source-address"] = c.sourceAddr
	}
	if c.flags&flagVerifyRequired != 0 {
		c.criticals["verify-required"] = ""
	}
	if c.flags&flagX11Forwarding != 0 {
		c.extensions["permit-X11-forwarding"] = ""
	}
	if c.flags&flagAgentForwarding != 0 {
		c.extensions["permit-agent-forwarding"] = ""
	}
	if c.flags&flagPortForwarding != 0 {
		c.extensions["permit-port-forwarding"] = ""
	}
	if c.flags&flagPTY != 0 {
		c.extensions["permit-pty"] = ""
	}
	if c.flags&flagUserRC != 0 {
		c.extensions["permit-user-rc"] = ""
	}
	if c.flags&flagNoTouchRequired != 0 {
		c.extensions["no-touch-required"] = ""
	}
}

// validateSourceAddressList 校验逗号分隔的地址/CIDR 列表。
// 与 OpenSSH addr_match_cidr_list 类似（此处只做基本格式校验）。
func validateSourceAddressList(list string) error {
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("empty address in list")
		}
		// 简单校验：host 或 host/mask 形式
		hostPart := part
		if idx := strings.Index(part, "/"); idx >= 0 {
			hostPart = part[:idx]
			mask := part[idx+1:]
			if mask == "" {
				return fmt.Errorf("empty mask in %q", part)
			}
		}
		if hostPart == "" {
			return fmt.Errorf("empty address in %q", part)
		}
	}
	return nil
}
