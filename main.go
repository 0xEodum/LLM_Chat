package main

import (
	"bufio"
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
	"time"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/api/option"
)

//
// ===============================
// Конвертация JSON Schema → genai
// ===============================
//

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

// утилита: извлекаем описание из description/title
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// попытка сконвертировать enum к []string (Gemini поддерживает string-энумы)
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

// Конвертация одного свойства jsonschema → *genai.Schema (рекурсивно)
func convertProperty(s *jsonschema.Schema) *genai.Schema {
	// на всякий случай
	if s == nil {
		return &genai.Schema{Type: genai.TypeString}
	}

	// anyOf: берём первое под-схему, чей тип не равен "null"
	if len(s.AnyOf) > 0 {
		for _, sub := range s.AnyOf {
			if sub == nil {
				continue
			}
			// тип может быть в Type или в Types
			t := strings.ToLower(strings.TrimSpace(sub.Type))
			if t == "" && len(sub.Types) > 0 {
				t = strings.ToLower(strings.TrimSpace(sub.Types[0]))
			}
			if t != "null" {
				return convertProperty(sub)
			}
		}
		// fallback
		return &genai.Schema{Type: genai.TypeString}
	}

	// Выбираем тип: либо Type, либо первый из Types
	propType := s.Type
	if propType == "" && len(s.Types) > 0 {
		propType = s.Types[0]
	}
	gType := schemaTypeToGenai(propType)
	desc := firstNonEmpty(s.Description, s.Title)
	enumVals := toStringEnum(s.Enum)

	switch gType {
	case genai.TypeArray:
		// В go-sdk jsonschema.Schema обычно имеет Items *Schema (draft 2020-12)
		var itemSchema *jsonschema.Schema
		if s.Items != nil {
			itemSchema = s.Items
		}
		return &genai.Schema{
			Type:        genai.TypeArray,
			Items:       convertProperty(itemSchema),
			Description: desc,
			Enum:        enumVals, // обычно enum на массивах не используют, но не мешает
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
		// примитивы
		return &genai.Schema{
			Type:        gType,
			Description: desc,
			Enum:        enumVals,
		}
	}
}

// MCP tools → Gemini FunctionDeclaration
func convertMCPToGeminiTools(tools []*mcp.Tool) []*genai.FunctionDeclaration {
	out := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		// гарантируем корневой OBJECT
		var root jsonschema.Schema
		if t.InputSchema != nil {
			root = *t.InputSchema
		}
		if root.Type == "" && len(root.Types) == 0 {
			root.Type = "object"
		}

		// сконвертировать свойства
		var params *genai.Schema
		if strings.EqualFold(root.Type, "object") || (len(root.Types) > 0 && strings.EqualFold(root.Types[0], "object")) {
			props := map[string]*genai.Schema{}
			if root.Properties != nil {
				for name, sub := range root.Properties {
					props[name] = convertProperty(sub)
				}
			}
			params = &genai.Schema{
				Type:        genai.TypeObject,
				Properties:  props,
				Required:    append([]string(nil), root.Required...),
				Description: firstNonEmpty(root.Description, root.Title),
			}
		} else {
			// на всякий случай, если корень неожиданный
			params = &genai.Schema{
				Type:        genai.TypeObject,
				Description: firstNonEmpty(root.Description, root.Title),
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

//
// ==================
// MCP клиент (Go)
// ==================
//

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

func defaultConfig() MCPClientConfig {
	return MCPClientConfig{
		ServerURL:   envOr("MCP_SERVER_URL", "http://localhost:8000/mcp"),
		HTTPHeaders: nil,

		GeminiAPIKey:  envOr("GEMINI_API_KEY", "sk-..."),
		GeminiBaseURL: envOr("GEMINI_BASE_URL", "https://api.proxyapi.ru/google"),
		GeminiModel:   envOr("GEMINI_MODEL", "gemini-2.5-flash"),

		// путь к системному промпту
		SystemPromptPath: envOr("SYSTEM_PROMPT_PATH", "system_prompt.txt"),

		// (опционально) старые поля для stdio-режима как fallback:
		ServerPath: "mcp_server.py",
		PythonPath: envOr("PYTHON", "python3"),
		ServerEnv:  nil,
	}
}

type MCPClient struct {
	cfg           MCPClientConfig
	mcpClient     *mcp.Client
	session       *mcp.ClientSession
	genClient     *genai.Client
	model         *genai.GenerativeModel
	chat          *genai.ChatSession
	available     []*mcp.Tool
	geminiTools   []*genai.FunctionDeclaration
	connectedProc *exec.Cmd
	systemPrompt  string // кешированный системный промпт
}

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
		return nil // ok: go-sdk возьмёт http.DefaultClient
	}
	rt := http.DefaultTransport
	return &http.Client{Transport: &headerRoundTripper{next: rt, headers: headers}}
}

// loadSystemPrompt загружает системный промпт из файла
func (c *MCPClient) loadSystemPrompt() error {
	if c.cfg.SystemPromptPath == "" {
		return errors.New("путь к файлу системного промпта не указан")
	}

	// Проверяем существование файла
	if _, err := os.Stat(c.cfg.SystemPromptPath); os.IsNotExist(err) {
		// Если файл не существует, создаем его с дефолтным промптом
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

func (c *MCPClient) Start(ctx context.Context) error {
	log.Println("📝 Загрузка системного промпта...")
	if err := c.loadSystemPrompt(); err != nil {
		return fmt.Errorf("ошибка загрузки системного промпта: %w", err)
	}

	log.Println("🌐 Подключение к MCP по Streamable HTTP…")

	// 1) Транспорт: streamable HTTP
	transport := &mcp.StreamableClientTransport{
		Endpoint:   strings.TrimRight(c.cfg.ServerURL, "/"),
		HTTPClient: httpClientWithHeaders(c.cfg.HTTPHeaders),
		// MaxRetries: 5 по умолчанию; при желании задайте своё значение
	}

	// 2) Клиент + соединение
	impl := &mcp.Implementation{Name: "go-mcp-client", Version: "0.2.0"}
	client := mcp.NewClient(impl, &mcp.ClientOptions{})

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP (streamable-http): %w", err)
	}
	c.mcpClient = client
	c.session = session

	// 3) Список инструментов
	log.Println("📋 Получение списка инструментов…")
	ltr, err := c.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		c.Stop()
		return fmt.Errorf("list tools: %w", err)
	}
	c.available = ltr.Tools
	log.Printf("✅ Получено %d инструментов\n", len(c.available))
	for _, t := range c.available {
		log.Printf("  - %s: %s", t.Name, t.Description)
	}

	// 4) Интеграция с Gemini — как было
	log.Println("🔄 Конвертация инструментов для Gemini…")
	c.geminiTools = convertMCPToGeminiTools(c.available)

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

func (c *MCPClient) Stop() {
	log.Println("🛑 Завершение работы...")
	if c.session != nil {
		_ = c.session.Close() // отправит DELETE на /mcp с session-id
	}
	if c.genClient != nil {
		_ = c.genClient.Close()
	}
}

func (c *MCPClient) callMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	log.Printf("🔧 Вызов MCP инструмента: %s\n", name)
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	if res.IsError {
		// попытаемся вытащить текст ошибки из контента
		msg := "tool error"
		for _, ct := range res.Content {
			if tc, ok := ct.(*mcp.TextContent); ok && strings.TrimSpace(tc.Text) != "" {
				msg = tc.Text
				break
			}
		}
		return map[string]any{"error": msg}, nil
	}

	// Если сервер вернул structuredContent — отлично
	if res.StructuredContent != nil {
		// гарантируем map[string]any (если это был типизированный output)
		switch v := res.StructuredContent.(type) {
		case map[string]any:
			return v, nil
		default:
			// попробуем через JSON маршал/анмаршал привести к объекту
			b, _ := json.Marshal(v)
			m := map[string]any{}
			if err := json.Unmarshal(b, &m); err == nil {
				return m, nil
			}
			// в крайнем случае — завернём как result:string
			return map[string]any{"result": string(b)}, nil
		}
	}

	// Иначе склеим текстовый результат (если есть)
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
	// ничего не пришло — вернём пустышку
	return map[string]any{"result": nil}, nil
}

// Итеративная обработка запроса (аналог Python process_query)
func (c *MCPClient) ProcessQuery(ctx context.Context, userQuery string, maxIterations int) (string, error) {
	log.Printf("\n📝 Обработка запроса: %s\n", userQuery)

	// Используем загруженный системный промпт
	if c.systemPrompt == "" {
		return "", errors.New("системный промпт не загружен")
	}

	// Первый ход: кладём весь system + вопрос в одно пользовательское сообщение (как в Python)
	firstTurn := genai.Text(c.systemPrompt + "\n\nВопрос пользователя: " + userQuery)

	var lastTextAnswer string

	for i := 0; i < maxIterations; i++ {
		log.Printf("\n🔄 Итерация %d/%d\n", i+1, maxIterations)

		var resp *genai.GenerateContentResponse
		var err error
		if i == 0 {
			resp, err = c.chat.SendMessage(ctx, firstTurn)
		} else {
			// Пустой 'толчок' после FunctionResponse — модель использует историю
			resp, err = c.chat.SendMessage(ctx, genai.Text(""))
		}
		if err != nil {
			return "", fmt.Errorf("gemini generate: %w", err)
		}
		if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return "", errors.New("❌ Не удалось получить ответ от LLM")
		}

		cand := resp.Candidates[0]
		// Собираем все вызовы функций
		fcalls := cand.FunctionCalls()
		// Собираем текстовые части (на случай, если это уже финал)
		var textParts []string
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				if t, ok := p.(genai.Text); ok && strings.TrimSpace(string(t)) != "" {
					textParts = append(textParts, string(t))
				}
			}
		}

		if len(fcalls) == 0 {
			// финальный ответ
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
				// В случае исключения заворачиваем в error-поле
				result = map[string]any{"error": err.Error()}
			}

			// Добавляем tool-ответ в историю с корректной ролью
			toolContent := &genai.Content{
				Role:  "tool", // роль для function response
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

func flattenEnv(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

func main() {

	userQuery := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	fmt.Print("Введите ваш вопрос: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	userQuery = strings.TrimSpace(line)

	// Конфиг (аналог Python)
	cfg := defaultConfig()
	cfg.ServerURL = "http://localhost:8000/mcp"
	cfg.HTTPHeaders = nil

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := NewMCPClient(cfg)
	if err := client.Start(ctx); err != nil {
		log.Fatalf("Старт клиента: %v", err)
	}
	defer client.Stop()

	answer, err := client.ProcessQuery(ctx, userQuery, 10)
	if err != nil {
		log.Fatalf("Ошибка: %v", err)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println("🎯 ИТОГОВЫЙ ОТВЕТ:")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Println(answer)
	fmt.Println(strings.Repeat("=", 80))
}
