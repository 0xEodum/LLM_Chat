package providers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	openai "github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/shared"
	"go.uber.org/zap"
)

type OpenRouterProvider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	client     *openai.Client
	logger     *zap.Logger
}

func NewOpenRouterProvider(config Config, logger *zap.Logger) (Provider, error) {
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	provider := &OpenRouterProvider{
		baseURL: config.BaseURL,
		apiKey:  config.APIKey,
		model:   config.Model,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger.With(zap.String("provider", "openrouter")),
	}

	// Initialize OpenAI-compatible client for OpenRouter
	oaClient := openai.NewClient(
		option.WithBaseURL(provider.baseURL),
		option.WithAPIKey(provider.apiKey),
		option.WithHTTPClient(provider.httpClient),
	)
	provider.client = &oaClient

	if err := provider.ValidateConfig(); err != nil {
		return nil, err
	}

	return provider, nil
}

func (p *OpenRouterProvider) GetName() string {
	return "openrouter"
}

func (p *OpenRouterProvider) ValidateConfig() error {
	if p.baseURL == "" {
		return fmt.Errorf("base URL is required for OpenRouter")
	}
	if p.apiKey == "" {
		return fmt.Errorf("API key is required for OpenRouter")
	}
	if p.model == "" {
		return fmt.Errorf("model is required for OpenRouter")
	}
	return nil
}

func (p *OpenRouterProvider) GetSupportedModels() []string {
	return []string{
		"google/gemma-3-27b-it:free",
		"anthropic/claude-sonnet-4",
		"openai/gpt-4o",
		"meta/llama-3.1-8b-instruct:free",
	}
}

func (p *OpenRouterProvider) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	oaMessages := make([]openai.ChatCompletionMessageParamUnion, len(messages))
	for i, msg := range messages {
		role := openai.ChatMessageRoleUser
		switch msg.Role {
		case "system":
			role = openai.ChatMessageRoleSystem
		case "assistant":
			role = openai.ChatMessageRoleAssistant
		case "user":
			role = openai.ChatMessageRoleUser
		}
		oaMessages[i] = &openai.ChatCompletionMessageParam{
			Role:    role,
			Content: openai.String(msg.Content),
		}
	}

	p.logger.Debug("Sending OpenRouter request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	resp, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(p.model),
		Messages:    oaMessages,
		MaxTokens:   openai.Int(1000),
		Temperature: openai.Float64(0.7),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get completion: %w", err)
	}

	return p.convertResponse(resp), nil
}

func (p *OpenRouterProvider) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	oaMessages := make([]openai.ChatCompletionMessageParamUnion, len(messages))
	for i, msg := range messages {
		role := openai.ChatMessageRoleUser
		switch msg.Role {
		case "system":
			role = openai.ChatMessageRoleSystem
		case "assistant":
			role = openai.ChatMessageRoleAssistant
		case "user":
			role = openai.ChatMessageRoleUser
		}
		oaMessages[i] = &openai.ChatCompletionMessageParam{
			Role:    role,
			Content: openai.String(msg.Content),
		}
	}

	p.logger.Debug("Sending streaming OpenRouter request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	stream := p.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(p.model),
		Messages:    oaMessages,
		MaxTokens:   openai.Int(1000),
		Temperature: openai.Float64(0.7),
	})

	chunks := make(chan StreamChunk, 100)

	go func() {
		defer close(chunks)
		defer stream.Close()

		for stream.Next() {
			resp := stream.Response
			if len(resp.Choices) > 0 {
				choice := resp.Choices[0]
				if choice.Delta.Content != "" {
					chunks <- StreamChunk{Content: choice.Delta.Content}
				}
				if choice.FinishReason != "" {
					chunks <- StreamChunk{Done: true}
					return
				}
			}
		}

		if err := stream.Err(); err != nil {
			chunks <- StreamChunk{Error: fmt.Errorf("stream error: %w", err)}
			return
		}
		chunks <- StreamChunk{Done: true}
	}()

	return chunks, nil
}

func (p *OpenRouterProvider) convertResponse(resp *openai.ChatCompletion) *ChatResponse {
	choices := make([]Choice, len(resp.Choices))
	for i, choice := range resp.Choices {
		choices[i] = Choice{
			Index: int(choice.Index),
			Message: Message{
				Role:    string(choice.Message.Role),
				Content: choice.Message.Content,
			},
			FinishReason: choice.FinishReason,
		}
	}

	return &ChatResponse{
		ID:      resp.ID,
		Model:   resp.Model,
		Choices: choices,
		Usage: Usage{
			PromptTokens:     int(resp.Usage.PromptTokens),
			CompletionTokens: int(resp.Usage.CompletionTokens),
			TotalTokens:      int(resp.Usage.TotalTokens),
		},
	}
}
