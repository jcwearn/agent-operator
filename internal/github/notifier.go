package github

import (
	"context"
	"fmt"

	gogithub "github.com/google/go-github/v68/github"
)

// Notifier posts status updates to GitHub issues.
type Notifier struct {
	client *Client
}

// NewNotifier creates a new GitHub notifier.
func NewNotifier(client *Client) *Notifier {
	return &Notifier{client: client}
}

// NotifyPlanReady posts the generated plan as a comment.
func (n *Notifier) NotifyPlanReady(ctx context.Context, owner, repo string, issue int, plan string) error {
	body := fmt.Sprintf("## 📋 Implementation Plan\n\n%s\n\n---\n_Proceeding to implementation..._", plan)
	return n.postComment(ctx, owner, repo, issue, body)
}

// NotifyStepUpdate posts a status update for a workflow step.
func (n *Notifier) NotifyStepUpdate(ctx context.Context, owner, repo string, issue int, step, status, msg string) error {
	emoji := statusEmoji(status)
	body := fmt.Sprintf("%s **%s**: %s — %s", emoji, step, status, msg)
	return n.postComment(ctx, owner, repo, issue, body)
}

// NotifyComplete posts the completion message with PR link.
func (n *Notifier) NotifyComplete(ctx context.Context, owner, repo string, issue int, prURL string) error {
	body := fmt.Sprintf("## ✅ Task Complete\n\nPull request created: %s", prURL)
	return n.postComment(ctx, owner, repo, issue, body)
}

// NotifyFailed posts a failure message.
func (n *Notifier) NotifyFailed(ctx context.Context, owner, repo string, issue int, reason string) error {
	body := fmt.Sprintf("## ❌ Task Failed\n\n%s", reason)
	return n.postComment(ctx, owner, repo, issue, body)
}

func (n *Notifier) postComment(ctx context.Context, owner, repo string, issue int, body string) error {
	_, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	return err
}

func statusEmoji(status string) string {
	switch status {
	case "Running":
		return "🔄"
	case "Succeeded":
		return "✅"
	case "Failed":
		return "❌"
	default:
		return "ℹ️"
	}
}
