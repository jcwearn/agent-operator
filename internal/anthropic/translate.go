package anthropic

import (
	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
)

// TranslateMessages converts OpenAI-format messages to Anthropic SDK format.
// System messages are extracted separately as Anthropic requires them in a
// dedicated parameter.
func TranslateMessages(msgs []ChatMessage) ([]sdkanthropic.TextBlockParam, []sdkanthropic.MessageParam) {
	var system []sdkanthropic.TextBlockParam
	var messages []sdkanthropic.MessageParam

	for _, msg := range msgs {
		switch msg.Role {
		case "system":
			system = append(system, sdkanthropic.TextBlockParam{Text: msg.Content})
		case "user":
			messages = append(messages, sdkanthropic.NewUserMessage(
				sdkanthropic.NewTextBlock(msg.Content),
			))
		case "assistant":
			messages = append(messages, sdkanthropic.NewAssistantMessage(
				sdkanthropic.NewTextBlock(msg.Content),
			))
		}
	}

	return system, messages
}

// MapModel maps an OpenAI-style model ID to the Anthropic SDK model constant.
func MapModel(model string) sdkanthropic.Model {
	switch model {
	case "claude-sonnet-4-5", "claude-sonnet-4-5-20250929":
		return sdkanthropic.ModelClaudeSonnet4_5_20250929
	case "claude-opus-4", "claude-opus-4-0", "claude-opus-4-20250514":
		return sdkanthropic.ModelClaudeOpus4_20250514
	case "claude-haiku-4-5", "claude-haiku-4-5-20251001":
		return sdkanthropic.ModelClaudeHaiku4_5_20251001
	default:
		// Default to Sonnet for unknown models.
		return sdkanthropic.ModelClaudeSonnet4_5_20250929
	}
}
