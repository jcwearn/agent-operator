package anthropic

import (
	"time"

	"github.com/jcwearn/agent-operator/internal/provider"
)

// ChatCompletionRequest is the OpenAI-compatible chat completion request.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

// ChatMessage is a single message in the OpenAI-compatible format.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse is the OpenAI-compatible non-streaming response.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *CompletionUsage       `json:"usage,omitempty"`
}

// ChatCompletionChoice is a single choice in the response.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// CompletionUsage tracks token usage.
type CompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionChunk is the OpenAI-compatible streaming chunk.
type ChatCompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []ChatChunkChoice `json:"choices"`
	Usage   *CompletionUsage  `json:"usage,omitempty"`
}

// ChatChunkChoice is a single choice in a streaming chunk.
type ChatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        ChatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

// ChatChunkDelta is the delta content in a streaming chunk.
type ChatChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ModelsResponse is the OpenAI-compatible models list response.
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// Model is a single model in the models list.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// NewModelsResponse returns the hardcoded list of Claude models (fallback).
func NewModelsResponse() ModelsResponse {
	now := time.Now().Unix()
	return ModelsResponse{
		Object: "list",
		Data: []Model{
			{ID: "claude-sonnet-4-5", Object: "model", Created: now, OwnedBy: "anthropic"},
			{ID: "claude-opus-4", Object: "model", Created: now, OwnedBy: "anthropic"},
			{ID: "claude-haiku-4-5", Object: "model", Created: now, OwnedBy: "anthropic"},
		},
	}
}

// NewModelsResponseFromProviders builds a models list from the provider registry.
func NewModelsResponseFromProviders(registry *provider.Registry) ModelsResponse {
	now := time.Now().Unix()
	allModels := registry.AllModels()
	data := make([]Model, 0, len(allModels))
	for _, m := range allModels {
		data = append(data, Model{
			ID:      m.ID,
			Object:  "model",
			Created: now,
			OwnedBy: m.OwnedBy,
		})
	}
	return ModelsResponse{
		Object: "list",
		Data:   data,
	}
}
