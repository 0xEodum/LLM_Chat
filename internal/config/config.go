package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server  ServerConfig  `mapstructure:"server"`
	Logging LoggingConfig `mapstructure:"logging"`
	Chat    ChatConfig    `mapstructure:"chat"`
	LLM     LLMConfig     `mapstructure:"llm"`
	MCP     MCPConfig     `mapstructure:"mcp"`
}

type ServerConfig struct {
	Host         string        `mapstructure:"host"`
	Port         int           `mapstructure:"port"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type ChatConfig struct {
	MaxMessagesPerSession int `mapstructure:"max_messages_per_session"`
	ContextWindowSize     int `mapstructure:"context_window_size"`
}

type LLMConfig struct {
	Provider string `mapstructure:"provider"` // всегда "gemini" (MCP)
	BaseURL  string `mapstructure:"base_url"` // ← ДОБАВИТЬ ЭТО ПОЛЕ
	APIKey   string `mapstructure:"api_key"`
	Model    string `mapstructure:"model"`
}

type MCPConfig struct {
	ServerURL        string            `mapstructure:"server_url"`
	HTTPHeaders      map[string]string `mapstructure:"http_headers"`
	SystemPromptPath string            `mapstructure:"system_prompt_path"`
	MaxIterations    int               `mapstructure:"max_iterations"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")

	// Environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("CHAT_LLM")

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

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")

	// Chat defaults
	viper.SetDefault("chat.max_messages_per_session", 50)
	viper.SetDefault("chat.context_window_size", 20)

	// LLM defaults (только Gemini MCP)
	viper.SetDefault("llm.provider", "gemini")
	viper.SetDefault("llm.model", "gemini-2.5-flash")

	// MCP defaults
	viper.SetDefault("mcp.server_url", "http://localhost:8000/mcp")
	viper.SetDefault("mcp.system_prompt_path", "system_prompt.txt")
	viper.SetDefault("mcp.max_iterations", 10)
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

	if config.Chat.ContextWindowSize <= 0 {
		return fmt.Errorf("context window size must be positive: %d", config.Chat.ContextWindowSize)
	}

	if config.Chat.MaxMessagesPerSession <= 0 {
		return fmt.Errorf("max messages per session must be positive: %d", config.Chat.MaxMessagesPerSession)
	}

	if strings.TrimSpace(config.LLM.Model) == "" {
		return fmt.Errorf("LLM model is required")
	}

	// Проверяем MCP конфигурацию
	if strings.TrimSpace(config.MCP.ServerURL) == "" {
		return fmt.Errorf("MCP server URL is required")
	}

	if strings.TrimSpace(config.MCP.SystemPromptPath) == "" {
		return fmt.Errorf("MCP system prompt path is required")
	}

	if strings.TrimSpace(config.LLM.BaseURL) != "" {
		if !strings.HasPrefix(config.LLM.BaseURL, "http") {
			return fmt.Errorf("LLM base_url must start with http:// or https://")
		}
	}

	if config.MCP.MaxIterations <= 0 {
		return fmt.Errorf("MCP max iterations must be positive: %d", config.MCP.MaxIterations)
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

	sources["config_file"] = viper.ConfigFileUsed()
	sources["provider"] = "gemini (MCP)"
	sources["mcp_server"] = config.MCP.ServerURL
	sources["system_prompt"] = config.MCP.SystemPromptPath

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
