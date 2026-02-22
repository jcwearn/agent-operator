package github

import (
	"context"
	"fmt"
	"strings"

	gogithub "github.com/google/go-github/v83/github"
	"github.com/jcwearn/agent-operator/internal/controller"
	"github.com/jcwearn/agent-operator/internal/provider"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const defaultProviderName = "claude"

// Notifier posts status updates to GitHub issues.
type Notifier struct {
	client           *Client
	providerRegistry *provider.Registry
}

// NewNotifier creates a new GitHub notifier.
func NewNotifier(client *Client, opts ...NotifierOption) *Notifier {
	n := &Notifier{client: client}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// NotifierOption configures the Notifier.
type NotifierOption func(*Notifier)

// WithProviderRegistry sets the provider registry for dynamic model selection.
func WithProviderRegistry(r *provider.Registry) NotifierOption {
	return func(n *Notifier) {
		n.providerRegistry = r
	}
}

// NotifyPlanReady posts the generated plan as a comment and returns the comment ID.
// If the plan is too long for a single GitHub comment, it splits across multiple
// comments with part headers. The returned comment ID is always the last comment
// (the one with the approval footer), so CheckApproval works correctly.
func (n *Notifier) NotifyPlanReady(ctx context.Context, owner, repo string, issue int, plan string) (int64, error) {
	footer := "\n\n---\n\n- [ ] Run tests before creating PR\n\n**To approve this plan**, react with :+1: on this comment.\n**To request changes**, reply to this issue with your feedback."

	// Reserve space for the header and footer in the size budget.
	// Header "## Implementation Plan (Part X of Y)\n\n" is ~45 chars max.
	chunks := splitComment(plan, maxCommentLen-len(footer)-50)

	if len(chunks) == 1 {
		body := fmt.Sprintf("## Implementation Plan\n\n%s%s", chunks[0], footer)
		comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
			&gogithub.IssueComment{Body: gogithub.Ptr(body)})
		if err != nil {
			return 0, err
		}
		return comment.GetID(), nil
	}

	// Multiple chunks — post each as a separate comment.
	var lastID int64
	for i, chunk := range chunks {
		var body string
		if i < len(chunks)-1 {
			body = fmt.Sprintf("## Implementation Plan (Part %d of %d)\n\n%s", i+1, len(chunks), chunk)
		} else {
			body = fmt.Sprintf("## Implementation Plan (Part %d of %d)\n\n%s%s", i+1, len(chunks), chunk, footer)
		}
		comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
			&gogithub.IssueComment{Body: gogithub.Ptr(body)})
		if err != nil {
			return 0, fmt.Errorf("posting plan part %d of %d: %w", i+1, len(chunks), err)
		}
		lastID = comment.GetID()
	}
	return lastID, nil
}

// NotifyRevisedPlan posts a revised plan as a comment with the changes summary visible
// and the full plan collapsed in a <details> block. Returns the comment ID.
// If the combined content exceeds GitHub's comment limit, it splits across multiple comments.
func (n *Notifier) NotifyRevisedPlan(ctx context.Context, owner, repo string, issue int, changesSummary, fullPlan string) (int64, error) {
	footer := "\n\n---\n- [ ] Run tests before creating PR\n\n**To approve this plan**, react with :+1: on this comment.\n**To request changes**, reply to this issue with your feedback."

	planContent := fmt.Sprintf("%s\n\n<details>\n<summary>Click to expand full revised plan</summary>\n\n%s\n\n</details>", changesSummary, fullPlan)

	chunks := splitComment(planContent, maxCommentLen-len(footer)-55)

	if len(chunks) == 1 {
		body := fmt.Sprintf("## Revised Implementation Plan\n\n%s%s", chunks[0], footer)
		comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
			&gogithub.IssueComment{Body: gogithub.Ptr(body)})
		if err != nil {
			return 0, err
		}
		return comment.GetID(), nil
	}

	var lastID int64
	for i, chunk := range chunks {
		var body string
		if i < len(chunks)-1 {
			body = fmt.Sprintf("## Revised Implementation Plan (Part %d of %d)\n\n%s", i+1, len(chunks), chunk)
		} else {
			body = fmt.Sprintf("## Revised Implementation Plan (Part %d of %d)\n\n%s%s", i+1, len(chunks), chunk, footer)
		}
		comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
			&gogithub.IssueComment{Body: gogithub.Ptr(body)})
		if err != nil {
			return 0, fmt.Errorf("posting revised plan part %d of %d: %w", i+1, len(chunks), err)
		}
		lastID = comment.GetID()
	}
	return lastID, nil
}

// NotifyProviderSelection posts a provider selection comment with checkboxes for each provider.
// Returns the comment ID for polling reactions.
func (n *Notifier) NotifyProviderSelection(ctx context.Context, owner, repo string, issue int) (int64, error) {
	body := n.buildProviderSelectionBody()

	comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	if err != nil {
		return 0, err
	}
	return comment.GetID(), nil
}

// buildProviderSelectionBody generates the provider selection comment body from the provider registry.
// Claude is checked by default.
func (n *Notifier) buildProviderSelectionBody() string {
	var b strings.Builder
	b.WriteString("## Agent Selection\n\nChoose the agent framework for this task, then react with :+1: to confirm.\n\n")

	if n.providerRegistry != nil {
		for _, p := range n.providerRegistry.All() {
			check := " "
			if p.Name() == defaultProviderName {
				check = "x"
			}
			b.WriteString(fmt.Sprintf("- [%s] %s — %s\n", check, p.DisplayName(), p.ProviderDescription()))
		}
	} else {
		b.WriteString("- [x] Claude Code — Claude models (Sonnet 4.5, Opus 4, Haiku 4.5) via Anthropic API\n")
	}

	b.WriteString("\n---\n**To confirm**, react with :+1: on this comment.")
	return b.String()
}

// CheckProviderSelection checks if the provider selection comment has been confirmed via :+1: reaction.
// If confirmed, it fetches the comment body and parses the checked provider.
func (n *Notifier) CheckProviderSelection(ctx context.Context, owner, repo string, issue int, commentID int64) (controller.ProviderSelectionResult, error) {
	reactions, _, err := n.client.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID,
		&gogithub.ListReactionOptions{ListOptions: gogithub.ListOptions{PerPage: 100}})
	if err != nil {
		return controller.ProviderSelectionResult{}, fmt.Errorf("listing reactions: %w", err)
	}

	hasThumbsUp := false
	for _, r := range reactions {
		if r.GetContent() == "+1" {
			hasThumbsUp = true
			break
		}
	}

	if !hasThumbsUp {
		return controller.ProviderSelectionResult{}, nil
	}

	comment, _, err := n.client.Issues.GetComment(ctx, owner, repo, commentID)
	if err != nil {
		return controller.ProviderSelectionResult{Confirmed: true, Provider: defaultProviderName}, nil
	}

	providerID := n.parseProviderSelection(comment.GetBody())
	return controller.ProviderSelectionResult{Confirmed: true, Provider: providerID}, nil
}

// parseProviderSelection finds the checked provider in a provider selection comment body.
// Returns the provider ID (e.g., "claude", "ollama"). Defaults to "claude" if nothing is checked.
func (n *Notifier) parseProviderSelection(body string) string {
	displayNameMap := n.buildProviderDisplayNameMap()

	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [x] ") {
			rest := trimmed[len("- [x] "):]
			if idx := strings.Index(rest, " — "); idx >= 0 {
				rest = rest[:idx]
			}
			if id, ok := displayNameMap[rest]; ok {
				return id
			}
		}
	}
	return defaultProviderName
}

// buildProviderDisplayNameMap maps provider display names to provider IDs from the registry.
func (n *Notifier) buildProviderDisplayNameMap() map[string]string {
	m := make(map[string]string)
	if n.providerRegistry != nil {
		for _, p := range n.providerRegistry.All() {
			m[p.DisplayName()] = p.Name()
		}
	}
	// Always include fallback.
	if _, ok := m["Claude Code"]; !ok {
		m["Claude Code"] = defaultProviderName
	}
	return m
}

// NotifyModelSelection posts a model selection comment with checkboxes for each workflow step.
// If providerName is non-empty, only models from that provider are shown.
// Returns the comment ID for polling reactions.
func (n *Notifier) NotifyModelSelection(ctx context.Context, owner, repo string, issue int, providerName string) (int64, error) {
	body := n.buildModelSelectionBody(providerName)

	comment, _, err := n.client.Issues.CreateComment(ctx, owner, repo, issue,
		&gogithub.IssueComment{Body: gogithub.Ptr(body)})
	if err != nil {
		return 0, err
	}
	return comment.GetID(), nil
}

// buildModelSelectionBody generates the model selection comment body from the provider registry.
// If providerName is non-empty, only that provider's models are shown.
func (n *Notifier) buildModelSelectionBody(providerName string) string {
	var b strings.Builder
	b.WriteString("## Model Selection\n\nSelect the model for each workflow step, then react with :+1: to confirm.\n")

	steps := []struct {
		title        string
		defaultModel string
	}{
		{"Plan", ""},
		{"Implement", ""},
		{"Test", ""},
		{"Pull Request", ""},
	}

	// Collect models, optionally filtered by provider.
	type providerModels struct {
		name   string
		models []provider.ModelInfo
	}
	var providers []providerModels
	if n.providerRegistry != nil {
		if providerName != "" {
			// Only show models from the specified provider.
			if p, err := n.providerRegistry.Get(providerName); err == nil {
				providers = append(providers, providerModels{name: p.Name(), models: p.AvailableModels()})
			}
		} else {
			for name, p := range n.providerRegistry.All() {
				providers = append(providers, providerModels{name: name, models: p.AvailableModels()})
			}
		}
	}
	if len(providers) == 0 {
		// Fallback: hardcoded Claude models.
		providers = append(providers, providerModels{
			name: defaultProviderName,
			models: []provider.ModelInfo{
				{ID: "sonnet", DisplayName: "Sonnet 4.5", Description: "balanced speed and capability"},
				{ID: "opus", DisplayName: "Opus 4", Description: "most capable, slower"},
				{ID: "haiku", DisplayName: "Haiku 4.5", Description: "fastest, lower cost"},
			},
		})
	}

	// Determine defaults from the target provider.
	targetProvider := providerName
	if targetProvider == "" {
		targetProvider = defaultProviderName
	}
	if n.providerRegistry != nil {
		if p, err := n.providerRegistry.Get(targetProvider); err == nil {
			steps[0].defaultModel = p.DefaultModelForStep("plan")
			steps[1].defaultModel = p.DefaultModelForStep("implement")
			steps[2].defaultModel = p.DefaultModelForStep("test")
			steps[3].defaultModel = p.DefaultModelForStep("pull-request")
		}
	}
	if steps[0].defaultModel == "" {
		const fallbackDefault = "sonnet"
		steps[0].defaultModel = fallbackDefault
		steps[1].defaultModel = fallbackDefault
		steps[2].defaultModel = fallbackDefault
		steps[3].defaultModel = "haiku"
	}

	for _, step := range steps {
		b.WriteString(fmt.Sprintf("\n### %s\n", step.title))
		for _, pm := range providers {
			for _, m := range pm.models {
				check := " "
				if m.ID == step.defaultModel {
					check = "x"
				}
				b.WriteString(fmt.Sprintf("- [%s] %s — %s\n", check, m.DisplayName, m.Description))
			}
		}
	}

	b.WriteString("\n---\n**To confirm**, react with :+1: on this comment.")
	return b.String()
}

// CheckModelSelection checks if the model selection comment has been confirmed via :+1: reaction.
// If confirmed, it fetches the comment body and parses the checked model selections.
func (n *Notifier) CheckModelSelection(ctx context.Context, owner, repo string, issue int, commentID int64) (controller.ModelSelectionResult, error) {
	reactions, _, err := n.client.Reactions.ListIssueCommentReactions(ctx, owner, repo, commentID,
		&gogithub.ListReactionOptions{ListOptions: gogithub.ListOptions{PerPage: 100}})
	if err != nil {
		return controller.ModelSelectionResult{}, fmt.Errorf("listing reactions: %w", err)
	}

	hasThumbsUp := false
	for _, r := range reactions {
		if r.GetContent() == "+1" {
			hasThumbsUp = true
			break
		}
	}

	if !hasThumbsUp {
		return controller.ModelSelectionResult{}, nil
	}

	// Fetch the comment body to parse checked models.
	comment, _, err := n.client.Issues.GetComment(ctx, owner, repo, commentID)
	if err != nil {
		// Confirmed but can't read body — return defaults.
		return controller.ModelSelectionResult{
			Confirmed: true,
			Plan:      "sonnet",
			Implement: "sonnet",
			Test:      "sonnet",
			PR:        "haiku",
		}, nil
	}

	result := n.parseModelSelections(comment.GetBody())
	result.Confirmed = true
	return result, nil
}

// buildDisplayNameMap dynamically builds a display-name-to-ID map from the provider registry.
func (n *Notifier) buildDisplayNameMap() map[string]string {
	m := make(map[string]string)
	if n.providerRegistry != nil {
		for _, p := range n.providerRegistry.All() {
			for _, model := range p.AvailableModels() {
				m[model.DisplayName] = model.ID
			}
		}
	}
	// Always include hardcoded Claude models as fallback.
	if _, ok := m["Sonnet 4.5"]; !ok {
		m["Sonnet 4.5"] = "sonnet"
	}
	if _, ok := m["Opus 4"]; !ok {
		m["Opus 4"] = "opus"
	}
	if _, ok := m["Haiku 4.5"]; !ok {
		m["Haiku 4.5"] = "haiku"
	}
	return m
}

// parseModelSelections parses the checked model selections from a model selection comment body.
// It splits by "### " headers and finds the first "- [x]" line in each section.
func (n *Notifier) parseModelSelections(body string) controller.ModelSelectionResult {
	displayNameMap := n.buildDisplayNameMap()

	result := controller.ModelSelectionResult{
		Plan:      "sonnet",
		Implement: "sonnet",
		Test:      "sonnet",
		PR:        "haiku",
	}

	// Split into sections by "### " headers.
	for section := range strings.SplitSeq(body, "### ") {
		if section == "" {
			continue
		}

		// Get the section title (first line).
		lines := strings.SplitN(section, "\n", 2)
		title := strings.TrimSpace(lines[0])
		if len(lines) < 2 {
			continue
		}

		// Find the first checked checkbox.
		model := ""
		for line := range strings.SplitSeq(lines[1], "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- [x] ") {
				// Extract the model name (everything before " — ").
				rest := trimmed[len("- [x] "):]
				if idx := strings.Index(rest, " — "); idx >= 0 {
					rest = rest[:idx]
				}
				if alias, ok := displayNameMap[rest]; ok {
					model = alias
					break
				}
			}
		}

		if model == "" {
			continue
		}

		switch title {
		case "Plan":
			result.Plan = model
		case "Implement":
			result.Implement = model
		case "Test":
			result.Test = model
		case "Pull Request":
			result.PR = model
		}
	}

	return result
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
		&gogithub.ListReactionOptions{ListOptions: gogithub.ListOptions{PerPage: 100}})
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

// maxCommentLen is the maximum length for a single GitHub comment body.
// GitHub's hard limit is 65,536 characters; we use 60,000 to leave headroom.
const maxCommentLen = 60000

// splitComment splits a long comment body into chunks that each fit within maxLen.
// It tries to split at natural markdown boundaries: section headers ("\n## "),
// horizontal rules ("\n---\n"), or paragraph breaks ("\n\n"). Falls back to
// splitting at the last newline before maxLen if no better boundary is found.
func splitComment(body string, maxLen int) []string {
	if len(body) <= maxLen {
		return []string{body}
	}

	var chunks []string
	remaining := body

	for len(remaining) > maxLen {
		chunk := remaining[:maxLen]

		// Try split points in order of preference.
		splitIdx := -1
		for _, sep := range []string{"\n## ", "\n---\n", "\n\n"} {
			if idx := strings.LastIndex(chunk, sep); idx > 0 {
				splitIdx = idx
				break
			}
		}
		// Fallback: split at last newline.
		if splitIdx <= 0 {
			if idx := strings.LastIndex(chunk, "\n"); idx > 0 {
				splitIdx = idx
			} else {
				// No newline at all — hard split.
				splitIdx = maxLen
			}
		}

		chunks = append(chunks, strings.TrimRight(remaining[:splitIdx], "\n"))
		remaining = strings.TrimLeft(remaining[splitIdx:], "\n")
	}

	if len(remaining) > 0 {
		chunks = append(chunks, remaining)
	}

	return chunks
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
