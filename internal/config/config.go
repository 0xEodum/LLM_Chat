package config

import (
	"LLM_Chat/pkg/llm/providers"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Logging  LoggingConfig  `mapstructure:"logging"`
	Chat     ChatConfig     `mapstructure:"chat"`
	LLM      LLMConfig      `mapstructure:"llm"`
	MCP      MCPConfig      `mapstructure:"mcp"`
}

type ServerConfig struct {
	Host         string        `mapstructure:"host"`
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type DatabaseConfig struct {
	URL             string        `mapstructure:"url"`
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	Database        string        `mapstructure:"database"`
	Username        string        `mapstructure:"username"`
	Password        string        `mapstructure:"password"`
	SSLMode         string        `mapstructure:"ssl_mode"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	MigrationsPath  string        `mapstructure:"migrations_path"`
	AutoMigrate     bool          `mapstructure:"auto_migrate"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type ChatConfig struct {
	MaxMessagesPerSession   int     `mapstructure:"max_messages_per_session"`
	ContextWindowSize       int     `mapstructure:"context_window_size"`
	MessageCompressionRatio float64 `mapstructure:"message_compression_ratio"`
	SummaryCompressionRatio float64 `mapstructure:"summary_compression_ratio"`
	MinMessagesInWindow     int     `mapstructure:"min_messages_in_window"`
}

type LLMConfig struct {
	Provider string `mapstructure:"provider"` // всегда "gemini" (MCP)
	BaseURL  string `mapstructure:"base_url"`
	APIKey   string `mapstructure:"api_key"`
	Model    string `mapstructure:"model"`
}

type MCPConfig struct {
	ServerURL        string            `mapstructure:"server_url"`
	HTTPHeaders      map[string]string `mapstructure:"http_headers"`
	SystemPromptPath string            `mapstructure:"system_prompt_path"`
	MaxIterations    int               `mapstructure:"max_iterations"`
}

func (cfg *Config) ToProviderConfig() providers.Config {
	return providers.Config{
		Provider: cfg.LLM.Provider,
		BaseURL:  cfg.LLM.BaseURL,
		APIKey:   cfg.LLM.APIKey,
		Model:    cfg.LLM.Model,
		Timeout:  60 * time.Second, // или cfg.LLM.Timeout если добавить
	}
}

// ToMCPConfig создает MCP конфигурацию
func (cfg *Config) ToMCPConfig() providers.MCPProviderConfig {
	return providers.MCPProviderConfig{
		ServerURL:        cfg.MCP.ServerURL,
		SystemPromptPath: cfg.MCP.SystemPromptPath,
		MaxIterations:    cfg.MCP.MaxIterations,
		HTTPHeaders:      cfg.MCP.HTTPHeaders,
	}
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")

	// Environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("CHAT_LLM")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Устанавливаем значения по умолчанию
	setDefaults()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Обработка API ключа для Gemini
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		config.LLM.APIKey = getGeminiAPIKey()
	}

	// Построение URL базы данных если не задан
	if strings.TrimSpace(config.Database.URL) == "" {
		config.Database.URL = buildDatabaseURL(config.Database)
	}

	// Валидация критических параметров
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

func setDefaults() {
	// Server defaults
	viper.SetDefault("server.host", "localhost")
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.read_timeout", "30s")
	viper.SetDefault("server.write_timeout", "30s")

	// Database defaults
	viper.SetDefault("database.host", "localhost")
	viper.SetDefault("database.port", 5432)
	viper.SetDefault("database.database", "chat_llm")
	viper.SetDefault("database.username", "postgres")
	viper.SetDefault("database.password", "postgres")
	viper.SetDefault("database.ssl_mode", "disable")
	viper.SetDefault("database.max_open_conns", 25)
	viper.SetDefault("database.max_idle_conns", 5)
	viper.SetDefault("database.conn_max_lifetime", "5m")
	viper.SetDefault("database.migrations_path", "./migrations")
	viper.SetDefault("database.auto_migrate", true)

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")

	// Chat defaults with multi-level compression
	viper.SetDefault("chat.max_messages_per_session", 1000) // Увеличено для БД
	viper.SetDefault("chat.context_window_size", 20)
	viper.SetDefault("chat.message_compression_ratio", 0.3) // 30%
	viper.SetDefault("chat.summary_compression_ratio", 0.8) // 80%
	viper.SetDefault("chat.min_messages_in_window", 5)

	// LLM defaults (только Gemini MCP)
	viper.SetDefault("llm.provider", "gemini")
	viper.SetDefault("llm.model", "gemini-2.5-flash")

	// MCP defaults
	viper.SetDefault("mcp.server_url", "http://localhost:8000/mcp")
	viper.SetDefault("mcp.system_prompt_path", "system_prompt.txt")
	viper.SetDefault("mcp.max_iterations", 10)
}

func buildDatabaseURL(dbConfig DatabaseConfig) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		dbConfig.Username,
		dbConfig.Password,
		dbConfig.Host,
		dbConfig.Port,
		dbConfig.Database,
		dbConfig.SSLMode,
	)
}

func getGeminiAPIKey() string {
	// Пробуем специфичные переменные для Gemini
	if key := viper.GetString("GEMINI_API_KEY"); key != "" {
		return key
	}
	return viper.GetString("LLM_API_KEY") // fallback
}

func validateConfig(config *Config) error {
	// Проверяем провайдер (должен быть только gemini)
	if strings.ToLower(config.LLM.Provider) != "gemini" {
		return fmt.Errorf("unsupported LLM provider: %s, only 'gemini' is supported", config.LLM.Provider)
	}

	// Проверяем наличие API ключа
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		return fmt.Errorf(`Gemini API key is required. 

Рекомендуемый способ - укажите ключ в config.yaml:
llm:
  api_key: "your_gemini_api_key_here"

Альтернативно, используйте переменную окружения: %s`,
			strings.Join(GetGeminiEnvVars(), " или "))
	}

	// Проверяем базовые параметры сервера
	if config.Server.Port <= 0 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", config.Server.Port)
	}

	// Проверяем конфигурацию чата
	if config.Chat.ContextWindowSize <= 0 {
		return fmt.Errorf("context window size must be positive: %d", config.Chat.ContextWindowSize)
	}

	if config.Chat.MaxMessagesPerSession <= 0 {
		return fmt.Errorf("max messages per session must be positive: %d", config.Chat.MaxMessagesPerSession)
	}

	if config.Chat.MessageCompressionRatio <= 0 || config.Chat.MessageCompressionRatio >= 1 {
		return fmt.Errorf("message compression ratio must be between 0 and 1: %f", config.Chat.MessageCompressionRatio)
	}

	if config.Chat.SummaryCompressionRatio <= 0 || config.Chat.SummaryCompressionRatio >= 1 {
		return fmt.Errorf("summary compression ratio must be between 0 and 1: %f", config.Chat.SummaryCompressionRatio)
	}

	// Проверяем конфигурацию LLM
	if strings.TrimSpace(config.LLM.Model) == "" {
		return fmt.Errorf("LLM model is required")
	}

	if strings.TrimSpace(config.LLM.BaseURL) != "" {
		if !strings.HasPrefix(config.LLM.BaseURL, "http") {
			return fmt.Errorf("LLM base_url must start with http:// or https://")
		}
	}

	// Проверяем MCP конфигурацию
	if strings.TrimSpace(config.MCP.ServerURL) == "" {
		return fmt.Errorf("MCP server URL is required")
	}

	if strings.TrimSpace(config.MCP.SystemPromptPath) == "" {
		return fmt.Errorf("MCP system prompt path is required")
	}

	if config.MCP.MaxIterations <= 0 {
		return fmt.Errorf("MCP max iterations must be positive: %d", config.MCP.MaxIterations)
	}

	// Проверяем конфигурацию базы данных
	if strings.TrimSpace(config.Database.URL) == "" {
		return fmt.Errorf("database URL is required")
	}

	if config.Database.MaxOpenConns <= 0 {
		return fmt.Errorf("database max_open_conns must be positive: %d", config.Database.MaxOpenConns)
	}

	if config.Database.MaxIdleConns < 0 {
		return fmt.Errorf("database max_idle_conns cannot be negative: %d", config.Database.MaxIdleConns)
	}

	return nil
}

// GetConfigSource возвращает информацию о том, откуда взяты настройки
func GetConfigSource(config *Config) map[string]string {
	sources := make(map[string]string)

	// Проверяем, откуда взят API ключ
	viperAPIKey := viper.GetString("llm.api_key")
	envAPIKey := getGeminiAPIKey()

	if viperAPIKey != "" {
		sources["api_key"] = "config.yaml"
	} else if envAPIKey != "" {
		if viper.GetString("GEMINI_API_KEY") != "" {
			sources["api_key"] = "environment variable CHAT_LLM_GEMINI_API_KEY"
		} else {
			sources["api_key"] = "environment variable CHAT_LLM_LLM_API_KEY"
		}
	} else {
		sources["api_key"] = "not set"
	}

	// Проверяем источник настроек базы данных
	if viper.GetString("database.url") != "" {
		sources["database"] = "config.yaml (url)"
	} else {
		sources["database"] = "config.yaml (host/port/database)"
	}

	sources["config_file"] = viper.ConfigFileUsed()
	sources["provider"] = "gemini (MCP)"
	sources["mcp_server"] = config.MCP.ServerURL
	sources["system_prompt"] = config.MCP.SystemPromptPath
	sources["database_url"] = config.Database.URL

	return sources
}

// GetGeminiEnvVars возвращает рекомендуемые переменные окружения для Gemini
func GetGeminiEnvVars() []string {
	return []string{
		"CHAT_LLM_GEMINI_API_KEY",
		"CHAT_LLM_LLM_API_KEY",
	}
}

// GetMCPEnvVars возвращает переменные окружения для MCP
func GetMCPEnvVars() []string {
	return []string{
		"CHAT_LLM_MCP_SERVER_URL",
		"CHAT_LLM_MCP_SYSTEM_PROMPT_PATH",
		"CHAT_LLM_MCP_MAX_ITERATIONS",
	}
}

// GetDatabaseEnvVars возвращает переменные окружения для БД
func GetDatabaseEnvVars() []string {
	return []string{
		"CHAT_LLM_DATABASE_URL",
		"CHAT_LLM_DATABASE_HOST",
		"CHAT_LLM_DATABASE_PORT",
		"CHAT_LLM_DATABASE_DATABASE",
		"CHAT_LLM_DATABASE_USERNAME",
		"CHAT_LLM_DATABASE_PASSWORD",
		"CHAT_LLM_DATABASE_SSL_MODE",
	}
}
