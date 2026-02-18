package server

import "github.com/jcwearn/agent-operator/internal/provider"

// chatBackend identifies which backend handles a chat request.
type chatBackend int

const (
	backendClaude chatBackend = iota
	backendOllama
)

// chatRouter routes chat completion requests to the appropriate backend
// based on the requested model name.
type chatRouter struct {
	registry *provider.Registry
}

func newChatRouter(registry *provider.Registry) *chatRouter {
	return &chatRouter{registry: registry}
}

// route determines which backend should handle a request for the given model.
func (cr *chatRouter) route(model string) chatBackend {
	if cr.registry == nil {
		return backendClaude
	}

	// Check if the model matches any Ollama model.
	ollamaProvider, err := cr.registry.Get("ollama")
	if err == nil {
		for _, m := range ollamaProvider.AvailableModels() {
			if m.ID == model {
				return backendOllama
			}
		}
	}

	// Default to Claude for claude-* models and anything unrecognized.
	return backendClaude
}
