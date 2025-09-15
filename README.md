# LLM Chat

Sample configuration demonstrating MCP parameters:

```yaml
llm:
  provider: openrouter
  base_url: https://openrouter.ai/api/v1
  model: google/gemma-3-27b-it:free
  api_key: YOUR_API_KEY
  server_url: http://localhost:8000/mcp
  system_prompt_path: system_prompt.txt
  http_headers:
    X-Custom-Header: value
```

Place this config in `configs/config.yaml` and adjust values to match your environment.
