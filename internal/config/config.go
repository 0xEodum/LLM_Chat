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
	BaseURL string `mapstructure:"base_url"`
	Model   string `mapstructure:"model"`
	APIKey  string `mapstructure:"api_key"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")

	// Environment variables
	viper.AutomaticEnv()
	viper.SetEnvPrefix("CHAT_LLM")

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var config Config
	if err := viper.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Логика для API ключа:
	// 1. Если в config.yaml указан ключ - используем его
	// 2. Если в config.yaml пусто - пробуем переменную окружения
	// 3. Если нигде нет - ошибка
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		if envAPIKey := viper.GetString("LLM_API_KEY"); envAPIKey != "" {
			config.LLM.APIKey = envAPIKey
		}
	}

	// Валидация критических параметров
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

func validateConfig(config *Config) error {
	// Проверяем наличие API ключа
	if strings.TrimSpace(config.LLM.APIKey) == "" {
		return fmt.Errorf("LLM API key is required. Set it in config.yaml (llm.api_key) or environment variable CHAT_LLM_LLM_API_KEY")
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

	return nil
}

// GetConfigSource возвращает информацию о том, откуда взят API ключ
func GetConfigSource(config *Config) map[string]string {
	sources := make(map[string]string)

	// Проверяем, откуда взят API ключ
	viperAPIKey := viper.GetString("llm.api_key")
	envAPIKey := viper.GetString("LLM_API_KEY")

	if viperAPIKey != "" {
		sources["api_key"] = "config.yaml"
	} else if envAPIKey != "" {
		sources["api_key"] = "environment variable CHAT_LLM_LLM_API_KEY"
	} else {
		sources["api_key"] = "not set"
	}

	sources["config_file"] = viper.ConfigFileUsed()

	return sources
}
