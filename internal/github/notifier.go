package github

import (
	"context"
	"fmt"
	"strings"

	gogithub "github.com/google/go-github/v68/github"
	"github.com/jcwearn/agent-operator/internal/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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
	body := fmt.Sprintf("## Implementation Plan\n\n%s\n\n---\n\n- [ ] Run tests before creating PR\n\n**To approve this plan**, react with :+1: on this comment.\n**To request changes**, reply to this issue with your feedback.", plan)
	comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	if err != nil {
		return 0, err
	}
	return comment.GetID(), nil
}

// NotifyRevisedPlan posts a revised plan as a comment with the changes summary visible
// and the full plan collapsed in a <details> block. Returns the comment ID.
func (n *Notifier) NotifyRevisedPlan(ctx context.Context, owner, repo string, issue int, changesSummary, fullPlan string) (int64, error) {
	body := fmt.Sprintf(`## Revised Implementation Plan

%s

<details>
<summary>Click to expand full revised plan</summary>

%s

</details>

---
- [ ] Run tests before creating PR

**To approve this plan**, react with :+1: on this comment.
**To request changes**, reply to this issue with your feedback.`, changesSummary, fullPlan)

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

// NotifyAwaitingMerge posts a comment indicating the PR is ready and awaiting merge.
// Returns the comment ID for change-request feedback detection.
func (n *Notifier) NotifyAwaitingMerge(ctx context.Context, owner, repo string, issue int, prURL string) (int64, error) {
	body := fmt.Sprintf("## Awaiting Merge\n\nPull request created: %s\n\nI'll close this issue automatically when the PR is merged.\n\n**To request changes**, reply to this issue with your feedback and I'll update the PR.", prURL)
	comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	if err != nil {
		return 0, err
	}
	return comment.GetID(), nil
}

// CloseIssue closes a GitHub issue.
func (n *Notifier) CloseIssue(ctx context.Context, owner, repo string, issue int) error {
	state := "closed"
	_, _, err := n.client.Issues.Edit(ctx, owner, repo, issue,
		&gogithub.IssueRequest{State: &state})
	return err
}

// CheckPRStatus checks whether a pull request has been merged or closed.
func (n *Notifier) CheckPRStatus(ctx context.Context, owner, repo string, prNumber int) (controller.PRStatus, error) {
	pr, _, err := n.client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return controller.PRStatus{}, fmt.Errorf("getting PR %d: %w", prNumber, err)
	}
	return controller.PRStatus{
		Merged: pr.GetMerged(),
		Closed: pr.GetState() == "closed",
	}, nil
}

// CheckForFeedback looks for non-bot comments posted after the given anchor comment ID.
// Returns the body of the first such comment, or empty string if none found.
func (n *Notifier) CheckForFeedback(ctx context.Context, owner, repo string, issue int, afterCommentID int64) (string, error) {
	comments, _, err := n.client.Issues.ListComments(ctx, owner, repo, issue,
		&gogithub.IssueListCommentsOptions{
			ListOptions: gogithub.ListOptions{PerPage: 100},
		})
	if err != nil {
		return "", fmt.Errorf("listing comments: %w", err)
	}

	for _, c := range comments {
		if c.GetID() > afterCommentID && c.GetUser() != nil && c.GetUser().GetType() != "Bot" {
			return c.GetBody(), nil
		}
	}
	return "", nil
}

// CheckForReviewFeedback looks for PR reviews with "changes_requested" state submitted
// after the anchor comment. It fetches the anchor comment's timestamp, then finds
// matching reviews and aggregates the review body with any inline comments.
func (n *Notifier) CheckForReviewFeedback(ctx context.Context, owner, repo string, prNumber int, anchorCommentID int64) (string, error) {
	// Fetch the anchor comment to get its creation time.
	anchor, _, err := n.client.Issues.GetComment(ctx, owner, repo, anchorCommentID)
	if err != nil {
		return "", fmt.Errorf("fetching anchor comment: %w", err)
	}
	anchorTime := anchor.GetCreatedAt().Time

	// List reviews on the PR.
	reviews, _, err := n.client.PullRequests.ListReviews(ctx, owner, repo, prNumber,
		&gogithub.ListOptions{PerPage: 100})
	if err != nil {
		return "", fmt.Errorf("listing PR reviews: %w", err)
	}

	// Find the first CHANGES_REQUESTED review submitted after the anchor.
	var review *gogithub.PullRequestReview
	for _, r := range reviews {
		if r.GetState() == "CHANGES_REQUESTED" && r.GetSubmittedAt().After(anchorTime) {
			review = r
			break
		}
	}
	if review == nil {
		return "", nil
	}

	// Build feedback from review body + inline comments.
	var b strings.Builder
	if body := review.GetBody(); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}

	// Fetch inline review comments.
	comments, _, err := n.client.PullRequests.ListReviewComments(ctx, owner, repo, prNumber, review.GetID(),
		&gogithub.ListOptions{PerPage: 100})
	if err != nil {
		// Non-fatal — return what we have from the review body.
		if b.Len() > 0 {
			return strings.TrimSpace(b.String()), nil
		}
		return "", fmt.Errorf("listing review comments: %w", err)
	}

	for _, c := range comments {
		fmt.Fprintf(&b, "**%s:%d** — %s\n", c.GetPath(), c.GetLine(), c.GetBody())
	}

	return strings.TrimSpace(b.String()), nil
}

// CheckApproval checks if the plan comment has been approved via a thumbs-up reaction,
// or if a human has left feedback as a reply. When approved, it also parses the
// plan comment body to determine whether the "Run tests" checkbox was checked
// and extracts any checked decision checkboxes.
func (n *Notifier) CheckApproval(ctx context.Context, owner, repo string, issue int, commentID int64) (controller.ApprovalResult, error) {
	log := logf.FromContext(ctx)

	// Check reactions on the plan comment.
	reactions, _, reactErr := n.client.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID,
		&gogithub.ListOptions{PerPage: 100})
	if reactErr != nil {
		// Log but don't return — fall through to comment feedback check.
		log.Error(reactErr, "failed to list reactions, falling back to comment check")
	} else {
		for _, r := range reactions {
			if r.GetContent() == "+1" {
				// Fetch the plan comment to check the test checkbox and decision states.
				comment, _, err := n.client.Issues.GetComment(ctx, owner, repo, commentID)
				if err != nil {
					return controller.ApprovalResult{Approved: true}, nil
				}
				body := comment.GetBody()
				return controller.ApprovalResult{
					Approved:  true,
					RunTests:  strings.Contains(body, "- [x] Run tests before creating PR"),
					Decisions: extractCheckedDecisions(body),
				}, nil
			}
		}
	}

	// Check for human comments posted after the plan comment.
	comments, _, err := n.client.Issues.ListComments(ctx, owner, repo, issue,
		&gogithub.IssueListCommentsOptions{
			ListOptions: gogithub.ListOptions{PerPage: 100},
		})
	if err != nil {
		return controller.ApprovalResult{}, fmt.Errorf("listing comments: %w", err)
	}

	for _, c := range comments {
		// Only consider comments posted after the plan comment and not from bots.
		if c.GetID() > commentID && c.GetUser() != nil && c.GetUser().GetType() != "Bot" {
			// Also extract any decisions checked in the plan comment before feedback.
			planComment, _, planErr := n.client.Issues.GetComment(ctx, owner, repo, commentID)
			decisions := ""
			if planErr == nil {
				decisions = extractCheckedDecisions(planComment.GetBody())
			}
			return controller.ApprovalResult{
				Feedback:  c.GetBody(),
				Decisions: decisions,
			}, nil
		}
	}

	return controller.ApprovalResult{}, nil
}

// extractCheckedDecisions finds all checked checkbox lines (- [x] ...) in a comment body,
// excluding the "Run tests before creating PR" checkbox which is handled separately.
func extractCheckedDecisions(body string) string {
	var decisions []string
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [x] ") && !strings.Contains(trimmed, "Run tests before creating PR") {
			decisions = append(decisions, trimmed)
		}
	}
	return strings.Join(decisions, "\n")
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
