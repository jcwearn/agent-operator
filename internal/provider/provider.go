package provider

import (
	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

// ModelInfo describes a model available from a provider.
type ModelInfo struct {
	// ID is the model identifier used in API requests (e.g., "sonnet", "qwen2.5:7b").
	ID string

	// DisplayName is the human-readable name shown in UIs (e.g., "Sonnet 4.5").
	DisplayName string

	// Description is a short description of the model.
	Description string

	// OwnedBy is the provider/owner of the model (e.g., "anthropic", "ollama").
	OwnedBy string
}

// Provider defines the interface for an LLM provider.
type Provider interface {
	// Name returns the provider identifier (e.g., "claude", "ollama").
	Name() string

	// DisplayName returns the human-readable provider name (e.g., "Claude Code", "Aider + Ollama").
	DisplayName() string

	// ProviderDescription returns a short description of the provider and its models.
	ProviderDescription() string

	// DefaultImage returns the default agent-runner container image for this provider.
	DefaultImage() string

	// DefaultModel returns the default model for this provider.
	DefaultModel() string

	// DefaultModelForStep returns the default model for a specific workflow step.
	DefaultModelForStep(step agentsv1alpha1.AgentRunStep) string

	// APIKeyEnvVar returns the environment variable name for the API key.
	APIKeyEnvVar() string

	// APIKeyRequired returns true if this provider requires an API key.
	APIKeyRequired() bool

	// DefaultBaseURL returns the default base URL for this provider's API.
	// Returns empty string if no base URL is needed (e.g., Claude uses SDK defaults).
	DefaultBaseURL() string

	// AvailableModels returns the list of models available from this provider.
	AvailableModels() []ModelInfo

	// PodEnvVars returns the environment variables to inject into agent pods.
	// apiKeyRef may be nil for providers that don't require API keys.
	PodEnvVars(apiKeyRef *agentsv1alpha1.SecretReference, baseURL string) []corev1.EnvVar
}
