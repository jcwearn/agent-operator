package provider

import (
	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

// Claude implements the Provider interface for Anthropic's Claude models.
type Claude struct{}

func (c *Claude) Name() string { return "claude" }

func (c *Claude) DisplayName() string { return "Claude Code" }

func (c *Claude) ProviderDescription() string {
	return "Claude models (Sonnet 4.5, Opus 4, Haiku 4.5) via Anthropic API"
}

func (c *Claude) DefaultImage() string { return "ghcr.io/jcwearn/agent-runner:latest" }

func (c *Claude) DefaultModel() string { return "sonnet" }

func (c *Claude) DefaultModelForStep(step agentsv1alpha1.AgentRunStep) string {
	switch step {
	case agentsv1alpha1.AgentRunStepTest, agentsv1alpha1.AgentRunStepPullRequest:
		return "haiku"
	default:
		return "sonnet"
	}
}

func (c *Claude) APIKeyEnvVar() string { return "ANTHROPIC_API_KEY" }

func (c *Claude) APIKeyRequired() bool { return true }

func (c *Claude) DefaultBaseURL() string { return "" }

func (c *Claude) AvailableModels() []ModelInfo {
	return []ModelInfo{
		{ID: "sonnet", DisplayName: "Sonnet 4.5", Description: "balanced speed and capability", OwnedBy: "anthropic"},
		{ID: "opus", DisplayName: "Opus 4", Description: "most capable, slower", OwnedBy: "anthropic"},
		{ID: "haiku", DisplayName: "Haiku 4.5", Description: "fastest, lower cost", OwnedBy: "anthropic"},
	}
}

func (c *Claude) PodEnvVars(apiKeyRef *agentsv1alpha1.SecretReference, _ string) []corev1.EnvVar {
	if apiKeyRef == nil {
		return nil
	}
	return []corev1.EnvVar{
		{
			Name: "ANTHROPIC_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: apiKeyRef.Name,
					},
					Key: apiKeyRef.Key,
				},
			},
		},
	}
}
