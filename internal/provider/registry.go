package provider

import "fmt"

// Registry holds all registered providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a new provider registry with default providers.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		providers: map[string]Provider{
			"claude": &Claude{},
			"ollama": &Ollama{},
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RegistryOption configures the registry.
type RegistryOption func(*Registry)

// WithOllamaBaseURL sets a custom base URL for the Ollama provider.
func WithOllamaBaseURL(url string) RegistryOption {
	return func(r *Registry) {
		if url != "" {
			r.providers["ollama"] = &Ollama{BaseURL: url}
		}
	}
}

// Get returns a provider by name, or an error if not found.
func (r *Registry) Get(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %q", name)
	}
	return p, nil
}

// MustGet returns a provider by name, panicking if not found.
func (r *Registry) MustGet(name string) Provider {
	p, err := r.Get(name)
	if err != nil {
		panic(err)
	}
	return p
}

// All returns all registered providers.
func (r *Registry) All() map[string]Provider {
	return r.providers
}

// AllModels returns all models from all providers.
func (r *Registry) AllModels() []ModelInfo {
	var models []ModelInfo
	for _, p := range r.providers {
		models = append(models, p.AvailableModels()...)
	}
	return models
}
