//go:build win7compat

package aiclipaths

const (
	buildProfile = "win7"

	// 用户可见配置文件与主构建完全统一：win7compat 版本直接读写 config.yaml /
	// aicli.yaml，与标准 runtime-server / Web UI 保持同一文件，避免写入与读取
	// 不一致（如 UI 写回 config.yaml 而 win7 版读取 config.win7.yaml）。
	defaultConfigFileName         = "config.yaml"
	defaultCLIConfigFileName      = "aicli.yaml"

	// 运行时配置仍保留 win7 专属名；会话数据库统一使用主库（session_history.sqlite），
	// 使前端 Web 控制台能按工作目录分组展示用户的所有会话。
	defaultRuntimeConfigFileName  = "runtime.win7.yaml"
	defaultSessionHistoryFileName = "session_history.sqlite"
)
