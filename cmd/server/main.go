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

	logger.Info("Starting chat-llm-mvp server with context management",
		zap.String("host", cfg.Server.Host),
		zap.Int("port", cfg.Server.Port),
		zap.String("llm_model", cfg.LLM.Model),
		zap.Int("context_window_size", cfg.Chat.ContextWindowSize),
		zap.Int("max_messages_per_session", cfg.Chat.MaxMessagesPerSession),
	)

	// Валидация конфигурации LLM
	if cfg.LLM.APIKey == "" {
		logger.Fatal("LLM API key is not set. Please set CHAT_LLM_LLM_API_KEY environment variable")
	}

	// Инициализация storage
	storage := memory.New()
	logger.Info("Initialized in-memory storage")

	// Инициализация LLM клиентов
	mainLLMClient := initLLMClient(cfg, logger, "main")
	shrinkLLMClient := initShrinkLLMClient(cfg, logger) // Отдельный клиент для сжатия

	logger.Info("Initialized LLM clients",
		zap.String("main_model", cfg.LLM.Model),
		zap.String("shrink_model", "google/gemma-3-27b-it:free"), // Более дешевая модель для сжатия
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

func initLLMClient(cfg *config.Config, logger *zap.Logger, clientType string) llm.LLMClient {
	llmConfig := llm.Config{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   cfg.LLM.Model,
		Timeout: 60 * time.Second,
	}

	client := llm.NewClient(llmConfig, logger.With(zap.String("llm_client", clientType)))
	return client
}

func initShrinkLLMClient(cfg *config.Config, logger *zap.Logger) llm.LLMClient {
	// Используем более дешевую модель для сжатия
	shrinkConfig := llm.Config{
		BaseURL: cfg.LLM.BaseURL,
		APIKey:  cfg.LLM.APIKey,
		Model:   "google/gemma-3-27b-it:free", // Бесплатная модель для сжатия
		Timeout: 45 * time.Second,             // Меньший таймаут для сжатия
	}

	client := llm.NewClient(shrinkConfig, logger.With(zap.String("llm_client", "shrink")))
	return client
}

func testLLMConnection(client llm.LLMClient, logger *zap.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Небольшой тестовый запрос
	testMessages := []llm.Message{
		{Role: "user", Content: "Hello! Just testing the connection. Please respond with 'OK'."},
	}

	logger.Info("Testing LLM connection...")

	resp, err := client.ChatCompletion(ctx, testMessages)
	if err != nil {
		logger.Error("LLM connection test failed", zap.Error(err))
		return
	}

	if len(resp.Choices) > 0 {
		logger.Info("LLM connection test successful",
			zap.String("response", resp.Choices[0].Message.Content),
			zap.Int("tokens_used", resp.Usage.TotalTokens),
		)
	} else {
		logger.Warn("LLM connection test: no choices in response")
	}
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
