package logger

// LogConfig holds logging configuration.
// Mirrors the fields needed from the gateway config.LogConfig.
type LogConfig struct {
	Level         string                     `yaml:"level" mapstructure:"level"`
	Format        string                     `yaml:"format" mapstructure:"format"`
	Output        string                     `yaml:"output" mapstructure:"output"`
	FilePath      string                     `yaml:"file_path" mapstructure:"file_path"`
	MaxSize       int                        `yaml:"max_size" mapstructure:"max_size"`
	MaxBackups    int                        `yaml:"max_backups" mapstructure:"max_backups"`
	MaxAge        int                        `yaml:"max_age" mapstructure:"max_age"`
	Compress      bool                       `yaml:"compress" mapstructure:"compress"`
	EnableConsole bool                       `yaml:"enable_console" mapstructure:"enable_console"`
	Modules       map[string]ModuleLogConfig `yaml:"modules" mapstructure:"modules"`
}

// ModuleLogConfig 模块级别日志配置
type ModuleLogConfig struct {
	Level    string `yaml:"level" mapstructure:"level"`
	Enabled  *bool  `yaml:"enabled" mapstructure:"enabled"`
	FilePath string `yaml:"file_path" mapstructure:"file_path"`
}
