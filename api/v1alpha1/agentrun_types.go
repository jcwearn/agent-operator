/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRunStep defines which step of the workflow this run represents.
// +kubebuilder:validation:Enum=plan;implement;test;pull-request
type AgentRunStep string

const (
	AgentRunStepPlan        AgentRunStep = "plan"
	AgentRunStepImplement   AgentRunStep = "implement"
	AgentRunStepTest        AgentRunStep = "test"
	AgentRunStepPullRequest AgentRunStep = "pull-request"
)

// AgentRunPhase defines the current phase of an AgentRun.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type AgentRunPhase string

const (
	AgentRunPhasePending   AgentRunPhase = "Pending"
	AgentRunPhaseRunning   AgentRunPhase = "Running"
	AgentRunPhaseSucceeded AgentRunPhase = "Succeeded"
	AgentRunPhaseFailed    AgentRunPhase = "Failed"
)

// AgentRunSpec defines the desired state of AgentRun.
type AgentRunSpec struct {
	// taskRef is the name of the parent CodingTask.
	// +required
	TaskRef string `json:"taskRef"`

	// step is which workflow step this run corresponds to.
	// +required
	Step AgentRunStep `json:"step"`

	// prompt is the step-specific prompt with context for the agent.
	// +required
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// image is the container image for the agent pod.
	// If empty, the operator's DEFAULT_AGENT_IMAGE env var is used.
	// +optional
	Image string `json:"image,omitempty"`

	// timeout is the maximum duration for this run (e.g., "30m").
	// +kubebuilder:default="30m"
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// repository defines the target Git repository (copied from parent CodingTask).
	// +required
	Repository RepositorySpec `json:"repository"`

	// resources defines resource limits for the agent pod.
	// +optional
	Resources TaskResources `json:"resources,omitempty"`

	// provider is the LLM provider name (e.g., "claude", "ollama").
	// +optional
	Provider string `json:"provider,omitempty"`

	// apiKeyRef references the Secret containing the provider's API key.
	// Not required for providers that don't need API keys (e.g., ollama).
	// +optional
	APIKeyRef *SecretReference `json:"apiKeyRef,omitempty"`

	// baseURL overrides the default API endpoint for the provider.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// anthropicAPIKeyRef references the Secret containing the Anthropic API key.
	// Deprecated: Use provider + apiKeyRef instead. Kept for backward compatibility.
	// +optional
	AnthropicAPIKeyRef SecretReference `json:"anthropicApiKeyRef,omitempty"`

	// gitCredentialsRef references the Secret containing Git credentials.
	// Optional when the operator is configured with a GitHub App (tokens are minted automatically).
	// +optional
	GitCredentialsRef SecretReference `json:"gitCredentialsRef,omitempty"`

	// serviceAccountName is the K8s service account for the agent pod.
	// +kubebuilder:default="agent-runner"
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// context provides additional context from previous steps (e.g., the plan text,
	// test error output from a failed run, etc.).
	// +optional
	Context string `json:"context,omitempty"`

	// model is the model to use for this run (e.g., "sonnet", "qwen2.5:7b").
	// +optional
	Model string `json:"model,omitempty"`

	// maxTurns limits the number of agentic turns for this run.
	// +optional
	MaxTurns *int `json:"maxTurns,omitempty"`
}

// AgentRunStatus defines the observed state of AgentRun.
type AgentRunStatus struct {
	// phase is the current phase of this run.
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`

	// podName is the name of the pod running this agent step.
	// +optional
	PodName string `json:"podName,omitempty"`

	// startedAt is when the agent pod started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the agent pod finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// output is the agent's primary output (e.g., plan text, test results, PR URL).
	// +optional
	Output string `json:"output,omitempty"`

	// exitCode is the exit code of the agent container.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// message provides a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`

	// conditions represent the current state of the AgentRun.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.spec.taskRef`
// +kubebuilder:printcolumn:name="Step",type=string,JSONPath=`.spec.step`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRun is the Schema for the agentruns API.
// It represents a single step execution within a CodingTask workflow.
type AgentRun struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AgentRunSpec `json:"spec"`

	// +optional
	Status AgentRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
