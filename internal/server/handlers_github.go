package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	gogithub "github.com/google/go-github/v68/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

const aiTaskLabel = "ai-task"

func (s *APIServer) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.githubClient == nil {
		respondError(w, http.StatusServiceUnavailable, "GitHub integration not configured")
		return
	}

	// Validate webhook signature.
	payload, err := gogithub.ValidatePayload(r, s.githubWebhookSecret)
	if err != nil {
		s.log.Error(err, "invalid webhook signature")
		respondError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	event, err := gogithub.ParseWebHook(gogithub.WebHookType(r), payload)
	if err != nil {
		s.log.Error(err, "failed to parse webhook")
		respondError(w, http.StatusBadRequest, "failed to parse webhook")
		return
	}

	switch e := event.(type) {
	case *gogithub.IssuesEvent:
		if err := s.handleIssuesEvent(r.Context(), e); err != nil {
			s.log.Error(err, "failed to handle issues event")
			respondError(w, http.StatusInternalServerError, "failed to process event")
			return
		}
	default:
		s.log.Info("ignoring unhandled webhook event type", "type", gogithub.WebHookType(r))
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *APIServer) handleIssuesEvent(ctx context.Context, event *gogithub.IssuesEvent) error {
	if event.GetAction() != "labeled" {
		return nil
	}

	label := event.GetLabel()
	if label == nil || label.GetName() != aiTaskLabel {
		return nil
	}

	issue := event.GetIssue()
	repo := event.GetRepo()
	owner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	issueNumber := issue.GetNumber()

	s.log.Info("processing ai-task label",
		"owner", owner, "repo", repoName, "issue", issueNumber)

	// Build CodingTask name (truncated to 63 chars for K8s).
	taskName := fmt.Sprintf("gh-%s-%s-%d", owner, repoName, issueNumber)
	if len(taskName) > 63 {
		taskName = taskName[:63]
	}
	// Ensure it ends with an alphanumeric character.
	taskName = strings.TrimRight(taskName, "-.")

	prompt := issue.GetTitle()
	if body := issue.GetBody(); body != "" {
		prompt = prompt + "\n\n" + body
	}

	task := &agentsv1alpha1.CodingTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: s.taskNamespace,
			Labels: map[string]string{
				"agents.wearn.dev/source": "github-issue",
				"agents.wearn.dev/owner":  owner,
				"agents.wearn.dev/repo":   repoName,
			},
		},
		Spec: agentsv1alpha1.CodingTaskSpec{
			Source: agentsv1alpha1.TaskSource{
				Type: agentsv1alpha1.TaskSourceGitHubIssue,
				GitHub: &agentsv1alpha1.GitHubSource{
					Owner:       owner,
					Repo:        repoName,
					IssueNumber: issueNumber,
				},
			},
			Repository: agentsv1alpha1.RepositorySpec{
				URL:    repo.GetCloneURL(),
				Branch: repo.GetDefaultBranch(),
			},
			Prompt: prompt,
			AnthropicAPIKeyRef: agentsv1alpha1.SecretReference{
				Name: s.anthropicSecretName,
				Key:  s.anthropicSecretKey,
			},
		},
	}

	if task.Spec.Repository.Branch == "" {
		task.Spec.Repository.Branch = "main"
	}

	if err := s.client.Create(ctx, task); err != nil {
		return fmt.Errorf("creating CodingTask: %w", err)
	}

	// Post acknowledgement comment on the issue.
	comment := fmt.Sprintf(
		"🤖 Agent task `%s` created. Starting work on this issue...\n\n"+
			"I'll post updates here as I progress through planning, implementation, testing, and PR creation.",
		taskName,
	)
	_, _, err := s.githubClient.Issues.CreateComment(ctx, owner, repoName, issueNumber,
		&gogithub.IssueComment{Body: gogithub.Ptr(comment)})
	if err != nil {
		s.log.Error(err, "failed to post acknowledgement comment", "issue", issueNumber)
		// Non-fatal: task was already created.
	}

	return nil
}
