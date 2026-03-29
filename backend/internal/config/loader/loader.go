package loader

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Loader loads configuration from files and environment variables
type Loader struct {
	configPath string
	configType string
}

// NewLoader creates a new configuration loader
func NewLoader(configPath string) *Loader {
	return &Loader{
		configPath: configPath,
		configType: strings.TrimPrefix(filepath.Ext(configPath), "."),
	}
}

// Load loads configuration from file and applies environment variable overrides
func (l *Loader) Load(cfg interface{}) error {
	if err := l.loadFromFile(cfg); err != nil {
		return fmt.Errorf("failed to load config from file: %w", err)
	}

	if err := l.loadFromEnv(cfg); err != nil {
		return fmt.Errorf("failed to load config from env: %w", err)
	}

	return nil
}

// loadFromFile loads configuration from YAML file
// Supports environment variable templates: ${VAR} and ${VAR:-default}
func (l *Loader) loadFromFile(cfg interface{}) error {
	if l.configPath == "" {
		return nil
	}

	data, err := os.ReadFile(l.configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// 替换环境变量模板
	data = []byte(l.expandEnvVars(string(data)))

	if l.configType == "yaml" || l.configType == "yml" {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("failed to unmarshal YAML: %w", err)
		}
	} else {
		return fmt.Errorf("unsupported config type: %s", l.configType)
	}

	return nil
}

// expandEnvVars 展开环境变量模板
// 支持语法：
// - ${VAR} - 直接引用环境变量
// - ${VAR:-default} - 带默认值的环境变量
// - $VAR - 简单环境变量（与系统行为一致）
func (l *Loader) expandEnvVars(content string) string {
	// 匹配 ${VAR} 或 ${VAR:-default} 格式
	re := regexp.MustCompile(`\$\{([^}:]+)(:-([^}]*))?\}`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		varName := parts[1]
		varValue := os.Getenv(varName)

		// 如果环境变量存在，返回环境变量的值
		if varValue != "" {
			return varValue
		}

		// 如果有默认值，返回默认值
		if len(parts) >= 4 && parts[3] != "" {
			return parts[3]
		}

		// 否则返回原始字符串（或者可以返回空字符串）
		return match
	})
}

// loadFromEnv 保留用于向后兼容，但主要逻辑已在 expandEnvVars 中处理
func (l *Loader) loadFromEnv(cfg interface{}) error {
	// 环境变量模板已在 loadFromFile 中处理
	// 这里可以添加额外的环境变量覆盖逻辑
	return nil
}

// LoadWithDefaults loads configuration with default values
func (l *Loader) LoadWithDefaults(cfg interface{}, defaults interface{}) error {
	if defaults != nil {
		if err := applyDefaults(cfg, defaults); err != nil {
			return fmt.Errorf("failed to apply defaults: %w", err)
		}
	}
	return l.Load(cfg)
}

// applyDefaults applies default values to configuration
func applyDefaults(cfg interface{}, defaults interface{}) error {
	// This is a simplified version. In production, you would use reflection
	// to properly merge defaults into the config

	return nil
}

// GetConfigPath returns the configuration file path
func (l *Loader) GetConfigPath() string {
	return l.configPath
}

// ConfigExists checks if the configuration file exists
func (l *Loader) ConfigExists() bool {
	if l.configPath == "" {
		return false
	}
	_, err := os.Stat(l.configPath)
	return !os.IsNotExist(err)
}
