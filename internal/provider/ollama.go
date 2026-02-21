package provider

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

const defaultOllamaBaseURL = "http://ollama.ollama.svc.cluster.local:11434"

// Ollama implements the Provider interface for local Ollama models via OpenCode.
type Ollama struct {
	// BaseURL overrides the default Ollama endpoint.
	BaseURL string
	// Image overrides the default agent-runner-opencode image.
	Image string
}

func (o *Ollama) Name() string { return "ollama" }

func (o *Ollama) DisplayName() string { return "OpenCode + Ollama" }

func (o *Ollama) ProviderDescription() string {
	return "Local models (Qwen 2.5 7B, 3B, 1.5B) via OpenCode + Ollama"
}

func (o *Ollama) DefaultImage() string {
	if o.Image != "" {
		return o.Image
	}
	return "ghcr.io/jcwearn/agent-runner-opencode:latest"
}

func (o *Ollama) DefaultModel() string { return "qwen2.5:7b" }

func (o *Ollama) DefaultModelForStep(_ agentsv1alpha1.AgentRunStep) string {
	return "qwen2.5:7b"
}

func (o *Ollama) APIKeyEnvVar() string { return "OPENAI_API_KEY" }

func (o *Ollama) APIKeyRequired() bool { return false }

func (o *Ollama) DefaultBaseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return defaultOllamaBaseURL
}

func (o *Ollama) AvailableModels() []ModelInfo {
	return []ModelInfo{
		{ID: "qwen2.5:7b", DisplayName: "Qwen 2.5 7B", Description: "best local quality", OwnedBy: "ollama"},
		{ID: "qwen2.5:3b", DisplayName: "Qwen 2.5 3B", Description: "balanced local model", OwnedBy: "ollama"},
		{ID: "qwen2.5:1.5b", DisplayName: "Qwen 2.5 1.5B", Description: "fastest local model", OwnedBy: "ollama"},
	}
}

func (o *Ollama) PodEnvVars(_ *agentsv1alpha1.SecretReference, baseURL string) []corev1.EnvVar {
	url := baseURL
	if url == "" {
		url = o.DefaultBaseURL()
	}
	// Append /v1 so the OpenAI-compatible SDK constructs the correct
	// /v1/chat/completions path against Ollama's OpenAI-compatible endpoint.
	apiBase := strings.TrimRight(url, "/") + "/v1"
	return []corev1.EnvVar{
		{Name: "OPENAI_API_BASE", Value: apiBase},
		{Name: "OPENAI_API_KEY", Value: "ollama"},
	}
}
