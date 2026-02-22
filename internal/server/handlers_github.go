package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v83/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
	case *gogithub.IssueCommentEvent:
		if err := s.handleIssueCommentEvent(r.Context(), e); err != nil {
			s.log.Error(err, "failed to handle issue comment event")
			respondError(w, http.StatusInternalServerError, "failed to process event")
			return
		}
	case *gogithub.PullRequestEvent:
		if err := s.handlePullRequestEvent(r.Context(), e); err != nil {
			s.log.Error(err, "failed to handle pull request event")
			respondError(w, http.StatusInternalServerError, "failed to process event")
			return
		}
	case *gogithub.PullRequestReviewEvent:
		if err := s.handlePullRequestReviewEvent(r.Context(), e); err != nil {
			s.log.Error(err, "failed to handle pull request review event")
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
			GitCredentialsRef: agentsv1alpha1.SecretReference{
				Name: s.gitCredentialsSecretName,
				Key:  s.gitCredentialsSecretKey,
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

// handleIssueCommentEvent triggers immediate reconciliation for CodingTasks in AwaitingApproval
// or AwaitingMerge when a comment is posted on the corresponding GitHub issue or PR.
func (s *APIServer) handleIssueCommentEvent(ctx context.Context, event *gogithub.IssueCommentEvent) error {
	if event.GetAction() != "created" {
		return nil
	}

	// Skip bot comments.
	if event.GetComment().GetUser().GetType() == "Bot" {
		return nil
	}

	repo := event.GetRepo()
	owner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	issueNumber := event.GetIssue().GetNumber()

	// Try direct issue-number lookup first.
	taskName := fmt.Sprintf("gh-%s-%s-%d", owner, repoName, issueNumber)
	if len(taskName) > 63 {
		taskName = taskName[:63]
	}
	taskName = strings.TrimRight(taskName, "-.")

	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Namespace: s.taskNamespace, Name: taskName}
	err := s.client.Get(ctx, key, &task)
	if err != nil {
		// Direct lookup failed — try PR-number fallback.
		// GitHub sends PR comments as IssueCommentEvent with issue number = PR number.
		found, foundTask := s.findTaskByPRNumber(ctx, owner, repoName, issueNumber)
		if !found {
			return nil
		}
		task = *foundTask
	}

	if task.Status.Phase != agentsv1alpha1.TaskPhaseAwaitingApproval &&
		task.Status.Phase != agentsv1alpha1.TaskPhaseAwaitingMerge {
		return nil
	}

	// Annotate the task to trigger immediate reconciliation.
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations["agents.wearn.dev/last-webhook-event"] = time.Now().UTC().Format(time.RFC3339)
	if err := s.client.Update(ctx, &task); err != nil {
		return fmt.Errorf("annotating CodingTask for reconciliation: %w", err)
	}

	s.log.Info("triggered reconciliation for issue comment",
		"task", task.Name, "owner", owner, "repo", repoName, "issue", issueNumber)
	return nil
}

// handlePullRequestEvent handles PR closed/merged events to trigger immediate reconciliation.
func (s *APIServer) handlePullRequestEvent(ctx context.Context, event *gogithub.PullRequestEvent) error {
	if event.GetAction() != "closed" {
		return nil
	}

	repo := event.GetRepo()
	owner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	prNumber := event.GetPullRequest().GetNumber()

	found, task := s.findTaskByPRNumber(ctx, owner, repoName, prNumber)
	if !found {
		return nil
	}

	if task.Status.Phase != agentsv1alpha1.TaskPhaseAwaitingMerge {
		return nil
	}

	// Annotate the task to trigger immediate reconciliation.
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations["agents.wearn.dev/last-webhook-event"] = time.Now().UTC().Format(time.RFC3339)
	if err := s.client.Update(ctx, task); err != nil {
		return fmt.Errorf("annotating CodingTask for PR event reconciliation: %w", err)
	}

	s.log.Info("triggered reconciliation for PR event",
		"task", task.Name, "owner", owner, "repo", repoName, "pr", prNumber,
		"merged", event.GetPullRequest().GetMerged())
	return nil
}

// handlePullRequestReviewEvent triggers immediate reconciliation when a PR review is submitted.
func (s *APIServer) handlePullRequestReviewEvent(ctx context.Context, event *gogithub.PullRequestReviewEvent) error {
	if event.GetAction() != "submitted" {
		return nil
	}

	// Only act on "changes_requested" reviews.
	if event.GetReview().GetState() != "changes_requested" {
		return nil
	}

	repo := event.GetRepo()
	owner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()
	prNumber := event.GetPullRequest().GetNumber()

	found, task := s.findTaskByPRNumber(ctx, owner, repoName, prNumber)
	if !found {
		return nil
	}

	if task.Status.Phase != agentsv1alpha1.TaskPhaseAwaitingMerge {
		return nil
	}

	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations["agents.wearn.dev/last-webhook-event"] = time.Now().UTC().Format(time.RFC3339)
	if err := s.client.Update(ctx, task); err != nil {
		return fmt.Errorf("annotating CodingTask for PR review reconciliation: %w", err)
	}

	s.log.Info("triggered reconciliation for PR review",
		"task", task.Name, "owner", owner, "repo", repoName, "pr", prNumber)
	return nil
}

// findTaskByPRNumber scans CodingTasks to find one matching the given PR number.
func (s *APIServer) findTaskByPRNumber(ctx context.Context, owner, repo string, prNumber int) (bool, *agentsv1alpha1.CodingTask) {
	var taskList agentsv1alpha1.CodingTaskList
	if err := s.client.List(ctx, &taskList, client.InNamespace(s.taskNamespace)); err != nil {
		s.log.Error(err, "failed to list tasks for PR-number lookup")
		return false, nil
	}

	for i := range taskList.Items {
		t := &taskList.Items[i]
		if t.Status.PullRequest != nil && t.Status.PullRequest.Number == prNumber {
			// Verify owner/repo match via source or tracking issue.
			if t.Spec.Source.GitHub != nil && t.Spec.Source.GitHub.Owner == owner && t.Spec.Source.GitHub.Repo == repo {
				return true, t
			}
			if t.Status.TrackingIssue != nil && t.Status.TrackingIssue.Owner == owner && t.Status.TrackingIssue.Repo == repo {
				return true, t
			}
		}
	}
	return false, nil
}
