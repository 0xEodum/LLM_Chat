package llm

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

type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *zap.Logger
}

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// OpenRouter API structs
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
}

type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason"`
}

type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type StreamResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

func NewClient(config Config, logger *zap.Logger) *Client {
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	return &Client{
		baseURL: config.BaseURL,
		apiKey:  config.APIKey,
		model:   config.Model,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger,
	}
}

// ChatCompletion выполняет запрос к LLM и возвращает полный ответ
func (c *Client) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   1000,
		Stream:      false,
		Temperature: 0.7,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.logger.Debug("Sending LLM request",
		zap.String("model", c.model),
		zap.Int("messages_count", len(messages)),
		zap.String("request_body", string(reqBody)),
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.logger.Error("LLM API error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	c.logger.Debug("LLM response received",
		zap.String("response_id", chatResp.ID),
		zap.Int("total_tokens", chatResp.Usage.TotalTokens),
	)

	return &chatResp, nil
}

// ChatCompletionStream выполняет стриминговый запрос к LLM
func (c *Client) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	req := ChatRequest{
		Model:       c.model,
		Messages:    messages,
		MaxTokens:   1000,
		Stream:      true,
		Temperature: 0.7,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	c.logger.Debug("Sending streaming LLM request",
		zap.String("model", c.model),
		zap.Int("messages_count", len(messages)),
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.logger.Error("LLM API streaming error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	chunks := make(chan StreamChunk, 100)

	go c.handleStreamResponse(ctx, resp.Body, chunks)

	return chunks, nil
}

type StreamChunk struct {
	Content string
	Done    bool
	Error   error
}

func (c *Client) handleStreamResponse(ctx context.Context, body io.ReadCloser, chunks chan<- StreamChunk) {
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

		// Server-Sent Events format: "data: {...}"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// Конец стрима
		if data == "[DONE]" {
			chunks <- StreamChunk{Done: true}
			return
		}

		var streamResp StreamResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			c.logger.Warn("Failed to parse stream chunk", zap.Error(err), zap.String("data", data))
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
