package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

type OpenRouterProvider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *zap.Logger
}

// OpenRouter API specific structs
type openRouterRequest struct {
	Model       string              `json:"model"`
	Messages    []openRouterMessage `json:"messages"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
	Temperature float64             `json:"temperature,omitempty"`
}

type openRouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openRouterResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openRouterChoice `json:"choices"`
	Usage   openRouterUsage    `json:"usage"`
}

type openRouterChoice struct {
	Index        int               `json:"index"`
	Message      openRouterMessage `json:"message"`
	Delta        openRouterDelta   `json:"delta,omitempty"`
	FinishReason string            `json:"finish_reason"`
}

type openRouterDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type openRouterUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openRouterStreamResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openRouterChoice `json:"choices"`
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
	// Конвертируем в формат OpenRouter
	orMessages := make([]openRouterMessage, len(messages))
	for i, msg := range messages {
		orMessages[i] = openRouterMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	req := openRouterRequest{
		Model:       p.model,
		Messages:    orMessages,
		MaxTokens:   1000,
		Stream:      false,
		Temperature: 0.7,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	p.logger.Debug("Sending OpenRouter request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		p.logger.Error("OpenRouter API error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	var orResp openRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&orResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Конвертируем в универсальный формат
	return p.convertResponse(&orResp), nil
}

func (p *OpenRouterProvider) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	// Конвертируем в формат OpenRouter
	orMessages := make([]openRouterMessage, len(messages))
	for i, msg := range messages {
		orMessages[i] = openRouterMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	req := openRouterRequest{
		Model:       p.model,
		Messages:    orMessages,
		MaxTokens:   1000,
		Stream:      true,
		Temperature: 0.7,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	p.logger.Debug("Sending streaming OpenRouter request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.logger.Error("OpenRouter API streaming error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	chunks := make(chan StreamChunk, 100)
	go p.handleStreamResponse(ctx, resp.Body, chunks)

	return chunks, nil
}

func (p *OpenRouterProvider) handleStreamResponse(ctx context.Context, body io.ReadCloser, chunks chan<- StreamChunk) {
	defer close(chunks)
	defer body.Close()

	scanner := bufio.NewScanner(body)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			chunks <- StreamChunk{Error: ctx.Err()}
			return
		default:
		}

		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			chunks <- StreamChunk{Done: true}
			return
		}

		var streamResp openRouterStreamResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			p.logger.Warn("Failed to parse stream chunk", zap.Error(err), zap.String("data", data))
			continue
		}

		if len(streamResp.Choices) > 0 {
			choice := streamResp.Choices[0]
			if choice.Delta.Content != "" {
				chunks <- StreamChunk{Content: choice.Delta.Content}
			}

			if choice.FinishReason != "" {
				chunks <- StreamChunk{Done: true}
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		chunks <- StreamChunk{Error: fmt.Errorf("scanner error: %w", err)}
	}
}

func (p *OpenRouterProvider) convertResponse(orResp *openRouterResponse) *ChatResponse {
	choices := make([]Choice, len(orResp.Choices))
	for i, choice := range orResp.Choices {
		choices[i] = Choice{
			Index: choice.Index,
			Message: Message{
				Role:    choice.Message.Role,
				Content: choice.Message.Content,
			},
			FinishReason: choice.FinishReason,
		}
	}

	return &ChatResponse{
		ID:      orResp.ID,
		Model:   orResp.Model,
		Choices: choices,
		Usage: Usage{
			PromptTokens:     orResp.Usage.PromptTokens,
			CompletionTokens: orResp.Usage.CompletionTokens,
			TotalTokens:      orResp.Usage.TotalTokens,
		},
	}
}
