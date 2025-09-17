package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"LLM_Chat/internal/api/handlers"
	"LLM_Chat/internal/api/routes"
	"LLM_Chat/internal/config"
	"LLM_Chat/internal/service/chat"
	contextmgr "LLM_Chat/internal/service/context"
	"LLM_Chat/internal/service/summary"
	"LLM_Chat/internal/storage/postgres"
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

	logger.Info("Starting chat-llm-mvp server with PostgreSQL and multi-level compression",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.String("llm_provider", cfg.LLM.Provider),
		zap.String("llm_model", cfg.LLM.Model),
		zap.String("mcp_server", cfg.MCP.ServerURL),
		zap.String("database_url", maskDatabaseURL(cfg.Database.URL)),
		zap.Int("context_window_size", cfg.Chat.ContextWindowSize),
		zap.Float64("message_compression_ratio", cfg.Chat.MessageCompressionRatio),
		zap.Float64("summary_compression_ratio", cfg.Chat.SummaryCompressionRatio),
		zap.Bool("auto_migrate", cfg.Database.AutoMigrate),
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

	// Инициализация PostgreSQL storage
	storage, err := postgres.New(cfg.Database.URL, logger)
	if err != nil {
		logger.Fatal("Failed to initialize PostgreSQL storage", zap.Error(err))
	}
	defer storage.Close()

	logger.Info("PostgreSQL storage initialized successfully",
		zap.String("database_url", maskDatabaseURL(cfg.Database.URL)),
		zap.Int("max_open_conns", cfg.Database.MaxOpenConns),
		zap.Int("max_idle_conns", cfg.Database.MaxIdleConns),
	)

	// Выполнение миграций
	if cfg.Database.AutoMigrate {
		logger.Info("Running database migrations...")
		migrator := postgres.NewMigrator(storage.GetDB(), logger)

		// Используем встроенные миграции
		if err := migrator.RunMigrationsFromStrings(context.Background(), postgres.EmbeddedMigrations); err != nil {
			logger.Fatal("Failed to run database migrations", zap.Error(err))
		}

		currentVersion, err := migrator.GetCurrentVersion(context.Background())
		if err != nil {
			logger.Warn("Failed to get current migration version", zap.Error(err))
		} else {
			logger.Info("Database migrations completed successfully", zap.Int("current_version", currentVersion))
		}
	} else {
		logger.Info("Auto-migration is disabled, skipping migrations")
	}

	// Инициализация LLM клиентов с MCP поддержкой
	mainLLMClient, err := initMCPLLMClient(cfg, logger, "main")
	if err != nil {
		logger.Fatal("Failed to initialize main LLM client", zap.Error(err))
	}

	shrinkLLMClient, err := initMCPLLMClient(cfg, logger, "shrink")
	if err != nil {
		logger.Fatal("Failed to initialize shrink LLM client", zap.Error(err))
	}

	logger.Info("MCP LLM clients initialized successfully",
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

	// Инициализация Summary Service с поддержкой многоуровневого сжатия
	summaryConfig := summary.DefaultConfig()
	summaryConfig.ContextWindowSize = cfg.Chat.ContextWindowSize

	summaryService := summary.NewService(
		storage, // ExtendedMessageStore (SummaryStore)
		shrinkLLMClient,
		summaryConfig,
		logger,
	)
	logger.Info("Multi-level summary service initialized",
		zap.Int("context_window_size", summaryConfig.ContextWindowSize),
		zap.Int("anchors_count", summaryConfig.AnchorsCount),
		zap.Int("summary_max_length", summaryConfig.SummaryMaxLength),
		zap.Int("min_messages_for_summary", summaryConfig.MinMessagesForSummary),
	)

	// Инициализация Context Manager с многоуровневым сжатием
	contextConfig := contextmgr.DefaultConfig()
	contextConfig.ContextWindowSize = cfg.Chat.ContextWindowSize
	contextConfig.MaxMessagesBeforeCompress = cfg.Chat.MaxMessagesPerSession
	contextConfig.MessageCompressionRatio = cfg.Chat.MessageCompressionRatio
	contextConfig.SummaryCompressionRatio = cfg.Chat.SummaryCompressionRatio
	contextConfig.MinMessagesInWindow = cfg.Chat.MinMessagesInWindow

	contextManager := contextmgr.NewManager(
		storage, // ExtendedMessageStore
		summaryService,
		contextConfig,
		logger,
	)
	logger.Info("Multi-level context manager initialized",
		zap.Int("context_window_size", contextConfig.ContextWindowSize),
		zap.Int("max_messages_before_compress", contextConfig.MaxMessagesBeforeCompress),
		zap.Float64("message_compression_ratio", contextConfig.MessageCompressionRatio),
		zap.Float64("summary_compression_ratio", contextConfig.SummaryCompressionRatio),
		zap.Int("min_messages_in_window", contextConfig.MinMessagesInWindow),
	)

	// Инициализация Chat Service с PostgreSQL и Context Manager
	chatService := chat.NewService(
		storage,        // ExtendedMessageStore (MessageStore)
		storage,        // ExtendedMessageStore (SessionStore)
		contextManager, // ContextManager с многоуровневым сжатием
		mainLLMClient,  // Main LLM
		&cfg.Chat,
		logger,
	)
	logger.Info("Chat service with PostgreSQL and multi-level compression initialized")

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
		logger.Info("Server starting with PostgreSQL backend", zap.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// Логируем информацию о конфигурации
	logConfigInfo(cfg, logger)

	// Проверяем подключение к базе данных
	if err := testDatabaseConnection(storage, logger); err != nil {
		logger.Fatal("Database connection test failed", zap.Error(err))
	}

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

	logger.Info("Server stopped gracefully")
}

func initMCPLLMClient(cfg *config.Config, logger *zap.Logger, clientType string) (*llm.Client, error) {
	providerConfig := cfg.ToProviderConfig()
	mcpConfig := cfg.ToMCPConfig()

	// Создаем MCP Gemini провайдер
	factory := providers.NewFactory(logger.With(zap.String("llm_client", clientType)))
	provider, err := factory.CreateProviderWithMCP(providerConfig, mcpConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s MCP provider: %w", clientType, err)
	}

	client := llm.NewClientWithProvider(provider, logger.With(zap.String("llm_client", clientType)))
	return client, nil
}

func testDatabaseConnection(storage *postgres.PostgresStorage, logger *zap.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Попробуем выполнить простой запрос
	if err := storage.CreateSession(ctx, "test-connection-"+fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		return fmt.Errorf("failed to create test session: %w", err)
	}

	logger.Info("Database connection test passed successfully")
	return nil
}

func maskDatabaseURL(dbURL string) string {
	// Маскируем пароль в URL для логирования
	if dbURL == "" {
		return ""
	}

	// Простая маскировка - заменяем всё между :// и @
	parts := strings.Split(dbURL, "://")
	if len(parts) != 2 {
		return dbURL
	}

	afterProtocol := parts[1]
	atIndex := strings.Index(afterProtocol, "@")
	if atIndex == -1 {
		return dbURL
	}

	// Находим первое двоеточие после протокола
	colonIndex := strings.Index(afterProtocol, ":")
	if colonIndex == -1 || colonIndex > atIndex {
		return dbURL
	}

	username := afterProtocol[:colonIndex]
	afterAt := afterProtocol[atIndex:]

	return fmt.Sprintf("%s://%s:***%s", parts[0], username, afterAt)
}

func logConfigInfo(cfg *config.Config, logger *zap.Logger) {
	configSources := config.GetConfigSource(cfg)

	logger.Info("Configuration loaded successfully",
		zap.String("config_file", configSources["config_file"]),
		zap.String("api_key_source", configSources["api_key"]),
		zap.String("provider", configSources["provider"]),
		zap.String("database_source", configSources["database"]),
		zap.String("mcp_server", configSources["mcp_server"]),
		zap.String("system_prompt", configSources["system_prompt"]),
	)

	// Логируем рекомендуемые переменные окружения
	geminiEnvVars := config.GetGeminiEnvVars()
	mcpEnvVars := config.GetMCPEnvVars()
	dbEnvVars := config.GetDatabaseEnvVars()

	logger.Info("Environment variables guide",
		zap.Strings("gemini_env_vars", geminiEnvVars),
		zap.Strings("mcp_env_vars", mcpEnvVars),
		zap.Strings("database_env_vars", dbEnvVars),
	)

	// Логируем конфигурацию многоуровневого сжатия
	logger.Info("Multi-level compression configuration",
		zap.Int("context_window_size", cfg.Chat.ContextWindowSize),
		zap.Float64("message_compression_ratio", cfg.Chat.MessageCompressionRatio),
		zap.Float64("summary_compression_ratio", cfg.Chat.SummaryCompressionRatio),
		zap.Int("min_messages_in_window", cfg.Chat.MinMessagesInWindow),
		zap.Int("max_messages_per_session", cfg.Chat.MaxMessagesPerSession),
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
