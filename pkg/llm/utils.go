package llm

import (
	"LLM_Chat/internal/storage/models"
)

// ConvertToLLMMessages converts storage models to LLM messages
func ConvertToLLMMessages(storageMessages []models.Message) []Message {
	llmMessages := make([]Message, len(storageMessages))

	for i, msg := range storageMessages {
		llmMessages[i] = Message{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}

	return llmMessages
}

// ConvertFromLLMMessage converts LLM message to storage model
func ConvertFromLLMMessage(llmMsg Message, sessionID string) models.Message {
	return models.Message{
		SessionID: sessionID,
		Role:      llmMsg.Role,
		Content:   llmMsg.Content,
	}
}

// AddSystemMessage добавляет системное сообщение в начало, если его нет
func AddSystemMessage(messages []Message, systemPrompt string) []Message {
	if len(messages) == 0 || messages[0].Role != "system" {
		systemMsg := Message{
			Role:    "system",
			Content: systemPrompt,
		}
		return append([]Message{systemMsg}, messages...)
	}
	return messages
}

// TrimMessages обрезает сообщения до указанного лимита, сохраняя системное сообщение
func TrimMessages(messages []Message, limit int) []Message {
	if len(messages) <= limit {
		return messages
	}

	// Если есть системное сообщение, сохраняем его
	if len(messages) > 0 && messages[0].Role == "system" {
		systemMsg := messages[0]
		remaining := messages[1:]

		if len(remaining) <= limit-1 {
			return messages
		}

		// Берём последние (limit-1) сообщений + системное
		trimmed := remaining[len(remaining)-(limit-1):]
		return append([]Message{systemMsg}, trimmed...)
	}

	// Берём последние limit сообщений
	return messages[len(messages)-limit:]
}
