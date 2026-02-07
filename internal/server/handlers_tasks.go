package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

// CreateTaskRequest is the JSON body for creating a task via the API.
type CreateTaskRequest struct {
	Repository string            `json:"repository"`
	Branch     string            `json:"branch,omitempty"`
	Prompt     string            `json:"prompt"`
	Source     *CreateTaskSource `json:"source,omitempty"`
}

// CreateTaskSource defines the source of a task in the create request.
type CreateTaskSource struct {
	Type string          `json:"type"`
	Chat *ChatSourceBody `json:"chat,omitempty"`
}

// ChatSourceBody is the chat source in a create request.
type ChatSourceBody struct {
	SessionID string `json:"sessionId"`
	UserID    string `json:"userId"`
}

func (s *APIServer) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Repository == "" {
		respondError(w, http.StatusBadRequest, "repository is required")
		return
	}
	if req.Prompt == "" {
		respondError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	branch := req.Branch
	if branch == "" {
		branch = "main"
	}

	// Build the task source.
	source := agentsv1alpha1.TaskSource{
		Type: agentsv1alpha1.TaskSourceChat,
		Chat: &agentsv1alpha1.ChatSource{
			SessionID: "api",
			UserID:    "api",
		},
	}
	if req.Source != nil {
		if req.Source.Type == "chat" && req.Source.Chat != nil {
			source.Chat = &agentsv1alpha1.ChatSource{
				SessionID: req.Source.Chat.SessionID,
				UserID:    req.Source.Chat.UserID,
			}
		}
	}

	// Generate a unique task name.
	taskName := fmt.Sprintf("task-%d", metav1.Now().UnixMilli())

	task := &agentsv1alpha1.CodingTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: s.taskNamespace,
		},
		Spec: agentsv1alpha1.CodingTaskSpec{
			Source: source,
			Repository: agentsv1alpha1.RepositorySpec{
				URL:    req.Repository,
				Branch: branch,
			},
			Prompt: req.Prompt,
			AnthropicAPIKeyRef: agentsv1alpha1.SecretReference{
				Name: s.anthropicSecretName,
				Key:  s.anthropicSecretKey,
			},
		},
	}

	if err := s.client.Create(r.Context(), task); err != nil {
		s.log.Error(err, "failed to create CodingTask")
		respondError(w, http.StatusInternalServerError, "failed to create task")
		return
	}

	respondJSON(w, http.StatusCreated, map[string]any{
		"name":      task.Name,
		"namespace": task.Namespace,
		"phase":     "Pending",
	})
}

func (s *APIServer) handleListTasks(w http.ResponseWriter, r *http.Request) {
	var taskList agentsv1alpha1.CodingTaskList
	listOpts := []client.ListOption{
		client.InNamespace(s.taskNamespace),
	}

	if err := s.client.List(r.Context(), &taskList, listOpts...); err != nil {
		s.log.Error(err, "failed to list CodingTasks")
		respondError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}

	// Apply optional filters.
	sourceFilter := r.URL.Query().Get("source")
	phaseFilter := r.URL.Query().Get("phase")

	var results []taskSummary
	for _, t := range taskList.Items {
		if sourceFilter != "" && string(t.Spec.Source.Type) != sourceFilter {
			continue
		}
		if phaseFilter != "" && string(t.Status.Phase) != phaseFilter {
			continue
		}
		results = append(results, toTaskSummary(&t))
	}

	respondJSON(w, http.StatusOK, results)
}

func (s *APIServer) handleGetTask(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Name: name, Namespace: s.taskNamespace}
	if err := s.client.Get(r.Context(), key, &task); err != nil {
		respondError(w, http.StatusNotFound, "task not found")
		return
	}

	respondJSON(w, http.StatusOK, toTaskDetail(&task))
}

func (s *APIServer) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Name: name, Namespace: s.taskNamespace}
	if err := s.client.Get(r.Context(), key, &task); err != nil {
		respondError(w, http.StatusNotFound, "task not found")
		return
	}

	if err := s.client.Delete(r.Context(), &task); err != nil {
		s.log.Error(err, "failed to delete CodingTask", "name", name)
		respondError(w, http.StatusInternalServerError, "failed to delete task")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *APIServer) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	// Plan approval — placeholder for future use.
	respondError(w, http.StatusNotImplemented, "plan approval not yet implemented")
}

type taskSummary struct {
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	CurrentStep int    `json:"currentStep"`
	TotalSteps  int    `json:"totalSteps"`
	Source      string `json:"source"`
	Repository  string `json:"repository"`
	Message     string `json:"message"`
	CreatedAt   string `json:"createdAt"`
}

type taskDetail struct {
	taskSummary
	Prompt      string                              `json:"prompt"`
	Plan        string                              `json:"plan,omitempty"`
	AgentRuns   []agentsv1alpha1.AgentRunReference   `json:"agentRuns,omitempty"`
	PullRequest *agentsv1alpha1.PullRequestInfo      `json:"pullRequest,omitempty"`
	RetryCount  int                                  `json:"retryCount"`
	StartedAt   string                               `json:"startedAt,omitempty"`
	CompletedAt string                               `json:"completedAt,omitempty"`
}

func toTaskSummary(t *agentsv1alpha1.CodingTask) taskSummary {
	return taskSummary{
		Name:        t.Name,
		Phase:       string(t.Status.Phase),
		CurrentStep: t.Status.CurrentStep,
		TotalSteps:  t.Status.TotalSteps,
		Source:      string(t.Spec.Source.Type),
		Repository:  t.Spec.Repository.URL,
		Message:     t.Status.Message,
		CreatedAt:   t.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
	}
}

func toTaskDetail(t *agentsv1alpha1.CodingTask) taskDetail {
	d := taskDetail{
		taskSummary: toTaskSummary(t),
		Prompt:      t.Spec.Prompt,
		Plan:        t.Status.Plan,
		AgentRuns:   t.Status.AgentRuns,
		PullRequest: t.Status.PullRequest,
		RetryCount:  t.Status.RetryCount,
	}
	if t.Status.StartedAt != nil {
		d.StartedAt = t.Status.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if t.Status.CompletedAt != nil {
		d.CompletedAt = t.Status.CompletedAt.Format("2006-01-02T15:04:05Z")
	}
	return d
}
