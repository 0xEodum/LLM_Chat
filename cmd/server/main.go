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
	"LLM_Chat/pkg/llm/providers"

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

	logger.Info("Starting chat-llm-mvp server with MCP Gemini support",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.String("llm_provider", cfg.LLM.Provider),
		zap.String("llm_model", cfg.LLM.Model),
		zap.String("mcp_server", cfg.MCP.ServerURL),
		zap.String("system_prompt_path", cfg.MCP.SystemPromptPath),
		zap.Int("context_window_size", cfg.Chat.ContextWindowSize),
		zap.Int("max_messages_per_session", cfg.Chat.MaxMessagesPerSession),
	)

	// Валидация конфигурации LLM
	if cfg.LLM.APIKey == "" {
		envVars := config.GetGeminiEnvVars()
		logger.Fatal("Gemini API key is not set",
			zap.String("provider", cfg.LLM.Provider),
			zap.String("recommended", "Set api_key in config.yaml"),
			zap.Strings("alternative_env_vars", envVars),
		)
	}

	// Проверяем поддержку провайдера (теперь только Gemini)
	if cfg.LLM.Provider != "gemini" {
		supportedProviders := llm.GetSupportedProviders(logger)
		logger.Fatal("Unsupported LLM provider",
			zap.String("provider", cfg.LLM.Provider),
			zap.Strings("supported_providers", supportedProviders),
		)
	}

	// Инициализация storage
	storage := memory.New()
	logger.Info("Initialized in-memory storage")

	// Инициализация LLM клиентов с MCP поддержкой
	mainLLMClient, err := initMCPLLMClient(cfg, logger, "main")
	if err != nil {
		logger.Fatal("Failed to initialize main LLM client", zap.Error(err))
	}

	shrinkLLMClient, err := initMCPLLMClient(cfg, logger, "shrink")
	if err != nil {
		logger.Fatal("Failed to initialize shrink LLM client", zap.Error(err))
	}

	logger.Info("Initialized MCP LLM clients",
		zap.String("main_provider", mainLLMClient.GetProviderName()),
		zap.String("main_model", cfg.LLM.Model),
		zap.String("shrink_provider", shrinkLLMClient.GetProviderName()),
		zap.String("mcp_server", cfg.MCP.ServerURL),
	)

	// Логируем поддерживаемые модели
	supportedModels := mainLLMClient.GetSupportedModels()
	logger.Info("Supported models for MCP Gemini provider",
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

func initMCPLLMClient(cfg *config.Config, logger *zap.Logger, clientType string) (*llm.Client, error) {
	llmConfig := llm.Config{
		Provider: cfg.LLM.Provider,
		BaseURL:  cfg.LLM.BaseURL, // ДОБАВИТЬ эту строку
		APIKey:   cfg.LLM.APIKey,
		Model:    cfg.LLM.Model,
		Timeout:  60 * time.Second,
	}

	// Создаем MCP конфигурацию
	mcpConfig := providers.MCPProviderConfig{
		ServerURL:        cfg.MCP.ServerURL,
		SystemPromptPath: cfg.MCP.SystemPromptPath,
		MaxIterations:    cfg.MCP.MaxIterations,
		HTTPHeaders:      cfg.MCP.HTTPHeaders,
	}

	// Используем новую фабрику с MCP поддержкой
	providerConfig := providers.Config{
		Provider: llmConfig.Provider,
		BaseURL:  llmConfig.BaseURL,
		APIKey:   llmConfig.APIKey,
		Model:    llmConfig.Model,
		Timeout:  llmConfig.Timeout,
	}

	// Используем новую фабрику с MCP поддержкой
	factory := providers.NewFactory(logger.With(zap.String("llm_client", clientType)))
	provider, err := factory.CreateProviderWithMCP(providerConfig, mcpConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s MCP provider: %w", clientType, err)
	}

	client := llm.NewClientWithProvider(provider, logger.With(zap.String("llm_client", clientType)))
	return client, nil
}

func logConfigInfo(cfg *config.Config, logger *zap.Logger) {
	configSources := config.GetConfigSource(cfg)

	logger.Info("Configuration loaded",
		zap.String("config_file", configSources["config_file"]),
		zap.String("api_key_source", configSources["api_key"]),
		zap.String("provider", configSources["provider"]),
		zap.String("mcp_server", configSources["mcp_server"]),
		zap.String("system_prompt", configSources["system_prompt"]),
	)

	// Логируем рекомендуемые переменные окружения
	geminiEnvVars := config.GetGeminiEnvVars()
	mcpEnvVars := config.GetMCPEnvVars()

	logger.Info("Environment variables for Gemini",
		zap.Strings("gemini_env_vars", geminiEnvVars),
	)

	logger.Info("Environment variables for MCP",
		zap.Strings("mcp_env_vars", mcpEnvVars),
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
