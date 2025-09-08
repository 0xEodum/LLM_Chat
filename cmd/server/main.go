package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"LLM_Chat/internal/api/handlers"
	"LLM_Chat/internal/api/routes"
	"LLM_Chat/internal/config"
	"LLM_Chat/internal/service/chat"
	contextmgr "LLM_Chat/internal/service/context"
	"LLM_Chat/internal/service/summary"
	"LLM_Chat/internal/storage/memory"
	"LLM_Chat/pkg/llm"

	"go.uber.org/zap"
)

func main() {
	// Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	// Настройка логгера
	logger, err := setupLogger(cfg.Logging)
	if err != nil {
		panic(fmt.Sprintf("Failed to setup logger: %v", err))
	}
	defer logger.Sync()

	logger.Info("Starting chat-llm-mvp server with provider support",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.String("llm_provider", cfg.LLM.Provider),
		zap.String("llm_model", cfg.LLM.Model),
		zap.Int("context_window_size", cfg.Chat.ContextWindowSize),
		zap.Int("max_messages_per_session", cfg.Chat.MaxMessagesPerSession),
	)

	// Валидация конфигурации LLM
	if cfg.LLM.APIKey == "" {
		envVars := config.GetProviderSpecificEnvVars(cfg.LLM.Provider)
		logger.Fatal("LLM API key is not set",
			zap.String("provider", cfg.LLM.Provider),
			zap.String("recommended", "Set api_key in config.yaml"),
			zap.Strings("alternative_env_vars", envVars),
		)
	}

	// Проверяем поддержку провайдера
	if err := llm.ValidateProvider(cfg.LLM.Provider, logger); err != nil {
		supportedProviders := llm.GetSupportedProviders(logger)
		logger.Fatal("Unsupported LLM provider",
			zap.String("provider", cfg.LLM.Provider),
			zap.Strings("supported_providers", supportedProviders),
			zap.Error(err),
		)
	}

	// Инициализация storage
	storage := memory.New()
	logger.Info("Initialized in-memory storage")

	// Инициализация LLM клиентов
	mainLLMClient, err := initLLMClient(cfg, logger, "main")
	if err != nil {
		logger.Fatal("Failed to initialize main LLM client", zap.Error(err))
	}

	shrinkLLMClient, err := initShrinkLLMClient(cfg, logger)
	if err != nil {
		logger.Fatal("Failed to initialize shrink LLM client", zap.Error(err))
	}

	logger.Info("Initialized LLM clients",
		zap.String("main_provider", mainLLMClient.GetProviderName()),
		zap.String("main_model", cfg.LLM.Model),
		zap.String("shrink_provider", shrinkLLMClient.GetProviderName()),
	)

	// Логируем поддерживаемые модели для текущего провайдера
	supportedModels := mainLLMClient.GetSupportedModels()
	logger.Info("Supported models for current provider",
		zap.String("provider", cfg.LLM.Provider),
		zap.Strings("models", supportedModels),
	)

	// Инициализация Summary Service
	summaryConfig := summary.DefaultConfig()
	summaryConfig.MaxMessagesBeforeSummary = cfg.Chat.MaxMessagesPerSession
	summaryConfig.ContextWindowSize = cfg.Chat.ContextWindowSize

	summaryService := summary.NewService(
		storage, // SummaryStore
		shrinkLLMClient,
		summaryConfig,
		logger,
	)
	logger.Info("Initialized summary service",
		zap.Int("max_messages_before_summary", summaryConfig.MaxMessagesBeforeSummary),
		zap.Int("context_window_size", summaryConfig.ContextWindowSize),
		zap.Int("anchors_count", summaryConfig.AnchorsCount),
	)

	// Инициализация Context Manager
	contextConfig := contextmgr.DefaultConfig()
	contextConfig.ContextWindowSize = cfg.Chat.ContextWindowSize
	contextConfig.MaxMessagesBeforeCompress = cfg.Chat.MaxMessagesPerSession

	contextManager := contextmgr.NewManager(
		storage, // MessageStore
		summaryService,
		contextConfig,
		logger,
	)
	logger.Info("Initialized context manager",
		zap.Int("context_window_size", contextConfig.ContextWindowSize),
		zap.Int("max_messages_before_compress", contextConfig.MaxMessagesBeforeCompress),
	)

	// Инициализация Chat Service с Context Manager
	chatService := chat.NewService(
		storage,        // MessageStore
		storage,        // SessionStore
		contextManager, // ContextManager
		mainLLMClient,  // Main LLM
		&cfg.Chat,
		logger,
	)
	logger.Info("Initialized chat service with context management")

	// Инициализация handlers
	chatHandler := handlers.NewChatHandler(chatService, storage, logger)
	summaryHandler := handlers.NewSummaryHandler(summaryService, logger)
	healthHandler := handlers.NewHealthHandler()
	modelsHandler := handlers.NewModelsHandler(logger)

	// Настройка роутов
	router := routes.SetupRoutes(cfg, logger, chatHandler, summaryHandler, healthHandler, modelsHandler)

	// Настройка HTTP сервера
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Запуск сервера в отдельной горутине
	go func() {
		logger.Info("Server starting", zap.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Тестовый запрос к LLM для проверки подключения
	go testLLMConnection(mainLLMClient, logger)

	// Логируем информацию о конфигурации
	logConfigInfo(cfg, logger)

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("Server stopped")
}

func initLLMClient(cfg *config.Config, logger *zap.Logger, clientType string) (*llm.Client, error) {
	llmConfig := llm.Config{
		Provider: cfg.LLM.Provider,
		BaseURL:  cfg.LLM.BaseURL,
		APIKey:   cfg.LLM.APIKey,
		Model:    cfg.LLM.Model,
		Timeout:  60 * time.Second,
	}

	client, err := llm.NewClient(llmConfig, logger.With(zap.String("llm_client", clientType)))
	if err != nil {
		return nil, fmt.Errorf("failed to create %s LLM client: %w", clientType, err)
	}

	return client, nil
}

func initShrinkLLMClient(cfg *config.Config, logger *zap.Logger) (*llm.Client, error) {
	// Для сжатия используем более дешевый провайдер/модель если возможно
	shrinkConfig := llm.Config{
		Provider: cfg.LLM.Provider, // Используем тот же провайдер
		BaseURL:  cfg.LLM.BaseURL,
		APIKey:   cfg.LLM.APIKey,
		Timeout:  45 * time.Second,
	}

	// Выбираем модель для сжатия в зависимости от провайдера
	switch cfg.LLM.Provider {
	case "openrouter":
		shrinkConfig.Model = "google/gemma-3-27b-it:free" // Бесплатная модель
	case "gemini":
		shrinkConfig.Model = "gemini-2.0-flash" // Быстрая модель Gemini
	default:
		shrinkConfig.Model = cfg.LLM.Model // Используем ту же модель
	}

	client, err := llm.NewClient(shrinkConfig, logger.With(zap.String("llm_client", "shrink")))
	if err != nil {
		return nil, fmt.Errorf("failed to create shrink LLM client: %w", err)
	}

	return client, nil
}

func testLLMConnection(client *llm.Client, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Небольшой тестовый запрос
	testMessages := []llm.Message{
		{Role: "user", Content: "Hello! Just testing the connection. Please respond with 'OK'."},
	}

	logger.Info("Testing LLM connection...",
		zap.String("provider", client.GetProviderName()),
	)

	resp, err := client.ChatCompletion(ctx, testMessages)
	if err != nil {
		logger.Error("LLM connection test failed",
			zap.String("provider", client.GetProviderName()),
			zap.Error(err),
		)
		return
	}

	if len(resp.Choices) > 0 {
		logger.Info("LLM connection test successful",
			zap.String("provider", client.GetProviderName()),
			zap.String("response", resp.Choices[0].Message.Content),
			zap.Int("tokens_used", resp.Usage.TotalTokens),
		)
	} else {
		logger.Warn("LLM connection test: no choices in response",
			zap.String("provider", client.GetProviderName()),
		)
	}
}

func logConfigInfo(cfg *config.Config, logger *zap.Logger) {
	configSources := config.GetConfigSource(cfg)

	logger.Info("Configuration loaded",
		zap.String("config_file", configSources["config_file"]),
		zap.String("api_key_source", configSources["api_key"]),
		zap.String("provider", configSources["provider"]),
	)

	// Логируем рекомендуемые переменные окружения для текущего провайдера
	envVars := config.GetProviderSpecificEnvVars(cfg.LLM.Provider)
	logger.Info("Environment variables for current provider",
		zap.String("provider", cfg.LLM.Provider),
		zap.Strings("env_vars", envVars),
	)
}

func setupLogger(cfg config.LoggingConfig) (*zap.Logger, error) {
	var zapCfg zap.Config

	if cfg.Format == "json" {
		zapCfg = zap.NewProductionConfig()
	} else {
		zapCfg = zap.NewDevelopmentConfig()
	}

	// Настройка уровня логирования
	switch cfg.Level {
	case "debug":
		zapCfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		zapCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		zapCfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		zapCfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		zapCfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	return zapCfg.Build()
}
