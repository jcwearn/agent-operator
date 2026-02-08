package github

import (
	"context"
	"fmt"
	"time"

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

// NotifyPlanReady posts the generated plan as a comment and returns the comment ID.
func (n *Notifier) NotifyPlanReady(ctx context.Context, owner, repo string, issue int, plan string) (int64, error) {
	body := fmt.Sprintf("## Implementation Plan\n\n%s\n\n---\n\n**To approve this plan**, react with :+1: on this comment.\n**To request changes**, reply to this issue with your feedback.", plan)
	comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	if err != nil {
		return 0, err
	}
	return comment.GetID(), nil
}

// NotifyStepUpdate posts a status update for a workflow step.
func (n *Notifier) NotifyStepUpdate(ctx context.Context, owner, repo string, issue int, step, status, msg string) error {
	emoji := statusEmoji(status)
	body := fmt.Sprintf("%s **%s**: %s — %s", emoji, step, status, msg)
	return n.postComment(ctx, owner, repo, issue, body)
}

// NotifyComplete posts the completion message with PR link.
func (n *Notifier) NotifyComplete(ctx context.Context, owner, repo string, issue int, prURL string) error {
	body := fmt.Sprintf("## Task Complete\n\nPull request created: %s", prURL)
	return n.postComment(ctx, owner, repo, issue, body)
}

// NotifyFailed posts a failure message.
func (n *Notifier) NotifyFailed(ctx context.Context, owner, repo string, issue int, reason string) error {
	body := fmt.Sprintf("## Task Failed\n\n%s", reason)
	return n.postComment(ctx, owner, repo, issue, body)
}

// CheckApproval checks if the plan comment has been approved via a thumbs-up reaction,
// or if a human has left feedback as a reply.
func (n *Notifier) CheckApproval(ctx context.Context, owner, repo string, issue int, commentID int64) (approved bool, feedback string, err error) {
	// Check reactions on the plan comment.
	reactions, _, err := n.client.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID,
		&gogithub.ListOptions{PerPage: 100})
	if err != nil {
		return false, "", fmt.Errorf("listing reactions: %w", err)
	}
	for _, r := range reactions {
		if r.GetContent() == "+1" {
			return true, "", nil
		}
	}

	// Check for human comments posted after the plan comment.
	comments, _, err := n.client.Issues.ListComments(ctx, owner, repo, issue,
		&gogithub.IssueListCommentsOptions{
			Since:       &time.Time{},
			ListOptions: gogithub.ListOptions{PerPage: 100},
		})
	if err != nil {
		return false, "", fmt.Errorf("listing comments: %w", err)
	}

	for _, c := range comments {
		// Only consider comments posted after the plan comment and not from bots.
		if c.GetID() > commentID && c.GetUser() != nil && c.GetUser().GetType() != "Bot" {
			return false, c.GetBody(), nil
		}
	}

	return false, "", nil
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
