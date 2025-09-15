package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"LLM_Chat/pkg/mcp"
	"go.uber.org/zap"
)

// GeminiMCPProvider integrates Gemini with MCP tools via the MCP client package.
// It leverages the MCP client's internal loop to repeatedly call tools until
// the model returns a final answer.
type GeminiMCPProvider struct {
	cfg    Config
	logger *zap.Logger
	client *mcp.MCPClient
}

// NewGeminiMCPProvider creates a new provider instance.
func NewGeminiMCPProvider(config Config, logger *zap.Logger) (Provider, error) {
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	p := &GeminiMCPProvider{
		cfg:    config,
		logger: logger.With(zap.String("provider", "gemini-mcp")),
	}
	if err := p.ValidateConfig(); err != nil {
		return nil, err
	}
	return p, nil
}

// ensureClient lazily initializes the underlying MCP client.
func (p *GeminiMCPProvider) ensureClient(ctx context.Context) error {
	if p.client != nil {
		return nil
	}
	cfg := mcp.MCPClientConfig{
		ServerURL:        p.cfg.ServerURL,
		HTTPHeaders:      p.cfg.HTTPHeaders,
		SystemPromptPath: p.cfg.SystemPromptPath,
		GeminiAPIKey:     p.cfg.APIKey,
		GeminiBaseURL:    p.cfg.BaseURL,
		GeminiModel:      p.cfg.Model,
	}
	c := mcp.NewMCPClient(cfg)
	if err := c.Start(ctx); err != nil {
		return err
	}
	p.client = c
	return nil
}

func (p *GeminiMCPProvider) GetName() string { return "gemini-mcp" }

func (p *GeminiMCPProvider) ValidateConfig() error {
	if strings.TrimSpace(p.cfg.BaseURL) == "" {
		return fmt.Errorf("base URL is required for Gemini MCP")
	}
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return fmt.Errorf("API key is required for Gemini MCP")
	}
	if strings.TrimSpace(p.cfg.Model) == "" {
		return fmt.Errorf("model is required for Gemini MCP")
	}
	if strings.TrimSpace(p.cfg.ServerURL) == "" {
		return fmt.Errorf("server URL is required for Gemini MCP")
	}
	if strings.TrimSpace(p.cfg.SystemPromptPath) == "" {
		return fmt.Errorf("system prompt path is required for Gemini MCP")
	}
	return nil
}

func (p *GeminiMCPProvider) GetSupportedModels() []string {
	return []string{
		"gemini-2.0-flash",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}
}

// ChatCompletion executes a single request using the MCP client's ProcessQuery
// loop. Only the latest user message is considered as query.
func (p *GeminiMCPProvider) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	if err := p.ensureClient(ctx); err != nil {
		return nil, err
	}
	var userMsg string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userMsg = messages[i].Content
			break
		}
	}
	if strings.TrimSpace(userMsg) == "" {
		return nil, errors.New("no user message provided")
	}
	answer, err := p.client.ProcessQuery(ctx, userMsg, 5)
	if err != nil {
		return nil, err
	}
	return &ChatResponse{
		ID:    fmt.Sprintf("gemini-mcp-%d", time.Now().Unix()),
		Model: p.cfg.Model,
		Choices: []Choice{{
			Index: 0,
			Message: Message{
				Role:    "assistant",
				Content: answer,
			},
			FinishReason: "stop",
		}},
	}, nil
}

// ChatCompletionStream provides a basic streaming interface by executing the
// non-streaming request in a goroutine and sending the final result as a single
// chunk.
func (p *GeminiMCPProvider) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	go func() {
		defer close(ch)
		resp, err := p.ChatCompletion(ctx, messages)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		if len(resp.Choices) > 0 {
			ch <- StreamChunk{Content: resp.Choices[0].Message.Content}
		}
		ch <- StreamChunk{Done: true}
	}()
	return ch, nil
}
