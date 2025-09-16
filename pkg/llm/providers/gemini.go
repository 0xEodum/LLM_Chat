package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

type MCPGeminiProvider struct {
	// MCP components
	mcpClient   *mcp.Client
	session     *mcp.ClientSession
	available   []*mcp.Tool
	geminiTools []*genai.FunctionDeclaration

	// Gemini components
	genClient *genai.Client
	model     *genai.GenerativeModel

	// Configuration
	mcpServerURL     string
	systemPromptPath string
	maxIterations    int
	httpHeaders      map[string]string
	geminiAPIKey     string
	geminiBaseURL    string
	geminiModel      string
	systemPrompt     string

	logger *zap.Logger
}

func NewMCPGeminiProvider(config Config, mcpConfig MCPProviderConfig, logger *zap.Logger) (Provider, error) {
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}

	provider := &MCPGeminiProvider{
		mcpServerURL:     mcpConfig.ServerURL,
		systemPromptPath: mcpConfig.SystemPromptPath,
		maxIterations:    mcpConfig.MaxIterations,
		httpHeaders:      mcpConfig.HTTPHeaders,
		geminiAPIKey:     config.APIKey,
		geminiBaseURL:    config.BaseURL, // ← ДОБАВИТЬ ЭТО
		geminiModel:      config.Model,
		logger:           logger.With(zap.String("provider", "gemini-mcp")),
	}

	if err := provider.ValidateConfig(); err != nil {
		return nil, err
	}

	return provider, nil
}

type MCPProviderConfig struct {
	ServerURL        string
	SystemPromptPath string
	MaxIterations    int
	HTTPHeaders      map[string]string
}

func (p *MCPGeminiProvider) GetName() string {
	return "gemini"
}

func (p *MCPGeminiProvider) ValidateConfig() error {
	if p.geminiAPIKey == "" {
		return fmt.Errorf("Gemini API key is required")
	}
	if p.geminiModel == "" {
		return fmt.Errorf("Gemini model is required")
	}
	if p.mcpServerURL == "" {
		return fmt.Errorf("MCP server URL is required")
	}
	if p.systemPromptPath == "" {
		return fmt.Errorf("system prompt path is required")
	}
	if p.maxIterations <= 0 {
		return fmt.Errorf("max iterations must be positive")
	}
	return nil
}

func (p *MCPGeminiProvider) GetSupportedModels() []string {
	return []string{
		"gemini-2.5-flash",
		"gemini-2.0-flash",
		"gemini-1.5-pro",
		"gemini-1.5-flash",
	}
}

// loadSystemPrompt загружает системный промпт из файла
func (p *MCPGeminiProvider) loadSystemPrompt() error {
	if _, err := os.Stat(p.systemPromptPath); os.IsNotExist(err) {
		p.logger.Warn("System prompt file not found", zap.String("path", p.systemPromptPath))
		p.systemPrompt = "You are a helpful AI assistant."
		return nil
	}

	file, err := os.Open(p.systemPromptPath)
	if err != nil {
		return fmt.Errorf("failed to open system prompt file '%s': %w", p.systemPromptPath, err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read system prompt file '%s': %w", p.systemPromptPath, err)
	}

	p.systemPrompt = strings.TrimSpace(string(content))
	if p.systemPrompt == "" {
		return fmt.Errorf("system prompt file '%s' is empty", p.systemPromptPath)
	}

	p.logger.Info("System prompt loaded",
		zap.String("path", p.systemPromptPath),
		zap.Int("length", len(p.systemPrompt)))
	return nil
}

// initializeMCP инициализирует MCP соединение
func (p *MCPGeminiProvider) initializeMCP(ctx context.Context) error {
	p.logger.Info("Connecting to MCP server", zap.String("url", p.mcpServerURL))

	transport := &mcp.StreamableClientTransport{
		Endpoint:   strings.TrimRight(p.mcpServerURL, "/"),
		HTTPClient: p.httpClientWithHeaders(p.httpHeaders),
	}

	impl := &mcp.Implementation{Name: "go-mcp-client", Version: "0.2.0"}
	client := mcp.NewClient(impl, &mcp.ClientOptions{})

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP server: %w", err)
	}
	p.mcpClient = client
	p.session = session

	// Получаем список инструментов
	ltr, err := p.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		p.session.Close()
		return fmt.Errorf("failed to list MCP tools: %w", err)
	}
	p.available = ltr.Tools

	p.logger.Info("MCP tools loaded", zap.Int("count", len(p.available)))
	for _, t := range p.available {
		p.logger.Debug("Available tool", zap.String("name", t.Name), zap.String("description", t.Description))
	}

	// Конвертируем инструменты для Gemini
	p.geminiTools = p.convertMCPToGeminiTools(p.available)

	return nil
}

// initializeGemini инициализирует Gemini клиент
func (p *MCPGeminiProvider) initializeGemini(ctx context.Context) error {
	p.logger.Info("Initializing Gemini client",
		zap.String("model", p.geminiModel),
		zap.String("base_url", p.geminiBaseURL))

	opts := []option.ClientOption{option.WithAPIKey(p.geminiAPIKey)}

	// ← ДОБАВИТЬ ПОДДЕРЖКУ BASE_URL
	if strings.TrimSpace(p.geminiBaseURL) != "" {
		endpoint := strings.TrimRight(p.geminiBaseURL, "/")
		opts = append(opts, option.WithEndpoint(endpoint))
		p.logger.Info("Using custom Gemini endpoint", zap.String("endpoint", endpoint))
	}

	genClient, err := genai.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create Gemini client: %w", err)
	}
	p.genClient = genClient

	p.model = p.genClient.GenerativeModel(p.geminiModel)
	p.model.Tools = []*genai.Tool{{FunctionDeclarations: p.geminiTools}}

	return nil
}

// ensureInitialized обеспечивает инициализацию всех компонентов
// Добавить в ensureInitialized метод более детальное логирование:

func (p *MCPGeminiProvider) ensureInitialized(ctx context.Context) error {
	if p.session != nil && p.genClient != nil && p.systemPrompt != "" {
		return nil // уже инициализировано
	}

	p.logger.Info("Starting MCP Gemini initialization")

	// Загружаем системный промпт
	if p.systemPrompt == "" {
		p.logger.Info("Loading system prompt", zap.String("path", p.systemPromptPath))
		if err := p.loadSystemPrompt(); err != nil {
			p.logger.Error("Failed to load system prompt", zap.Error(err))
			return err
		}
		p.logger.Info("System prompt loaded successfully", zap.Int("length", len(p.systemPrompt)))
	}

	// Инициализируем MCP
	if p.session == nil {
		p.logger.Info("Initializing MCP connection", zap.String("server_url", p.mcpServerURL))
		if err := p.initializeMCP(ctx); err != nil {
			p.logger.Error("Failed to initialize MCP", zap.Error(err))
			return err
		}
		p.logger.Info("MCP initialized successfully", zap.Int("tools_count", len(p.available)))
	}

	// Инициализируем Gemini
	if p.genClient == nil {
		p.logger.Info("Initializing Gemini client", zap.String("model", p.geminiModel))
		if err := p.initializeGemini(ctx); err != nil {
			p.logger.Error("Failed to initialize Gemini", zap.Error(err))
			return err
		}
		p.logger.Info("Gemini initialized successfully")
	}

	p.logger.Info("MCP Gemini initialization completed successfully")
	return nil
}

func (p *MCPGeminiProvider) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	if err := p.ensureInitialized(ctx); err != nil {
		return nil, fmt.Errorf("initialization failed: %w", err)
	}

	p.model.SystemInstruction = &genai.Content{Parts: []genai.Part{genai.Text(p.systemPrompt)}}
	p.model.Tools = []*genai.Tool{{FunctionDeclarations: p.geminiTools}}

	history, lastUser := p.toGenaiHistory(messages)

	chat := p.model.StartChat()
	chat.History = history

	var finalAnswer string
	var totalTokens int

	resp, err := chat.SendMessage(ctx, lastUser.Parts...)
	if err != nil {
		return nil, fmt.Errorf("Gemini generate error: %w", err)
	}

	for i := 0; i < p.maxIterations; i++ {
		if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return nil, errors.New("no response from Gemini")
		}

		// Подсчёт usage (если SDK вернул)
		if resp.UsageMetadata != nil {
			totalTokens += int(resp.UsageMetadata.TotalTokenCount)
		}

		cand := resp.Candidates[0]
		fcalls := cand.FunctionCalls()

		if len(fcalls) > 0 {
			for _, fc := range fcalls {
				args := fc.Args
				if args == nil {
					args = map[string]any{}
				}
				result, err := p.callMCPTool(ctx, fc.Name, args)
				if err != nil {
					result = map[string]any{"error": err.Error()}
				}

				chat.History = append(chat.History, &genai.Content{
					Role: "tool",
					Parts: []genai.Part{
						genai.FunctionResponse{
							Name:     fc.Name,
							Response: result,
						},
					},
				})
			}

			resp, err = chat.SendMessage(ctx, genai.Text(""))
			if err != nil {
				return nil, fmt.Errorf("Gemini generate error (after tool): %w", err)
			}
			continue
		}

		// Иначе — финализируем текстовый ответ
		var textParts []string
		for _, part := range cand.Content.Parts {
			if t, ok := part.(genai.Text); ok {
				s := strings.TrimSpace(string(t))
				if s != "" {
					textParts = append(textParts, s)
				}
			}
		}

		finalAnswer = strings.Join(textParts, "\n")
		if strings.TrimSpace(finalAnswer) == "" {
			finalAnswer = "Нет текстового ответа"
		}
		break
	}

	if finalAnswer == "" {
		finalAnswer = "Достигнут лимит итераций без финального ответа"
	}

	return &ChatResponse{
		ID:    fmt.Sprintf("mcp-gemini-%d", time.Now().Unix()),
		Model: p.geminiModel,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: finalAnswer,
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      totalTokens,
		},
	}, nil
}

func (p *MCPGeminiProvider) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	// Стриминг не поддерживается для MCP, используем обычную реализацию
	chunks := make(chan StreamChunk, 1)

	go func() {
		defer close(chunks)

		resp, err := p.ChatCompletion(ctx, messages)
		if err != nil {
			chunks <- StreamChunk{Error: err}
			return
		}

		if len(resp.Choices) > 0 {
			content := resp.Choices[0].Message.Content
			// Разбиваем ответ на чанки для имитации стриминга
			words := strings.Fields(content)
			for i, word := range words {
				if i > 0 {
					chunks <- StreamChunk{Content: " "}
				}
				chunks <- StreamChunk{Content: word}
			}
		}

		chunks <- StreamChunk{Done: true}
	}()

	return chunks, nil
}

// callMCPTool вызывает MCP инструмент
func (p *MCPGeminiProvider) callMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	p.logger.Debug("Calling MCP tool", zap.String("name", name))

	if args == nil {
		args = map[string]any{}
	}

	res, err := p.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("tool call failed: %w", err)
	}

	if res.IsError {
		msg := "tool error"
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok && strings.TrimSpace(tc.Text) != "" {
				msg = tc.Text
				break
			}
		}
		return map[string]any{"error": msg}, nil
	}

	if res.StructuredContent != nil {
		switch v := res.StructuredContent.(type) {
		case map[string]any:
			return v, nil
		default:
			b, _ := json.Marshal(v)
			m := map[string]any{}
			if err := json.Unmarshal(b, &m); err == nil {
				return m, nil
			}
			return map[string]any{"result": string(b)}, nil
		}
	}

	var sb strings.Builder
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
			sb.WriteString("\n")
		}
	}
	out := strings.TrimSpace(sb.String())
	if out != "" {
		return map[string]any{"result": out}, nil
	}
	return map[string]any{"result": nil}, nil
}

func (p *MCPGeminiProvider) httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	rt := http.DefaultTransport
	return &http.Client{Transport: &headerRoundTripper{next: rt, headers: headers}}
}

type headerRoundTripper struct {
	next    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	return h.next.RoundTrip(req)
}

// Закрытие соединений
func (p *MCPGeminiProvider) Close() {
	if p.session != nil {
		p.session.Close()
	}
	if p.genClient != nil {
		p.genClient.Close()
	}
}

// ===============================
// JSON Schema → genai конвертация
// ===============================

func (p *MCPGeminiProvider) schemaTypeToGenai(t string) genai.Type {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	case "null":
		return genai.TypeString
	default:
		return genai.TypeString
	}
}

func (p *MCPGeminiProvider) firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (p *MCPGeminiProvider) toStringEnum(vals []any) []string {
	if len(vals) == 0 {
		return nil
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *MCPGeminiProvider) convertProperty(s *jsonschema.Schema) *genai.Schema {
	if s == nil {
		return &genai.Schema{Type: genai.TypeString}
	}

	if len(s.AnyOf) > 0 {
		for _, sub := range s.AnyOf {
			if sub == nil {
				continue
			}
			t := strings.ToLower(strings.TrimSpace(sub.Type))
			if t == "" && len(sub.Types) > 0 {
				t = strings.ToLower(strings.TrimSpace(sub.Types[0]))
			}
			if t != "null" {
				return p.convertProperty(sub)
			}
		}
		return &genai.Schema{Type: genai.TypeString}
	}

	propType := s.Type
	if propType == "" && len(s.Types) > 0 {
		propType = s.Types[0]
	}
	gType := p.schemaTypeToGenai(propType)
	desc := p.firstNonEmpty(s.Description, s.Title)
	enumVals := p.toStringEnum(s.Enum)

	switch gType {
	case genai.TypeArray:
		var itemSchema *jsonschema.Schema
		if s.Items != nil {
			itemSchema = s.Items
		}
		return &genai.Schema{
			Type:        genai.TypeArray,
			Items:       p.convertProperty(itemSchema),
			Description: desc,
			Enum:        enumVals,
		}

	case genai.TypeObject:
		props := map[string]*genai.Schema{}
		if s.Properties != nil {
			for name, sub := range s.Properties {
				props[name] = p.convertProperty(sub)
			}
		}
		var required []string
		if len(s.Required) > 0 {
			required = append(required, s.Required...)
		}
		return &genai.Schema{
			Type:        genai.TypeObject,
			Properties:  props,
			Required:    required,
			Description: desc,
			Enum:        enumVals,
		}

	default:
		return &genai.Schema{
			Type:        gType,
			Description: desc,
			Enum:        enumVals,
		}
	}
}

func (p *MCPGeminiProvider) toGenaiHistory(messages []Message) (history []*genai.Content, lastUser *genai.Content) {
	history = make([]*genai.Content, 0, len(messages))
	var lastUserIdx = -1

	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	// Если не нашли последнего user — просто положим всё как историю и отправим пустое
	for i, m := range messages {
		// system уже использован как SystemInstruction
		if m.Role == "system" {
			continue
		}

		c := &genai.Content{Parts: []genai.Part{genai.Text(m.Content)}}

		switch m.Role {
		case "user":
			c.Role = "user"
		case "assistant":
			c.Role = "model"
		default:
			// если появятся tool/system и т.п., тут можно расширить
			c.Role = "user"
		}

		if i == lastUserIdx {
			lastUser = c
		} else {
			history = append(history, c)
		}
	}

	// На всякий случай: если не было user-сообщений
	if lastUser == nil {
		lastUser = &genai.Content{Role: "user", Parts: []genai.Part{genai.Text("")}}
	}
	return
}

func (p *MCPGeminiProvider) convertMCPToGeminiTools(tools []*mcp.Tool) []*genai.FunctionDeclaration {
	out := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		var root jsonschema.Schema
		if t.InputSchema != nil {
			root = *t.InputSchema
		}
		if root.Type == "" && len(root.Types) == 0 {
			root.Type = "object"
		}

		var params *genai.Schema
		if strings.EqualFold(root.Type, "object") || (len(root.Types) > 0 && strings.EqualFold(root.Types[0], "object")) {
			props := map[string]*genai.Schema{}
			if root.Properties != nil {
				for name, sub := range root.Properties {
					props[name] = p.convertProperty(sub)
				}
			}
			params = &genai.Schema{
				Type:        genai.TypeObject,
				Properties:  props,
				Required:    append([]string(nil), root.Required...),
				Description: p.firstNonEmpty(root.Description, root.Title),
			}
		} else {
			params = &genai.Schema{
				Type:        genai.TypeObject,
				Description: p.firstNonEmpty(root.Description, root.Title),
			}
		}

		desc := t.Description
		if desc == "" && t.Annotations != nil {
			desc = t.Annotations.Title
		}

		fd := &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: desc,
			Parameters:  params,
		}
		out = append(out, fd)
	}
	return out
}
