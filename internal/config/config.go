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
	Provider         string            `mapstructure:"provider"` // новое поле: "openrouter", "gemini"
	BaseURL          string            `mapstructure:"base_url"`
	Model            string            `mapstructure:"model"`
	APIKey           string            `mapstructure:"api_key"`
	ServerURL        string            `mapstructure:"server_url"`
	HTTPHeaders      map[string]string `mapstructure:"http_headers"`
	SystemPromptPath string            `mapstructure:"system_prompt_path"`
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
	config.LLM.HTTPHeaders = viper.GetStringMapString("llm.http_headers")

	// Обработка API ключей для разных провайдеров
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		config.LLM.APIKey = getAPIKeyForProvider(config.LLM.Provider)
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

	// LLM defaults
	viper.SetDefault("llm.provider", "openrouter")
	viper.SetDefault("llm.base_url", "https://openrouter.ai/api/v1")
	viper.SetDefault("llm.model", "google/gemma-3-27b-it:free")
	viper.SetDefault("llm.server_url", "http://localhost:8000/mcp")
	viper.SetDefault("llm.http_headers", map[string]string{})
	viper.SetDefault("llm.system_prompt_path", "system_prompt.txt")
}

func getAPIKeyForProvider(provider string) string {
	switch strings.ToLower(provider) {
	case "openrouter":
		return viper.GetString("LLM_API_KEY") // CHAT_LLM_LLM_API_KEY
	case "gemini":
		// Пробуем специфичные переменные для Gemini
		if key := viper.GetString("GEMINI_API_KEY"); key != "" {
			return key
		}
		return viper.GetString("LLM_API_KEY") // fallback
	default:
		return viper.GetString("LLM_API_KEY")
	}
}

func validateConfig(config *Config) error {
	// Проверяем провайдер
	supportedProviders := []string{"openrouter", "gemini", "gemini-mcp"}
	providerValid := false
	for _, supported := range supportedProviders {
		if strings.ToLower(config.LLM.Provider) == supported {
			providerValid = true
			break
		}
	}
	if !providerValid {
		return fmt.Errorf("unsupported LLM provider: %s, supported: %v",
			config.LLM.Provider, supportedProviders)
	}

	// Проверяем наличие API ключа
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		return fmt.Errorf(`LLM API key is required. 

Рекомендуемый способ - укажите ключ в config.yaml:
llm:
  provider: "%s"
  api_key: "your_api_key_here"

Альтернативно, используйте переменную окружения: %s`,
			config.LLM.Provider,
			strings.Join(GetProviderSpecificEnvVars(config.LLM.Provider), " или "))
	}

	// Проверяем базовые параметры
	if config.Server.Port <= 0 || config.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", config.Server.Port)
	}

	if config.Chat.ContextWindowSize <= 0 {
		return fmt.Errorf("context window size must be positive: %d", config.Chat.ContextWindowSize)
	}

	if config.Chat.MaxMessagesPerSession <= 0 {
		return fmt.Errorf("max messages per session must be positive: %d", config.Chat.MaxMessagesPerSession)
	}

	if strings.TrimSpace(config.LLM.BaseURL) == "" {
		return fmt.Errorf("LLM base URL is required")
	}

	if strings.TrimSpace(config.LLM.Model) == "" {
		return fmt.Errorf("LLM model is required")
	}

	if strings.TrimSpace(config.LLM.ServerURL) == "" {
		return fmt.Errorf("LLM server URL is required")
	}

	if strings.TrimSpace(config.LLM.SystemPromptPath) == "" {
		return fmt.Errorf("system prompt path is required")
	}

	// Валидация специфичная для провайдеров
	if err := validateProviderSpecific(config); err != nil {
		return err
	}

	return nil
}

func validateProviderSpecific(config *Config) error {
	switch strings.ToLower(config.LLM.Provider) {
	case "openrouter":
		// Для OpenRouter проверяем, что URL правильный
		if !strings.Contains(config.LLM.BaseURL, "openrouter") {
			return fmt.Errorf("base URL should contain 'openrouter' for OpenRouter provider")
		}
	case "gemini", "gemini-mcp":
		// Для Gemini проверяем, что URL содержит правильный эндпоинт
		if !strings.Contains(config.LLM.BaseURL, "google") && !strings.Contains(config.LLM.BaseURL, "gemini") {
			return fmt.Errorf("base URL should contain 'google' or 'gemini' for Gemini provider")
		}
		// Проверяем модель
		if !strings.Contains(config.LLM.Model, "gemini") {
			return fmt.Errorf("model should contain 'gemini' for Gemini provider")
		}
	}
	return nil
}

// GetConfigSource возвращает информацию о том, откуда взяты настройки
func GetConfigSource(config *Config) map[string]string {
	sources := make(map[string]string)

	// Проверяем, откуда взят API ключ
	viperAPIKey := viper.GetString("llm.api_key")
	envAPIKey := getAPIKeyForProvider(config.LLM.Provider)

	if viperAPIKey != "" {
		sources["api_key"] = "config.yaml"
	} else if envAPIKey != "" {
		switch strings.ToLower(config.LLM.Provider) {
		case "gemini", "gemini-mcp":
			if viper.GetString("GEMINI_API_KEY") != "" {
				sources["api_key"] = "environment variable CHAT_LLM_GEMINI_API_KEY"
			} else {
				sources["api_key"] = "environment variable CHAT_LLM_LLM_API_KEY"
			}
		default:
			sources["api_key"] = "environment variable CHAT_LLM_LLM_API_KEY"
		}
	} else {
		sources["api_key"] = "not set"
	}

	sources["config_file"] = viper.ConfigFileUsed()
	sources["provider"] = config.LLM.Provider

	return sources
}

// GetProviderSpecificEnvVars возвращает рекомендуемые переменные окружения для провайдера
func GetProviderSpecificEnvVars(provider string) []string {
	switch strings.ToLower(provider) {
	case "openrouter":
		return []string{
			"CHAT_LLM_LLM_API_KEY",
		}
	case "gemini", "gemini-mcp":
		return []string{
			"CHAT_LLM_GEMINI_API_KEY", // специфичная для Gemini
			"CHAT_LLM_LLM_API_KEY",    // универсальная
		}
	default:
		return []string{
			"CHAT_LLM_LLM_API_KEY",
		}
	}
}
