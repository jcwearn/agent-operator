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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskSourceType defines where a task originated from.
// +kubebuilder:validation:Enum=github-issue;chat
type TaskSourceType string

const (
	TaskSourceGitHubIssue TaskSourceType = "github-issue"
	TaskSourceChat        TaskSourceType = "chat"
)

// TaskPhase defines the current phase of a CodingTask.
// +kubebuilder:validation:Enum=Pending;Planning;Implementing;Testing;PullRequest;Complete;Failed
type TaskPhase string

const (
	TaskPhasePending      TaskPhase = "Pending"
	TaskPhasePlanning     TaskPhase = "Planning"
	TaskPhaseImplementing TaskPhase = "Implementing"
	TaskPhaseTesting      TaskPhase = "Testing"
	TaskPhasePullRequest  TaskPhase = "PullRequest"
	TaskPhaseComplete     TaskPhase = "Complete"
	TaskPhaseFailed       TaskPhase = "Failed"
)

// GitHubSource contains information about a GitHub issue source.
type GitHubSource struct {
	// owner is the GitHub repository owner.
	// +required
	Owner string `json:"owner"`

	// repo is the GitHub repository name.
	// +required
	Repo string `json:"repo"`

	// issueNumber is the GitHub issue number.
	// +required
	IssueNumber int `json:"issueNumber"`
}

// ChatSource contains information about a chat-originated task.
type ChatSource struct {
	// sessionID is the chat session identifier.
	// +required
	SessionID string `json:"sessionId"`

	// userID is the user who initiated the task.
	// +required
	UserID string `json:"userId"`
}

// TaskSource defines where the task originated from.
type TaskSource struct {
	// type is the source type (github-issue or chat).
	// +required
	Type TaskSourceType `json:"type"`

	// github contains GitHub issue details when type is github-issue.
	// +optional
	GitHub *GitHubSource `json:"github,omitempty"`

	// chat contains chat session details when type is chat.
	// +optional
	Chat *ChatSource `json:"chat,omitempty"`
}

// RepositorySpec defines the target repository for the coding task.
type RepositorySpec struct {
	// url is the Git clone URL for the repository.
	// +required
	URL string `json:"url"`

	// branch is the base branch to work from (e.g., "main").
	// +kubebuilder:default="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// workBranch is the branch the agent will create for its changes.
	// If empty, one will be generated from the task name.
	// +optional
	WorkBranch string `json:"workBranch,omitempty"`
}

// TaskResources defines resource limits for agent pods.
type TaskResources struct {
	// cpu is the CPU limit for agent pods (e.g., "4").
	// +kubebuilder:default="4"
	// +optional
	CPU string `json:"cpu,omitempty"`

	// memory is the memory limit for agent pods (e.g., "8Gi").
	// +kubebuilder:default="8Gi"
	// +optional
	Memory string `json:"memory,omitempty"`

	// storage is the PVC size for the workspace (e.g., "20Gi").
	// +kubebuilder:default="20Gi"
	// +optional
	Storage string `json:"storage,omitempty"`
}

// SecretReference refers to a Kubernetes secret by name and key.
type SecretReference struct {
	// name is the name of the Secret.
	// +required
	Name string `json:"name"`

	// key is the key within the Secret.
	// +required
	Key string `json:"key"`
}

// CodingTaskSpec defines the desired state of CodingTask.
type CodingTaskSpec struct {
	// source defines where the task originated.
	// +required
	Source TaskSource `json:"source"`

	// repository defines the target Git repository.
	// +required
	Repository RepositorySpec `json:"repository"`

	// prompt is the task description / instructions for the agent.
	// +required
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// agentImage is the container image for agent pods.
	// +kubebuilder:default="ghcr.io/jcwearn/agent-runner:latest"
	// +optional
	AgentImage string `json:"agentImage,omitempty"`

	// resources defines resource limits for agent pods.
	// +optional
	Resources TaskResources `json:"resources,omitempty"`

	// anthropicAPIKeyRef references the Secret containing the Anthropic API key.
	// +required
	AnthropicAPIKeyRef SecretReference `json:"anthropicApiKeyRef"`

	// gitCredentialsRef references the Secret containing Git credentials (e.g., GitHub token).
	// +required
	GitCredentialsRef SecretReference `json:"gitCredentialsRef"`

	// maxRetries is the maximum number of retries per step on failure.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRetries int `json:"maxRetries,omitempty"`

	// stepTimeout is the default timeout for each agent step (e.g., "30m").
	// +kubebuilder:default="30m"
	// +optional
	StepTimeout string `json:"stepTimeout,omitempty"`

	// serviceAccountName is the K8s service account for agent pods.
	// +kubebuilder:default="agent-runner"
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// nodeSelector for agent pods.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// tolerations for agent pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

// PullRequestInfo contains information about the created pull request.
type PullRequestInfo struct {
	// url is the web URL of the pull request.
	URL string `json:"url"`

	// number is the PR number.
	Number int `json:"number"`
}

// AgentRunReference is a lightweight reference to an AgentRun.
type AgentRunReference struct {
	// name is the AgentRun resource name.
	Name string `json:"name"`

	// step is the workflow step this run corresponds to.
	Step AgentRunStep `json:"step"`

	// phase is the current phase of this run.
	Phase AgentRunPhase `json:"phase"`
}

// CodingTaskStatus defines the observed state of CodingTask.
type CodingTaskStatus struct {
	// phase is the current workflow phase.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// plan is the generated implementation plan text.
	// +optional
	Plan string `json:"plan,omitempty"`

	// currentStep is the current step number (1-based).
	// +optional
	CurrentStep int `json:"currentStep,omitempty"`

	// totalSteps is the total number of steps in the workflow.
	// +optional
	TotalSteps int `json:"totalSteps,omitempty"`

	// retryCount tracks how many retries have been attempted for the current step.
	// +optional
	RetryCount int `json:"retryCount,omitempty"`

	// agentRuns references all AgentRun objects created for this task.
	// +optional
	AgentRuns []AgentRunReference `json:"agentRuns,omitempty"`

	// pullRequest contains info about the created PR, if any.
	// +optional
	PullRequest *PullRequestInfo `json:"pullRequest,omitempty"`

	// startedAt is when the task began processing.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt is when the task finished (success or failure).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// message provides a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`

	// conditions represent the current state of the CodingTask.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Step",type=string,JSONPath=`.status.currentStep`
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repository.url`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CodingTask is the Schema for the codingtasks API.
// It represents a complete agentic coding workflow from prompt to pull request.
type CodingTask struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec CodingTaskSpec `json:"spec"`

	// +optional
	Status CodingTaskStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// CodingTaskList contains a list of CodingTask.
type CodingTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []CodingTask `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CodingTask{}, &CodingTaskList{})
}
