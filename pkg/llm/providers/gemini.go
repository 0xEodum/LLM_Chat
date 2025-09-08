// pkg/llm/providers/gemini.go
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

type GeminiProvider struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	logger     *zap.Logger
}

// Gemini API specific structs
type geminiRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiStreamRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	Temperature     float64 `json:"temperature,omitempty"`
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func NewGeminiProvider(config Config, logger *zap.Logger) (Provider, error) {
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	provider := &GeminiProvider{
		baseURL: config.BaseURL,
		apiKey:  config.APIKey,
		model:   config.Model,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger.With(zap.String("provider", "gemini")),
	}

	if err := provider.ValidateConfig(); err != nil {
		return nil, err
	}

	return provider, nil
}

func (p *GeminiProvider) GetName() string {
	return "gemini"
}

func (p *GeminiProvider) ValidateConfig() error {
	if p.baseURL == "" {
		return fmt.Errorf("base URL is required for Gemini")
	}
	if p.apiKey == "" {
		return fmt.Errorf("API key is required for Gemini")
	}
	if p.model == "" {
		return fmt.Errorf("model is required for Gemini")
	}
	return nil
}

func (p *GeminiProvider) GetSupportedModels() []string {
	return []string{
		"gemini-2.0-flash",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}
}

func (p *GeminiProvider) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	// Конвертируем сообщения в формат Gemini
	contents, err := p.convertMessagesToGemini(messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}

	req := geminiRequest{
		Contents: contents,
		GenerationConfig: &geminiGenConfig{
			Temperature:     0.7,
			MaxOutputTokens: 1000,
		},
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	p.logger.Debug("Sending Gemini request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	// Формируем URL для Gemini API
	url := fmt.Sprintf("%s/models/%s:generateContent", p.baseURL, p.model)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
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
		p.logger.Error("Gemini API error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	p.logger.Debug("Gemini response received",
		zap.Int("candidates_count", len(geminiResp.Candidates)),
		zap.Int("total_tokens", geminiResp.UsageMetadata.TotalTokenCount),
	)

	// Конвертируем в универсальный формат
	return p.convertGeminiResponse(&geminiResp), nil
}

func (p *GeminiProvider) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	// Конвертируем сообщения в формат Gemini
	contents, err := p.convertMessagesToGemini(messages)
	if err != nil {
		return nil, fmt.Errorf("failed to convert messages: %w", err)
	}

	req := geminiStreamRequest{
		Contents: contents,
		GenerationConfig: &geminiGenConfig{
			Temperature:     0.7,
			MaxOutputTokens: 1000,
		},
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	p.logger.Debug("Sending streaming Gemini request",
		zap.String("model", p.model),
		zap.Int("messages_count", len(messages)),
	)

	// Формируем URL для стриминга Gemini API
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent", p.baseURL, p.model)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		p.logger.Error("Gemini API streaming error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response_body", string(body)),
		)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(body))
	}

	chunks := make(chan StreamChunk, 100)
	go p.handleGeminiStreamResponse(ctx, resp.Body, chunks)

	return chunks, nil
}

func (p *GeminiProvider) convertMessagesToGemini(messages []Message) ([]geminiContent, error) {
	var contents []geminiContent

	for _, msg := range messages {
		// Gemini использует другие роли
		role := ""
		switch msg.Role {
		case "system":
			// Системные сообщения можно добавлять как пользовательские в начале
			role = "user"
		case "user":
			role = "user"
		case "assistant":
			role = "model" // В Gemini ассистент называется "model"
		default:
			role = "user"
		}

		content := geminiContent{
			Parts: []geminiPart{
				{Text: msg.Content},
			},
			Role: role,
		}
		contents = append(contents, content)
	}

	return contents, nil
}

func (p *GeminiProvider) convertGeminiResponse(geminiResp *geminiResponse) *ChatResponse {
	choices := make([]Choice, len(geminiResp.Candidates))

	for i, candidate := range geminiResp.Candidates {
		content := ""
		if len(candidate.Content.Parts) > 0 {
			content = candidate.Content.Parts[0].Text
		}

		choices[i] = Choice{
			Index: candidate.Index,
			Message: Message{
				Role:    "assistant",
				Content: content,
			},
			FinishReason: candidate.FinishReason,
		}
	}

	return &ChatResponse{
		ID:      fmt.Sprintf("gemini-%d", time.Now().Unix()),
		Model:   p.model,
		Choices: choices,
		Usage: Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}
}

func (p *GeminiProvider) handleGeminiStreamResponse(ctx context.Context, body io.ReadCloser, chunks chan<- StreamChunk) {
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
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		// Gemini использует JSON объекты разделённые переносами строк
		var streamResp geminiResponse
		if err := json.Unmarshal([]byte(line), &streamResp); err != nil {
			p.logger.Warn("Failed to parse Gemini stream chunk",
				zap.Error(err),
				zap.String("data", line))
			continue
		}

		if len(streamResp.Candidates) > 0 {
			candidate := streamResp.Candidates[0]

			if len(candidate.Content.Parts) > 0 {
				content := candidate.Content.Parts[0].Text
				if content != "" {
					chunks <- StreamChunk{Content: content}
				}
			}

			// Проверяем, завершен ли поток
			if candidate.FinishReason != "" && candidate.FinishReason != "STOP" {
				p.logger.Debug("Gemini stream finished",
					zap.String("finish_reason", candidate.FinishReason))
				chunks <- StreamChunk{Done: true}
				return
			}
		}
	}

	if err := scanner.Err(); err != nil {
		chunks <- StreamChunk{Error: fmt.Errorf("scanner error: %w", err)}
		return
	}

	// Если сканер завершился без ошибок, значит поток закончен
	chunks <- StreamChunk{Done: true}
}
