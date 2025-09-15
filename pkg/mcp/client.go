package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/api/option"
)

// schemaTypeToGenai converts JSON schema type to genai type.
func schemaTypeToGenai(t string) genai.Type {
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

// firstNonEmpty returns the first non-empty string from the list.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// toStringEnum converts enum values to []string if possible.
func toStringEnum(vals []any) []string {
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

// convertProperty recursively converts jsonschema.Schema to genai.Schema.
func convertProperty(s *jsonschema.Schema) *genai.Schema {
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
				return convertProperty(sub)
			}
		}
		return &genai.Schema{Type: genai.TypeString}
	}
	propType := s.Type
	if propType == "" && len(s.Types) > 0 {
		propType = s.Types[0]
	}
	gType := schemaTypeToGenai(propType)
	desc := firstNonEmpty(s.Description, s.Title)
	enumVals := toStringEnum(s.Enum)

	switch gType {
	case genai.TypeArray:
		var itemSchema *jsonschema.Schema
		if s.Items != nil {
			itemSchema = s.Items
		}
		return &genai.Schema{
			Type:        genai.TypeArray,
			Items:       convertProperty(itemSchema),
			Description: desc,
			Enum:        enumVals,
		}
	case genai.TypeObject:
		props := map[string]*genai.Schema{}
		if s.Properties != nil {
			for name, sub := range s.Properties {
				props[name] = convertProperty(sub)
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

// ConvertMCPToGeminiTools converts MCP tools to Gemini FunctionDeclaration.
// The function is exported so other packages (e.g. Gemini MCP provider)
// can leverage the same conversion logic.
func ConvertMCPToGeminiTools(tools []*sdkmcp.Tool) []*genai.FunctionDeclaration {
	out := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		var root jsonschema.Schema
		if t.InputSchema != nil {
			root = *t.InputSchema
		}
		if strings.ToLower(strings.TrimSpace(root.Type)) != "object" {
			root.Type = "object"
		}
		params := convertProperty(&root)
		out = append(out, &genai.FunctionDeclaration{
			Name:       t.Name,
			Parameters: params,
			Description: firstNonEmpty(
				t.Description,
				t.Title,
			),
		})
	}
	return out
}

// MCPClientConfig holds configuration for MCPClient.
type MCPClientConfig struct {
	ServerPath       string
	ServerEnv        map[string]string
	PythonPath       string
	GeminiAPIKey     string
	GeminiBaseURL    string
	GeminiModel      string
	ServerURL        string
	HTTPHeaders      map[string]string
	SystemPromptPath string
}

// DefaultConfig returns configuration populated from environment variables.
func DefaultConfig() MCPClientConfig {
	return MCPClientConfig{
		ServerURL:   envOr("MCP_SERVER_URL", "http://localhost:8000/mcp"),
		HTTPHeaders: nil,

		GeminiAPIKey:  envOr("GEMINI_API_KEY", "sk-..."),
		GeminiBaseURL: envOr("GEMINI_BASE_URL", "https://api.proxyapi.ru/google"),
		GeminiModel:   envOr("GEMINI_MODEL", "gemini-2.5-flash"),

		SystemPromptPath: envOr("SYSTEM_PROMPT_PATH", "system_prompt.txt"),

		ServerPath: "mcp_server.py",
		PythonPath: envOr("PYTHON", "python3"),
		ServerEnv:  nil,
	}
}

// MCPClient represents the client interacting with MCP server and Gemini LLM.
type MCPClient struct {
	cfg           MCPClientConfig
	mcpClient     *sdkmcp.Client
	session       *sdkmcp.ClientSession
	genClient     *genai.Client
	model         *genai.GenerativeModel
	chat          *genai.ChatSession
	available     []*sdkmcp.Tool
	geminiTools   []*genai.FunctionDeclaration
	connectedProc *exec.Cmd
	systemPrompt  string
}

// NewMCPClient creates a new MCPClient instance.
func NewMCPClient(cfg MCPClientConfig) *MCPClient {
	return &MCPClient{cfg: cfg}
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

func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	rt := http.DefaultTransport
	return &http.Client{Transport: &headerRoundTripper{next: rt, headers: headers}}
}

// loadSystemPrompt loads system prompt from file.
func (c *MCPClient) loadSystemPrompt() error {
	if c.cfg.SystemPromptPath == "" {
		return errors.New("путь к файлу системного промпта не указан")
	}
	if _, err := os.Stat(c.cfg.SystemPromptPath); os.IsNotExist(err) {
		log.Printf("📝 Файл системного промпта не найден %s", c.cfg.SystemPromptPath)
		return nil
	}
	file, err := os.Open(c.cfg.SystemPromptPath)
	if err != nil {
		return fmt.Errorf("не удалось открыть файл системного промпта '%s': %w", c.cfg.SystemPromptPath, err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("не удалось прочитать файл системного промпта '%s': %w", c.cfg.SystemPromptPath, err)
	}
	c.systemPrompt = strings.TrimSpace(string(content))
	if c.systemPrompt == "" {
		return fmt.Errorf("файл системного промпта '%s' пуст", c.cfg.SystemPromptPath)
	}
	log.Printf("✅ Системный промпт загружен из файла: %s (%d символов)", c.cfg.SystemPromptPath, len(c.systemPrompt))
	return nil
}

// Start initializes connection to MCP server and Gemini client.
func (c *MCPClient) Start(ctx context.Context) error {
	log.Println("📝 Загрузка системного промпта...")
	if err := c.loadSystemPrompt(); err != nil {
		return fmt.Errorf("ошибка загрузки системного промпта: %w", err)
	}

	log.Println("🌐 Подключение к MCP по Streamable HTTP…")
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:   strings.TrimRight(c.cfg.ServerURL, "/"),
		HTTPClient: httpClientWithHeaders(c.cfg.HTTPHeaders),
	}

	impl := &sdkmcp.Implementation{Name: "go-mcp-client", Version: "0.2.0"}
	client := sdkmcp.NewClient(impl, &sdkmcp.ClientOptions{})

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c.mcpClient = client
	c.session = session

	log.Println("📋 Получение списка инструментов…")
	ltr, err := c.session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		c.Stop()
		return fmt.Errorf("list tools: %w", err)
	}
	c.available = ltr.Tools
	log.Printf("✅ Получено %d инструментов\n", len(c.available))
	for _, t := range c.available {
		log.Printf("  - %s: %s", t.Name, t.Description)
	}

	log.Println("🔄 Конвертация инструментов для Gemini…")
	c.geminiTools = ConvertMCPToGeminiTools(c.available)

	log.Println("🤖 Инициализация Gemini клиента…")
	opts := []option.ClientOption{option.WithAPIKey(c.cfg.GeminiAPIKey)}
	if strings.TrimSpace(c.cfg.GeminiBaseURL) != "" {
		opts = append(opts, option.WithEndpoint(strings.TrimRight(c.cfg.GeminiBaseURL, "/")))
	}
	genClient, err := genai.NewClient(ctx, opts...)
	if err != nil {
		c.Stop()
		return fmt.Errorf("genai client: %w", err)
	}
	c.genClient = genClient
	c.model = c.genClient.GenerativeModel(c.cfg.GeminiModel)
	c.model.Tools = []*genai.Tool{{FunctionDeclarations: c.geminiTools}}
	c.chat = c.model.StartChat()

	log.Println("✅ Все компоненты готовы!")
	return nil
}

// Stop closes all underlying connections.
func (c *MCPClient) Stop() {
	log.Println("🛑 Завершение работы...")
	if c.session != nil {
		_ = c.session.Close()
	}
	if c.genClient != nil {
		_ = c.genClient.Close()
	}
}

// callMCPTool invokes an MCP tool by name with arguments.
func (c *MCPClient) callMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	log.Printf("🔧 Вызов MCP инструмента: %s\n", name)
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	if res.IsError {
		msg := "tool error"
		for _, ct := range res.Content {
			if tc, ok := ct.(*sdkmcp.TextContent); ok && strings.TrimSpace(tc.Text) != "" {
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
		if tc, ok := ct.(*sdkmcp.TextContent); ok {
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

// ProcessQuery iteratively processes user query via Gemini and MCP tools.
func (c *MCPClient) ProcessQuery(ctx context.Context, userQuery string, maxIterations int) (string, error) {
	log.Printf("\n📝 Обработка запроса: %s\n", userQuery)
	if c.systemPrompt == "" {
		return "", errors.New("системный промпт не загружен")
	}
	firstTurn := genai.Text(c.systemPrompt + "\n\nВопрос пользователя: " + userQuery)
	var lastTextAnswer string
	for i := 0; i < maxIterations; i++ {
		log.Printf("\n🔄 Итерация %d/%d\n", i+1, maxIterations)
		var resp *genai.GenerateContentResponse
		var err error
		if i == 0 {
			resp, err = c.chat.SendMessage(ctx, firstTurn)
		} else {
			resp, err = c.chat.SendMessage(ctx, genai.Text(""))
		}
		if err != nil {
			return "", fmt.Errorf("gemini generate: %w", err)
		}
		if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return "", errors.New("❌ Не удалось получить ответ от LLM")
		}
		cand := resp.Candidates[0]
		fcalls := cand.FunctionCalls()
		var textParts []string
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				if t, ok := p.(genai.Text); ok && strings.TrimSpace(string(t)) != "" {
					textParts = append(textParts, string(t))
				}
			}
		}
		if len(fcalls) == 0 {
			lastTextAnswer = strings.Join(textParts, "\n")
			if strings.TrimSpace(lastTextAnswer) == "" {
				lastTextAnswer = "Нет текстового ответа"
			}
			log.Println("✅ Получен финальный ответ")
			return lastTextAnswer, nil
		}
		for _, fc := range fcalls {
			args := fc.Args
			if args == nil {
				args = map[string]any{}
			}
			result, err := c.callMCPTool(ctx, fc.Name, args)
			if err != nil {
				result = map[string]any{"error": err.Error()}
			}
			toolContent := &genai.Content{
				Role:  "tool",
				Parts: []genai.Part{genai.FunctionResponse{Name: fc.Name, Response: result}},
			}
			c.chat.History = append(c.chat.History, toolContent)
		}
	}
	return "⚠️ Достигнут лимит итераций без финального ответа", nil
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
