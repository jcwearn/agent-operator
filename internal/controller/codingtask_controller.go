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

package controller

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

// Notifier posts status updates to external systems (e.g., GitHub issues).
type Notifier interface {
	NotifyPlanReady(ctx context.Context, owner, repo string, issue int, plan string) (int64, error)
	NotifyRevisedPlan(ctx context.Context, owner, repo string, issue int, changesSummary, fullPlan string) (int64, error)
	NotifyStepUpdate(ctx context.Context, owner, repo string, issue int, step, status, msg string) error
	NotifyComplete(ctx context.Context, owner, repo string, issue int, prURL string) error
	NotifyFailed(ctx context.Context, owner, repo string, issue int, reason string) error
	NotifyAwaitingMerge(ctx context.Context, owner, repo string, issue int, prURL string) (int64, error)
	NotifyModelSelection(ctx context.Context, owner, repo string, issue int) (int64, error)
	CloseIssue(ctx context.Context, owner, repo string, issue int) error
}

// PRStatus contains the merge/close state of a pull request.
type PRStatus struct {
	Merged bool
	Closed bool
}

// PRStatusChecker checks the merge status of a pull request.
type PRStatusChecker interface {
	CheckPRStatus(ctx context.Context, owner, repo string, prNumber int) (PRStatus, error)
}

// ApprovalResult contains the parsed result of a plan approval check.
type ApprovalResult struct {
	Approved  bool
	RunTests  bool
	Decisions string // checked checkbox lines (excluding "Run tests")
	Feedback  string
}

// ApprovalChecker checks whether a plan has been approved via GitHub reactions or comments.
type ApprovalChecker interface {
	CheckApproval(ctx context.Context, owner, repo string, issue int, commentID int64) (ApprovalResult, error)
	CheckForFeedback(ctx context.Context, owner, repo string, issue int, afterCommentID int64) (string, error)
	CheckForReviewFeedback(ctx context.Context, owner, repo string, prNumber int, anchorCommentID int64) (string, error)
}

// ModelSelectionResult contains the parsed model choices from a model selection comment.
type ModelSelectionResult struct {
	Confirmed bool
	Plan      string
	Implement string
	Test      string
	PR        string
}

// ModelSelectionChecker checks whether model selection has been confirmed via GitHub reactions.
type ModelSelectionChecker interface {
	CheckModelSelection(ctx context.Context, owner, repo string, issue int, commentID int64) (ModelSelectionResult, error)
}

// Broadcaster publishes real-time task status events (e.g., to WebSocket clients).
type Broadcaster interface {
	Broadcast(event BroadcastEvent)
}

// BroadcastEvent represents a task status change.
type BroadcastEvent struct {
	Type    string `json:"type"`
	Task    string `json:"task"`
	Phase   string `json:"phase"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// CodingTaskReconciler reconciles a CodingTask object.
type CodingTaskReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	Notifier              Notifier              // optional, nil-safe
	Broadcaster           Broadcaster           // optional, nil-safe
	ApprovalChecker       ApprovalChecker       // optional; if nil, plans are auto-approved
	PRStatusChecker       PRStatusChecker       // optional; if nil, awaiting-merge polling is disabled
	ModelSelectionChecker ModelSelectionChecker // optional; if nil, model selection is skipped
	DefaultAgentImage     string                // default agent-runner image; used when CodingTask.Spec.AgentImage is empty
}

// +kubebuilder:rbac:groups=agents.wearn.dev,resources=codingtasks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.wearn.dev,resources=codingtasks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.wearn.dev,resources=codingtasks/finalizers,verbs=update
// +kubebuilder:rbac:groups=agents.wearn.dev,resources=agentruns,verbs=get;list;watch;create;update;patch;delete

func (r *CodingTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var task agentsv1alpha1.CodingTask
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Initialize status if this is a new task.
	if task.Status.Phase == "" {
		task.Status.Phase = agentsv1alpha1.TaskPhasePending
		task.Status.TotalSteps = 5
		task.Status.CurrentStep = 0
		if err := r.Status().Update(ctx, &task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Terminal states — nothing to do.
	if task.Status.Phase == agentsv1alpha1.TaskPhaseComplete ||
		task.Status.Phase == agentsv1alpha1.TaskPhaseFailed {
		return ctrl.Result{}, nil
	}

	switch task.Status.Phase {
	case agentsv1alpha1.TaskPhasePending:
		return r.handlePending(ctx, &task)
	case agentsv1alpha1.TaskPhaseAwaitingModelSelection:
		return r.handleAwaitingModelSelection(ctx, &task)
	case agentsv1alpha1.TaskPhasePlanning:
		return r.handlePlanning(ctx, &task)
	case agentsv1alpha1.TaskPhaseAwaitingApproval:
		return r.handleAwaitingApproval(ctx, &task)
	case agentsv1alpha1.TaskPhaseImplementing:
		return r.handleImplementing(ctx, &task)
	case agentsv1alpha1.TaskPhaseTesting:
		return r.handleTesting(ctx, &task)
	case agentsv1alpha1.TaskPhasePullRequest:
		return r.handlePullRequest(ctx, &task)
	case agentsv1alpha1.TaskPhaseAwaitingMerge:
		return r.handleAwaitingMerge(ctx, &task)
	default:
		log.Info("unknown phase", "phase", task.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handlePending posts a model selection comment (for GitHub-sourced tasks) and
// transitions to AwaitingModelSelection, or skips directly to Planning for
// non-GitHub tasks or when no Notifier is configured.
func (r *CodingTaskReconciler) handlePending(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Skip model selection for non-GitHub tasks or when notifier is unavailable.
	if r.Notifier == nil || task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		log.Info("skipping model selection (non-GitHub source or no notifier), going straight to Planning")
		return r.transitionToPlanning(ctx, task)
	}

	gh := task.Spec.Source.GitHub
	commentID, err := r.Notifier.NotifyModelSelection(ctx, gh.Owner, gh.Repo, gh.IssueNumber)
	if err != nil {
		log.Error(err, "failed to post model selection comment, proceeding to Planning")
		return r.transitionToPlanning(ctx, task)
	}

	task.Status.Phase = agentsv1alpha1.TaskPhaseAwaitingModelSelection
	task.Status.ModelSelectionCommentID = &commentID
	task.Status.Message = "Awaiting model selection"
	r.broadcast(task)

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handleAwaitingModelSelection polls for a :+1: reaction on the model selection comment.
// Once confirmed, it parses the checked models, stores them in ModelOverrides, and transitions to Planning.
func (r *CodingTaskReconciler) handleAwaitingModelSelection(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If no comment ID or no checker, skip to Planning with defaults.
	if task.Status.ModelSelectionCommentID == nil || r.ModelSelectionChecker == nil {
		log.Info("no model selection comment or checker, proceeding to Planning")
		return r.transitionToPlanning(ctx, task)
	}

	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		return r.transitionToPlanning(ctx, task)
	}

	gh := task.Spec.Source.GitHub
	result, err := r.ModelSelectionChecker.CheckModelSelection(ctx, gh.Owner, gh.Repo, gh.IssueNumber, *task.Status.ModelSelectionCommentID)
	if err != nil {
		log.Error(err, "failed to check model selection")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if !result.Confirmed {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	log.Info("model selection confirmed", "plan", result.Plan, "implement", result.Implement, "test", result.Test, "pr", result.PR)
	task.Status.ModelOverrides = &agentsv1alpha1.ModelConfig{
		Plan:        result.Plan,
		Implement:   result.Implement,
		Test:        result.Test,
		PullRequest: result.PR,
	}

	return r.transitionToPlanning(ctx, task)
}

// transitionToPlanning creates a plan AgentRun and moves to the Planning phase.
func (r *CodingTaskReconciler) transitionToPlanning(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("transitioning task to Planning phase")

	// Guard: if a plan run is already active (status not yet visible via label query), skip creation.
	if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepPlan) {
		log.Info("plan run already active, skipping duplicate creation")
		task.Status.Phase = agentsv1alpha1.TaskPhasePlanning
		task.Status.Message = "Planning agent already running"
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	now := metav1.Now()
	task.Status.Phase = agentsv1alpha1.TaskPhasePlanning
	task.Status.CurrentStep = 1
	task.Status.StartedAt = &now
	task.Status.Message = "Creating planning agent"

	if err := r.notify(ctx, task, "planning-starting", func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Planning", "Running", "Starting planning agent")
	}); err != nil {
		return ctrl.Result{}, err
	}

	prompt := fmt.Sprintf(`You are a planning agent. Analyze the following task and the repository codebase, then produce a detailed implementation plan.

Task: %s

Instructions:
1. Clone and explore the repository structure
2. Identify the files that need to be created or modified
3. Break the work into clear, ordered steps
4. Consider edge cases and testing requirements
5. Output your plan as structured markdown
6. If there are technical or product decisions to make, present each as a
   multiple-choice group using GitHub checkboxes. Example:
   ### Decision: Database choice
   - [ ] PostgreSQL -- mature, ACID-compliant
   - [ ] SQLite -- simpler, no external dependency

Output ONLY the plan — no code implementation.`, task.Spec.Prompt)

	run, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPlan, prompt, "")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating plan AgentRun: %w", err)
	}

	task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
		Name:  run.Name,
		Step:  agentsv1alpha1.AgentRunStepPlan,
		Phase: agentsv1alpha1.AgentRunPhasePending,
	})

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handlePlanning checks if the planning AgentRun has completed and transitions to AwaitingApproval.
func (r *CodingTaskReconciler) handlePlanning(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPlan)
	if err != nil {
		return ctrl.Result{}, err
	}
	if run == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		task.Status.Phase = agentsv1alpha1.TaskPhaseAwaitingApproval
		task.Status.CurrentStep = 2
		task.Status.RetryCount = 0
		task.Status.Message = "Plan ready, awaiting approval"
		r.updateAgentRunRef(task, run)
		r.broadcast(task)

		if task.Status.PlanRevision > 0 {
			// Revised plan — parse the structured output and post a collapsed comment.
			changesSummary, fullPlan := parseRevisedPlanOutput(run.Status.Output)
			task.Status.Plan = fullPlan
			if err := r.notifyRevisedPlan(ctx, task, changesSummary, fullPlan); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			// First plan — post the full plan as-is.
			task.Status.Plan = run.Status.Output
			if err := r.notifyPlanReady(ctx, task, run.Status.Output); err != nil {
				return ctrl.Result{}, err
			}
		}

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case agentsv1alpha1.AgentRunPhaseFailed:
		return r.handleStepFailure(ctx, task, run, agentsv1alpha1.AgentRunStepPlan)

	default:
		r.updateAgentRunRef(task, run)
		task.Status.Message = fmt.Sprintf("Planning agent is %s", run.Status.Phase)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
}

// handleAwaitingApproval polls for plan approval (thumbs-up reaction or comment feedback).
func (r *CodingTaskReconciler) handleAwaitingApproval(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// If no ApprovalChecker is configured, auto-approve (tests skipped by default).
	if r.ApprovalChecker == nil {
		log.Info("no approval checker configured, auto-approving plan")
		task.Status.RunTests = false
		task.Status.TotalSteps = 4
		return r.transitionToImplementing(ctx, task)
	}

	// Need a plan comment ID to check approval.
	if task.Status.PlanCommentID == nil {
		// If we have a notifier and this is a GitHub issue, try to (re-)post the plan comment.
		// This handles the case where notifyPlanReady succeeded but the status update conflicted,
		// losing the PlanCommentID.
		if r.Notifier != nil && task.Spec.Source.Type == agentsv1alpha1.TaskSourceGitHubIssue && task.Spec.Source.GitHub != nil {
			log.Info("plan comment ID missing, attempting to post plan comment")
			if err := r.notifyPlanReady(ctx, task, task.Status.Plan); err != nil {
				return ctrl.Result{}, err
			}
			if task.Status.PlanCommentID != nil {
				// notifyPlanReady already persisted via Status().Update().
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		log.Info("no plan comment ID, auto-approving")
		task.Status.RunTests = false
		task.Status.TotalSteps = 4
		return r.transitionToImplementing(ctx, task)
	}

	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		task.Status.RunTests = false
		task.Status.TotalSteps = 4
		return r.transitionToImplementing(ctx, task)
	}

	gh := task.Spec.Source.GitHub
	result, err := r.ApprovalChecker.CheckApproval(ctx, gh.Owner, gh.Repo, gh.IssueNumber, *task.Status.PlanCommentID)
	if err != nil {
		log.Error(err, "failed to check approval")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if result.Approved {
		log.Info("plan approved via reaction", "runTests", result.RunTests)
		task.Status.RunTests = result.RunTests
		task.Status.Decisions = result.Decisions
		if result.RunTests {
			task.Status.TotalSteps = 5
		} else {
			task.Status.TotalSteps = 4
		}
		return r.transitionToImplementing(ctx, task)
	}

	if result.Feedback != "" {
		// Guard: if a plan run is already active (e.g., from a previous re-plan attempt), don't create another.
		if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepPlan) {
			log.Info("plan run already active, skipping duplicate re-plan creation")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Feedback received — re-plan with the feedback incorporated.
		log.Info("feedback received, re-planning", "feedback", result.Feedback)
		// Store any decisions checked in the plan comment before re-planning.
		task.Status.Decisions = result.Decisions
		task.Status.Phase = agentsv1alpha1.TaskPhasePlanning
		task.Status.CurrentStep = 1
		task.Status.RetryCount = 0
		task.Status.PlanCommentID = nil
		task.Status.PlanRevision++
		// Clear "plan-ready" from NotifiedPhases so the revised plan comment gets posted.
		task.Status.NotifiedPhases = slices.DeleteFunc(task.Status.NotifiedPhases, func(s string) bool {
			return s == "plan-ready"
		})
		task.Status.Message = "Re-planning with reviewer feedback"
		r.broadcast(task)

		decisionsSection := ""
		if task.Status.Decisions != "" {
			decisionsSection = fmt.Sprintf("\nPrevious Decisions (respect these unless feedback overrides them):\n%s\n", task.Status.Decisions)
		}

		prompt := fmt.Sprintf(`You are a planning agent. Your previous plan received feedback from a reviewer. Revise the plan based on their feedback.

Task: %s

Previous Plan:
%s
%sReviewer Feedback:
%s

Instructions:
1. Address the reviewer's feedback
2. Revise the plan accordingly
3. Keep the same structured markdown format
4. If there are technical or product decisions to make, present each as a
   multiple-choice group using GitHub checkboxes. Example:
   ### Decision: Database choice
   - [ ] PostgreSQL -- mature, ACID-compliant
   - [ ] SQLite -- simpler, no external dependency

Output your response in this exact format:

## What Changed
<summary of what changed from the previous plan>

---PLAN---

<full revised plan>`, task.Spec.Prompt, task.Status.Plan, decisionsSection, result.Feedback)

		run, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPlan, prompt, result.Feedback)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating re-plan AgentRun: %w", err)
		}
		task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
			Name:  run.Name,
			Step:  agentsv1alpha1.AgentRunStepPlan,
			Phase: agentsv1alpha1.AgentRunPhasePending,
		})

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Neither approved nor feedback — requeue and check again.
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// transitionToImplementing moves a task from AwaitingApproval to Implementing.
func (r *CodingTaskReconciler) transitionToImplementing(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Guard: if an implement run already exists, just ensure phase/step are correct and requeue.
	if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepImplement) {
		log.Info("implement run already active, skipping duplicate creation")
		task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
		task.Status.CurrentStep = 3
		task.Status.Message = "Implementation agent already running"
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
	task.Status.CurrentStep = 3
	task.Status.RetryCount = 0
	task.Status.Message = "Plan approved, starting implementation"
	r.broadcast(task)
	if err := r.notify(ctx, task, "implement-starting", func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Implementation", "Running", "Plan approved, starting implementation agent")
	}); err != nil {
		return ctrl.Result{}, err
	}

	decisionsSection := ""
	if task.Status.Decisions != "" {
		decisionsSection = fmt.Sprintf("\nReviewer Decisions:\n%s\n", task.Status.Decisions)
	}

	prompt := fmt.Sprintf(`You are an implementation agent. Implement the following plan in the repository.

Original Task: %s

Plan:
%s
%sInstructions:
1. Create a new branch: %s
2. Implement all changes described in the plan
3. Write tests that cover the changes
4. Commit your changes with a descriptive commit message
5. Do NOT open a pull request — that will be handled separately

Output a summary of what you implemented and any notes for the tester.`, task.Spec.Prompt, task.Status.Plan, decisionsSection, r.workBranch(task))

	newRun, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement, prompt, task.Status.Plan)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating implement AgentRun: %w", err)
	}
	task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
		Name:  newRun.Name,
		Step:  agentsv1alpha1.AgentRunStepImplement,
		Phase: agentsv1alpha1.AgentRunPhasePending,
	})

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleImplementing checks if the implementation AgentRun has completed.
func (r *CodingTaskReconciler) handleImplementing(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement)
	if err != nil {
		return ctrl.Result{}, err
	}
	if run == nil {
		// Guard: if an implement run is already tracked in status, the label query just hasn't caught up.
		if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepImplement) {
			log.Info("implement run active in status but not yet visible via label query, requeuing")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		// No implement run exists yet — create one (e.g., after API-based approval).
		return r.transitionToImplementing(ctx, task)
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		r.updateAgentRunRef(task, run)
		task.Status.RetryCount = 0

		if !task.Status.RunTests {
			// Skip testing — go straight to PullRequest.
			task.Status.Phase = agentsv1alpha1.TaskPhasePullRequest
			task.Status.CurrentStep = task.Status.TotalSteps
			task.Status.Message = "Implementation complete, creating pull request (tests skipped)"
			r.broadcast(task)
			if err := r.notify(ctx, task, "implement-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
				return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Implementation", "Succeeded", "Creating pull request (tests skipped)")
			}); err != nil {
				return ctrl.Result{}, err
			}
			return r.createPullRequestRun(ctx, task, run.Status.Output, "")
		}

		task.Status.Phase = agentsv1alpha1.TaskPhaseTesting
		task.Status.CurrentStep = task.Status.TotalSteps - 1
		task.Status.Message = "Implementation complete, running tests"
		r.broadcast(task)
		if err := r.notify(ctx, task, "implement-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Implementation", "Succeeded", "Starting tests")
		}); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.notify(ctx, task, "testing-starting", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Testing", "Running", "Starting test agent")
		}); err != nil {
			return ctrl.Result{}, err
		}

		// Guard: if a test run is already active, skip creation.
		if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepTest) {
			log.Info("test run already active, skipping duplicate creation")
			if err := r.Status().Update(ctx, task); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		prompt := fmt.Sprintf(`You are a testing agent. Run the test suite for the repository and verify the changes are correct.

Original Task: %s

Implementation Notes: %s

Instructions:
1. Check out the work branch: %s
2. Run the full test suite
3. If tests fail, output the failure details clearly
4. If tests pass, output "ALL TESTS PASSED"
5. Include a summary of test results

Output the test results.`, task.Spec.Prompt, run.Status.Output, r.workBranch(task))

		testRun, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepTest, prompt, run.Status.Output)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating test AgentRun: %w", err)
		}
		task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
			Name:  testRun.Name,
			Step:  agentsv1alpha1.AgentRunStepTest,
			Phase: agentsv1alpha1.AgentRunPhasePending,
		})

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case agentsv1alpha1.AgentRunPhaseFailed:
		return r.handleStepFailure(ctx, task, run, agentsv1alpha1.AgentRunStepImplement)

	default:
		r.updateAgentRunRef(task, run)
		task.Status.Message = fmt.Sprintf("Implementation agent is %s", run.Status.Phase)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
}

// handleTesting checks if the test AgentRun completed. On failure, retries implementation.
func (r *CodingTaskReconciler) handleTesting(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepTest)
	if err != nil {
		return ctrl.Result{}, err
	}
	if run == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		task.Status.Phase = agentsv1alpha1.TaskPhasePullRequest
		task.Status.CurrentStep = task.Status.TotalSteps
		task.Status.RetryCount = 0
		task.Status.Message = "Tests passed, creating pull request"
		r.updateAgentRunRef(task, run)
		r.broadcast(task)
		if err := r.notify(ctx, task, "test-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Testing", "Succeeded", "Creating pull request")
		}); err != nil {
			return ctrl.Result{}, err
		}

		return r.createPullRequestRun(ctx, task, "", run.Status.Output)

	case agentsv1alpha1.AgentRunPhaseFailed:
		// Test failure: retry the implement step with error context.
		if task.Status.RetryCount < task.Spec.MaxRetries {
			// Guard: if an implement run is already active from a previous retry, don't create another.
			if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepImplement) {
				log.Info("implement run already active, skipping duplicate retry creation")
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}

			task.Status.RetryCount++
			task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
			task.Status.CurrentStep = 3
			task.Status.Message = fmt.Sprintf("Tests failed (attempt %d/%d), retrying implementation", task.Status.RetryCount, task.Spec.MaxRetries)
			r.updateAgentRunRef(task, run)

			prompt := fmt.Sprintf(`You are an implementation agent. The previous implementation had test failures. Fix the issues.

Original Task: %s

Plan:
%s

Test Failure Output:
%s

Instructions:
1. Check out the work branch: %s
2. Analyze the test failures
3. Fix the code to make tests pass
4. Commit your fixes
5. Do NOT open a pull request

Output a summary of what you fixed.`, task.Spec.Prompt, task.Status.Plan, run.Status.Output, r.workBranch(task))

			newRun, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement, prompt, run.Status.Output)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("creating retry implement AgentRun: %w", err)
			}
			task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
				Name:  newRun.Name,
				Step:  agentsv1alpha1.AgentRunStepImplement,
				Phase: agentsv1alpha1.AgentRunPhasePending,
			})

			if err := r.Status().Update(ctx, task); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		return r.failTask(ctx, task, fmt.Sprintf("tests failed after %d retries: %s", task.Spec.MaxRetries, run.Status.Message))

	default:
		r.updateAgentRunRef(task, run)
		task.Status.Message = fmt.Sprintf("Test agent is %s", run.Status.Phase)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
}

// handlePullRequest checks if the PR AgentRun has completed.
func (r *CodingTaskReconciler) handlePullRequest(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPullRequest)
	if err != nil {
		return ctrl.Result{}, err
	}
	if run == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		task.Status.Phase = agentsv1alpha1.TaskPhaseAwaitingMerge
		task.Status.Message = "Pull request created, awaiting merge"
		r.updateAgentRunRef(task, run)

		// The output should contain the PR URL on the first line.
		prURL := ""
		if run.Status.Output != "" {
			prURL = run.Status.Output
			prNumber := parsePRNumber(prURL)
			task.Status.PullRequest = &agentsv1alpha1.PullRequestInfo{
				URL:    prURL,
				Number: prNumber,
			}
		}

		r.broadcast(task)
		if err := r.notifyAwaitingMerge(ctx, task, prURL); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil

	case agentsv1alpha1.AgentRunPhaseFailed:
		return r.handleStepFailure(ctx, task, run, agentsv1alpha1.AgentRunStepPullRequest)

	default:
		r.updateAgentRunRef(task, run)
		task.Status.Message = fmt.Sprintf("PR agent is %s", run.Status.Phase)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
}

// createPullRequestRun creates a PR AgentRun. Either implNotes or testResults (or both) may be provided
// to give context to the PR agent.
func (r *CodingTaskReconciler) createPullRequestRun(ctx context.Context, task *agentsv1alpha1.CodingTask, implNotes, testResults string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Guard: if a PR run is already active, skip creation.
	if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepPullRequest) {
		log.Info("pull-request run already active, skipping duplicate creation")
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if err := r.notify(ctx, task, "pr-starting", func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Pull Request", "Running", "Creating pull request")
	}); err != nil {
		return ctrl.Result{}, err
	}

	contextSection := ""
	if testResults != "" {
		contextSection += fmt.Sprintf("\nTest Results: %s", testResults)
	}
	if implNotes != "" {
		contextSection += fmt.Sprintf("\nImplementation Notes: %s", implNotes)
	}

	prompt := fmt.Sprintf(`You are a pull request agent. Create a pull request for the completed work.

Original Task: %s

Plan:
%s
%s

Instructions:
1. Check out the work branch: %s
2. Push the branch to the remote
3. Create a pull request against the base branch (%s)
4. Write a clear PR title and description summarizing the changes
5. If this task originated from a GitHub issue, reference it in the PR description
6. Output the PR URL

Output the PR URL as the first line, followed by the PR description.`,
		task.Spec.Prompt, task.Status.Plan, contextSection,
		r.workBranch(task), task.Spec.Repository.Branch)

	prRun, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPullRequest, prompt, "")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating PR AgentRun: %w", err)
	}
	task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
		Name:  prRun.Name,
		Step:  agentsv1alpha1.AgentRunStepPullRequest,
		Phase: agentsv1alpha1.AgentRunPhasePending,
	})

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleStepFailure handles a failed step, retrying if under the max retry count.
func (r *CodingTaskReconciler) handleStepFailure(ctx context.Context, task *agentsv1alpha1.CodingTask, run *agentsv1alpha1.AgentRun, step agentsv1alpha1.AgentRunStep) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	r.updateAgentRunRef(task, run)

	if task.Status.RetryCount < task.Spec.MaxRetries {
		// Guard: if a run for this step is already active, don't create a duplicate retry.
		if r.hasActiveRunForStep(task, step) {
			log.Info("run already active for step, skipping duplicate retry", "step", step)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		task.Status.RetryCount++
		task.Status.Message = fmt.Sprintf("Step %s failed (attempt %d/%d), retrying", step, task.Status.RetryCount, task.Spec.MaxRetries)

		retryPrompt := fmt.Sprintf(`Retry the previous %s step. The previous attempt failed with:
%s

Original task: %s`, step, run.Status.Output, task.Spec.Prompt)

		newRun, err := r.createAgentRun(ctx, task, step, retryPrompt, run.Status.Output)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating retry AgentRun: %w", err)
		}
		task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
			Name:  newRun.Name,
			Step:  step,
			Phase: agentsv1alpha1.AgentRunPhasePending,
		})

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	return r.failTask(ctx, task, fmt.Sprintf("step %s failed after %d retries: %s", step, task.Spec.MaxRetries, run.Status.Message))
}

// failTask transitions a task to the Failed phase.
func (r *CodingTaskReconciler) failTask(ctx context.Context, task *agentsv1alpha1.CodingTask, message string) (ctrl.Result, error) {
	now := metav1.Now()
	task.Status.Phase = agentsv1alpha1.TaskPhaseFailed
	task.Status.CompletedAt = &now
	task.Status.Message = message

	r.broadcast(task)
	if err := r.notify(ctx, task, "failed", func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyFailed(ctx, owner, repo, issue, message)
	}); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// modelForStep returns the model to use for a given workflow step.
// Priority: ModelOverrides (step-specific) → ModelOverrides.Default → Spec.Model (step-specific) → Spec.Model.Default → "sonnet"
func (r *CodingTaskReconciler) modelForStep(task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep) string {
	// Check status-level overrides first (from model selection comment).
	if o := task.Status.ModelOverrides; o != nil {
		switch step {
		case agentsv1alpha1.AgentRunStepPlan:
			if o.Plan != "" {
				return o.Plan
			}
		case agentsv1alpha1.AgentRunStepImplement:
			if o.Implement != "" {
				return o.Implement
			}
		case agentsv1alpha1.AgentRunStepTest:
			if o.Test != "" {
				return o.Test
			}
		case agentsv1alpha1.AgentRunStepPullRequest:
			if o.PullRequest != "" {
				return o.PullRequest
			}
		}
		if o.Default != "" {
			return o.Default
		}
	}

	// Fall back to spec-level model config.
	m := task.Spec.Model
	switch step {
	case agentsv1alpha1.AgentRunStepPlan:
		if m.Plan != "" {
			return m.Plan
		}
	case agentsv1alpha1.AgentRunStepImplement:
		if m.Implement != "" {
			return m.Implement
		}
	case agentsv1alpha1.AgentRunStepTest:
		if m.Test != "" {
			return m.Test
		}
	case agentsv1alpha1.AgentRunStepPullRequest:
		if m.PullRequest != "" {
			return m.PullRequest
		}
	}
	if m.Default != "" {
		return m.Default
	}
	// Cost-optimized defaults: Haiku for simpler steps, Sonnet for complex ones.
	switch step {
	case agentsv1alpha1.AgentRunStepTest, agentsv1alpha1.AgentRunStepPullRequest:
		return "haiku"
	default:
		return "sonnet"
	}
}

// Default max turns per step, calibrated to typical task complexity:
//   plan:         15 turns  — explore codebase, design approach (complex/new feature)
//   implement:    50 turns  — multi-file edits, iterative coding (large task workflow)
//   test:          8 turns  — run tests, check output (multi-file operation)
//   pull-request:  3 turns  — create PR, write description (small task)
var defaultMaxTurns = map[agentsv1alpha1.AgentRunStep]int{
	agentsv1alpha1.AgentRunStepPlan:        15,
	agentsv1alpha1.AgentRunStepImplement:   50,
	agentsv1alpha1.AgentRunStepTest:        8,
	agentsv1alpha1.AgentRunStepPullRequest: 3,
}

// maxTurnsForStep returns the maxTurns to use for a given workflow step.
// Priority: per-step override → global maxTurns → built-in default.
func (r *CodingTaskReconciler) maxTurnsForStep(task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep) *int {
	m := task.Spec.Model
	switch step {
	case agentsv1alpha1.AgentRunStepPlan:
		if m.PlanMaxTurns != nil {
			return m.PlanMaxTurns
		}
	case agentsv1alpha1.AgentRunStepImplement:
		if m.ImplementMaxTurns != nil {
			return m.ImplementMaxTurns
		}
	case agentsv1alpha1.AgentRunStepTest:
		if m.TestMaxTurns != nil {
			return m.TestMaxTurns
		}
	case agentsv1alpha1.AgentRunStepPullRequest:
		if m.PullRequestMaxTurns != nil {
			return m.PullRequestMaxTurns
		}
	}
	if m.MaxTurns != nil {
		return m.MaxTurns
	}
	if v, ok := defaultMaxTurns[step]; ok {
		return &v
	}
	return nil
}

// agentImage returns the agent-runner image for the given task.
// It prefers the task-level override, then the operator default, then the CRD default.
func (r *CodingTaskReconciler) agentImage(task *agentsv1alpha1.CodingTask) string {
	if task.Spec.AgentImage != "" {
		return task.Spec.AgentImage
	}
	if r.DefaultAgentImage != "" {
		return r.DefaultAgentImage
	}
	return "ghcr.io/jcwearn/agent-runner:main"
}

// createAgentRun creates an AgentRun for the given step.
func (r *CodingTaskReconciler) createAgentRun(ctx context.Context, task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep, prompt, contextStr string) (*agentsv1alpha1.AgentRun, error) {
	runName := fmt.Sprintf("%s-%s-%d", task.Name, step, time.Now().Unix())
	maxTurns := r.maxTurnsForStep(task, step)

	// Tell the agent about its turn budget so it can plan accordingly.
	// Hitting the limit causes a hard error, so the agent needs to finish before that.
	if maxTurns != nil {
		prompt += fmt.Sprintf("\n\nIMPORTANT: You have a budget of %d agentic turns for this step. "+
			"Exceeding this limit will cause an error. Plan your work to complete well within "+
			"this budget — be focused, avoid unnecessary exploration, and prioritize finishing the task.", *maxTurns)
	}

	run := &agentsv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: task.Namespace,
			Labels: map[string]string{
				"agents.wearn.dev/task": task.Name,
				"agents.wearn.dev/step": string(step),
			},
		},
		Spec: agentsv1alpha1.AgentRunSpec{
			TaskRef:            task.Name,
			Step:               step,
			Prompt:             prompt,
			Image:              r.agentImage(task),
			Timeout:            task.Spec.StepTimeout,
			Repository:         task.Spec.Repository,
			Resources:          task.Spec.Resources,
			AnthropicAPIKeyRef: task.Spec.AnthropicAPIKeyRef,
			GitCredentialsRef:  task.Spec.GitCredentialsRef,
			ServiceAccountName: task.Spec.ServiceAccountName,
			Context:            contextStr,
			Model:              r.modelForStep(task, step),
			MaxTurns:           maxTurns,
		},
	}

	// Set the work branch if not set.
	if run.Spec.Repository.WorkBranch == "" {
		run.Spec.Repository.WorkBranch = r.workBranch(task)
	}

	// Set owner reference so AgentRuns are garbage-collected with the task.
	if err := controllerutil.SetControllerReference(task, run, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference: %w", err)
	}

	if err := r.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("creating AgentRun: %w", err)
	}

	return run, nil
}

// getLatestAgentRun finds the most recent AgentRun for a given step.
func (r *CodingTaskReconciler) getLatestAgentRun(ctx context.Context, task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep) (*agentsv1alpha1.AgentRun, error) {
	var runs agentsv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(task.Namespace),
		client.MatchingLabels{
			"agents.wearn.dev/task": task.Name,
			"agents.wearn.dev/step": string(step),
		},
	); err != nil {
		return nil, fmt.Errorf("listing AgentRuns: %w", err)
	}

	if len(runs.Items) == 0 {
		return nil, nil
	}

	// Return the most recently created run.
	latest := &runs.Items[0]
	for i := range runs.Items {
		if runs.Items[i].CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = &runs.Items[i]
		}
	}

	return latest, nil
}

// updateAgentRunRef updates the phase of an AgentRun reference in the task status.
func (r *CodingTaskReconciler) updateAgentRunRef(task *agentsv1alpha1.CodingTask, run *agentsv1alpha1.AgentRun) {
	for i := range task.Status.AgentRuns {
		if task.Status.AgentRuns[i].Name == run.Name {
			task.Status.AgentRuns[i].Phase = run.Status.Phase
			return
		}
	}
}

// hasActiveRunForStep returns true if the task's in-memory AgentRuns list already
// contains a run for the given step that is neither Failed nor Succeeded. This is
// used as an idempotency guard to prevent duplicate AgentRun creation when
// getLatestAgentRun (label-indexed API query) hasn't caught up yet.
func (r *CodingTaskReconciler) hasActiveRunForStep(task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep) bool {
	for _, ref := range task.Status.AgentRuns {
		if ref.Step == step && ref.Phase != agentsv1alpha1.AgentRunPhaseFailed && ref.Phase != agentsv1alpha1.AgentRunPhaseSucceeded {
			return true
		}
	}
	return false
}

// workBranch returns the work branch name for a task.
func (r *CodingTaskReconciler) workBranch(task *agentsv1alpha1.CodingTask) string {
	if task.Spec.Repository.WorkBranch != "" {
		return task.Spec.Repository.WorkBranch
	}
	return fmt.Sprintf("ai/%s", task.Name)
}

// notifyPlanReady posts the plan as a GitHub comment and stores the comment ID for approval tracking.
// Uses the same deduplication as notify() via the "plan-ready" key.
// The comment is posted first (we need the comment ID), then the key and PlanCommentID are persisted
// via Status().Update(). If persist fails, in-memory changes are reverted and an error is returned.
func (r *CodingTaskReconciler) notifyPlanReady(ctx context.Context, task *agentsv1alpha1.CodingTask, plan string) error {
	if r.Notifier == nil {
		return nil
	}
	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		return nil
	}
	if slices.Contains(task.Status.NotifiedPhases, "plan-ready") {
		return nil
	}

	// Post comment first (need the ID).
	gh := task.Spec.Source.GitHub
	commentID, err := r.Notifier.NotifyPlanReady(ctx, gh.Owner, gh.Repo, gh.IssueNumber, plan)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to post plan comment")
		return nil // non-fatal
	}

	// Persist key + comment ID.
	task.Status.PlanCommentID = &commentID
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, "plan-ready")
	if err := r.Status().Update(ctx, task); err != nil {
		// Revert in-memory changes.
		task.Status.PlanCommentID = nil
		task.Status.NotifiedPhases = task.Status.NotifiedPhases[:len(task.Status.NotifiedPhases)-1]
		return fmt.Errorf("persisting plan comment ID: %w", err)
	}
	return nil
}

// notifyRevisedPlan posts the revised plan as a collapsed GitHub comment and stores the comment ID.
// Uses the same deduplication as notifyPlanReady via the "plan-ready" key.
func (r *CodingTaskReconciler) notifyRevisedPlan(ctx context.Context, task *agentsv1alpha1.CodingTask, changesSummary, fullPlan string) error {
	if r.Notifier == nil {
		return nil
	}
	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		return nil
	}
	if slices.Contains(task.Status.NotifiedPhases, "plan-ready") {
		return nil
	}

	gh := task.Spec.Source.GitHub
	commentID, err := r.Notifier.NotifyRevisedPlan(ctx, gh.Owner, gh.Repo, gh.IssueNumber, changesSummary, fullPlan)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to post revised plan comment")
		return nil // non-fatal
	}

	task.Status.PlanCommentID = &commentID
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, "plan-ready")
	if err := r.Status().Update(ctx, task); err != nil {
		task.Status.PlanCommentID = nil
		task.Status.NotifiedPhases = task.Status.NotifiedPhases[:len(task.Status.NotifiedPhases)-1]
		return fmt.Errorf("persisting revised plan comment ID: %w", err)
	}
	return nil
}

// parseRevisedPlanOutput splits a revised plan output into a changes summary and the full plan.
// It looks for the "---PLAN---" separator. If the separator is missing, the entire output is
// treated as the full plan with an empty changes summary.
func parseRevisedPlanOutput(output string) (changesSummary, fullPlan string) {
	const separator = "---PLAN---"
	idx := strings.Index(output, separator)
	if idx < 0 {
		return "", strings.TrimSpace(output)
	}
	changesSummary = strings.TrimSpace(output[:idx])
	fullPlan = strings.TrimSpace(output[idx+len(separator):])
	return changesSummary, fullPlan
}

// notify sends a notification if the task originated from a GitHub issue and a Notifier is configured.
// It deduplicates using notifyKey: if the key is already in NotifiedPhases, the notification is skipped.
// The key is persisted via Status().Update() BEFORE sending the notification to prevent duplicates
// on optimistic-lock conflicts. If the persist fails, the in-memory change is reverted and an error
// is returned so the reconcile requeues with fresh state.
func (r *CodingTaskReconciler) notify(ctx context.Context, task *agentsv1alpha1.CodingTask, notifyKey string, fn func(ctx context.Context, owner, repo string, issue int) error) error {
	if r.Notifier == nil {
		return nil
	}
	owner, repo, issueNumber := r.resolveTrackingIssue(task)
	if owner == "" || repo == "" || issueNumber == 0 {
		return nil
	}
	if slices.Contains(task.Status.NotifiedPhases, notifyKey) {
		return nil
	}

	// Pre-append key and persist BEFORE sending.
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, notifyKey)
	if err := r.Status().Update(ctx, task); err != nil {
		// Revert in-memory change, return error to trigger requeue.
		task.Status.NotifiedPhases = task.Status.NotifiedPhases[:len(task.Status.NotifiedPhases)-1]
		return fmt.Errorf("persisting notification key %q: %w", notifyKey, err)
	}

	// Key persisted — safe to send (at-most-once delivery).
	if err := fn(ctx, owner, repo, issueNumber); err != nil {
		logf.FromContext(ctx).Error(err, "failed to send notification", "key", notifyKey)
	}
	return nil
}

// handleAwaitingMerge monitors a PR for merge, closure, or change-request feedback.
func (r *CodingTaskReconciler) handleAwaitingMerge(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. Check for an active implement run (from a change-request cycle).
	if result, done, err := r.checkChangeRequestRun(ctx, task); done {
		return result, err
	}

	// 2. Check PR merge status.
	if result, done, err := r.checkPRMergeStatus(ctx, task); done {
		return result, err
	}

	// 3. Check for change-request feedback on the tracking issue and on the PR itself.
	if r.ApprovalChecker != nil && task.Status.PRCommentID != nil {
		owner, repo, issueNumber := r.resolveTrackingIssue(task)
		if owner != "" && repo != "" && issueNumber > 0 {
			feedback, err := r.ApprovalChecker.CheckForFeedback(ctx, owner, repo, issueNumber, *task.Status.PRCommentID)
			if err != nil {
				log.Error(err, "failed to check for feedback on issue")
			} else if feedback != "" {
				log.Info("change request feedback received on issue", "feedback", feedback)
				return r.handleChangeRequest(ctx, task, feedback, issueNumber)
			}

			// Also check top-level comments on the PR (GitHub treats PRs as issues in its API).
			if task.Status.PullRequest != nil && task.Status.PullRequest.Number > 0 && task.Status.PullRequest.Number != issueNumber {
				feedback, err = r.ApprovalChecker.CheckForFeedback(ctx, owner, repo, task.Status.PullRequest.Number, *task.Status.PRCommentID)
				if err != nil {
					log.Error(err, "failed to check for feedback on PR")
				} else if feedback != "" {
					log.Info("change request feedback received on PR", "feedback", feedback)
					return r.handleChangeRequest(ctx, task, feedback, task.Status.PullRequest.Number)
				}
			}

			// Check for PR review feedback (request changes with inline comments).
			if task.Status.PullRequest != nil && task.Status.PullRequest.Number > 0 {
				feedback, err = r.ApprovalChecker.CheckForReviewFeedback(ctx, owner, repo, task.Status.PullRequest.Number, *task.Status.PRCommentID)
				if err != nil {
					log.Error(err, "failed to check for review feedback")
				} else if feedback != "" {
					log.Info("change request feedback received via PR review", "feedback", feedback)
					return r.handleChangeRequest(ctx, task, feedback, task.Status.PullRequest.Number)
				}
			}
		}
	}

	// 4. Nothing happened — requeue.
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// checkChangeRequestRun checks if a change-request implement run is active or completed.
// Returns (result, true, err) if the caller should return, or (_, false, nil) to continue.
func (r *CodingTaskReconciler) checkChangeRequestRun(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)

	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement)
	if err != nil {
		return ctrl.Result{}, true, err
	}
	if run == nil {
		return ctrl.Result{}, false, nil
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		// If PRCommentID is nil, this is a change-request run that needs the awaiting-merge comment re-posted.
		if task.Status.PRCommentID == nil {
			log.Info("change-request implement run succeeded, re-posting awaiting-merge comment")
			r.updateAgentRunRef(task, run)
			task.Status.NotifiedPhases = slices.DeleteFunc(task.Status.NotifiedPhases, func(s string) bool {
				return s == "awaiting-merge"
			})
			prURL := ""
			if task.Status.PullRequest != nil {
				prURL = task.Status.PullRequest.URL
			}
			task.Status.Message = "Changes pushed, awaiting merge"
			if err := r.notifyAwaitingMerge(ctx, task, prURL); err != nil {
				return ctrl.Result{}, true, err
			}
			if err := r.Status().Update(ctx, task); err != nil {
				return ctrl.Result{}, true, err
			}
			return ctrl.Result{RequeueAfter: 60 * time.Second}, true, nil
		}

	case agentsv1alpha1.AgentRunPhaseFailed:
		// Only handle failure for change-request runs (PRCommentID is nil).
		if task.Status.PRCommentID == nil {
			res, err := r.handleStepFailure(ctx, task, run, agentsv1alpha1.AgentRunStepImplement)
			return res, true, err
		}

	default:
		// Still running — wait for it.
		r.updateAgentRunRef(task, run)
		task.Status.Message = fmt.Sprintf("Change-request implementation agent is %s", run.Status.Phase)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, true, nil
	}

	return ctrl.Result{}, false, nil
}

// checkPRMergeStatus checks if the PR has been merged or closed.
// Returns (result, true, err) if the caller should return, or (_, false, nil) to continue.
func (r *CodingTaskReconciler) checkPRMergeStatus(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, bool, error) {
	if r.PRStatusChecker == nil || task.Status.PullRequest == nil || task.Status.PullRequest.Number == 0 {
		return ctrl.Result{}, false, nil
	}
	log := logf.FromContext(ctx)

	owner, repo, _ := r.resolveTrackingIssue(task)
	if owner == "" || repo == "" {
		return ctrl.Result{}, false, nil
	}

	prStatus, err := r.PRStatusChecker.CheckPRStatus(ctx, owner, repo, task.Status.PullRequest.Number)
	if err != nil {
		log.Error(err, "failed to check PR status")
		return ctrl.Result{}, false, nil
	}

	if prStatus.Merged {
		log.Info("PR merged, completing task")
		now := metav1.Now()
		task.Status.Phase = agentsv1alpha1.TaskPhaseComplete
		task.Status.CompletedAt = &now
		task.Status.Message = "PR merged"
		r.broadcast(task)
		if err := r.notify(ctx, task, "complete", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyComplete(ctx, owner, repo, issue, task.Status.PullRequest.URL)
		}); err != nil {
			return ctrl.Result{}, true, err
		}
		r.closeTrackingIssue(ctx, task)
		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{}, true, nil
	}

	if prStatus.Closed {
		res, err := r.failTask(ctx, task, "PR closed without being merged")
		return res, true, err
	}

	return ctrl.Result{}, false, nil
}

// handleChangeRequest creates an implement AgentRun to address reviewer feedback on a PR.
// feedbackSourceNumber is the issue/PR number where the feedback was posted; used to
// acknowledge the feedback at the source location when it differs from the tracking issue.
func (r *CodingTaskReconciler) handleChangeRequest(ctx context.Context, task *agentsv1alpha1.CodingTask, feedback string, feedbackSourceNumber int) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Guard: if an implement run is already active, don't create another.
	if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepImplement) {
		log.Info("implement run already active, skipping change-request creation")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Post acknowledgment where the feedback was left.
	// Skip if source is the tracking issue — the notify() below already covers it.
	_, _, trackingIssueNumber := r.resolveTrackingIssue(task)
	if r.Notifier != nil && feedbackSourceNumber > 0 && feedbackSourceNumber != trackingIssueNumber {
		owner, repo, _ := r.resolveTrackingIssue(task)
		if owner != "" && repo != "" {
			if err := r.Notifier.NotifyStepUpdate(ctx, owner, repo, feedbackSourceNumber,
				"Change Request", "Running", "Starting to implement your feedback"); err != nil {
				logf.FromContext(ctx).Error(err, "failed to post feedback acknowledgment")
			}
		}
	}

	// Clear PRCommentID so handleAwaitingMerge knows to re-post after the run completes.
	task.Status.PRCommentID = nil
	task.Status.RetryCount = 0
	task.Status.Message = "Addressing reviewer feedback"
	r.broadcast(task)

	if err := r.notify(ctx, task, fmt.Sprintf("change-request-%d", time.Now().Unix()), func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Change Request", "Running", "Addressing reviewer feedback")
	}); err != nil {
		return ctrl.Result{}, err
	}

	prURL := ""
	if task.Status.PullRequest != nil {
		prURL = task.Status.PullRequest.URL
	}

	prompt := fmt.Sprintf(`You are an implementation agent. A reviewer has requested changes on your pull request. Address their feedback.

Original Task: %s

Plan:
%s

Pull Request: %s

Reviewer Feedback:
%s

Instructions:
1. Check out the work branch: %s
2. Pull the latest changes
3. Address the reviewer's feedback by modifying the code
4. Commit and push your changes
5. Do NOT create a new pull request — the existing PR will be updated automatically

Output a summary of what you changed.`, task.Spec.Prompt, task.Status.Plan, prURL, feedback, r.workBranch(task))

	newRun, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement, prompt, feedback)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating change-request AgentRun: %w", err)
	}
	task.Status.AgentRuns = append(task.Status.AgentRuns, agentsv1alpha1.AgentRunReference{
		Name:  newRun.Name,
		Step:  agentsv1alpha1.AgentRunStepImplement,
		Phase: agentsv1alpha1.AgentRunPhasePending,
	})

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// parsePRNumber extracts the PR number from a GitHub PR URL.
// Example: "https://github.com/owner/repo/pull/42" → 42
func parsePRNumber(prURL string) int {
	idx := strings.LastIndex(prURL, "/pull/")
	if idx < 0 {
		return 0
	}
	numStr := strings.TrimSpace(prURL[idx+len("/pull/"):])
	// Remove anything after the number (query params, fragments, newlines).
	for i, c := range numStr {
		if c < '0' || c > '9' {
			numStr = numStr[:i]
			break
		}
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

// notifyAwaitingMerge posts a "PR ready, awaiting merge" comment and stores the comment ID.
func (r *CodingTaskReconciler) notifyAwaitingMerge(ctx context.Context, task *agentsv1alpha1.CodingTask, prURL string) error {
	if r.Notifier == nil {
		return nil
	}
	owner, repo, issueNumber := r.resolveTrackingIssue(task)
	if owner == "" || repo == "" || issueNumber == 0 {
		return nil
	}
	if slices.Contains(task.Status.NotifiedPhases, "awaiting-merge") {
		return nil
	}

	commentID, err := r.Notifier.NotifyAwaitingMerge(ctx, owner, repo, issueNumber, prURL)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to post awaiting-merge comment")
		return nil // non-fatal
	}

	task.Status.PRCommentID = &commentID
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, "awaiting-merge")
	if err := r.Status().Update(ctx, task); err != nil {
		task.Status.PRCommentID = nil
		task.Status.NotifiedPhases = task.Status.NotifiedPhases[:len(task.Status.NotifiedPhases)-1]
		return fmt.Errorf("persisting awaiting-merge comment ID: %w", err)
	}
	return nil
}

// closeTrackingIssue closes the tracking GitHub issue for a task.
func (r *CodingTaskReconciler) closeTrackingIssue(ctx context.Context, task *agentsv1alpha1.CodingTask) {
	if r.Notifier == nil {
		return
	}
	owner, repo, issueNumber := r.resolveTrackingIssue(task)
	if owner == "" || repo == "" || issueNumber == 0 {
		return
	}
	if err := r.Notifier.CloseIssue(ctx, owner, repo, issueNumber); err != nil {
		logf.FromContext(ctx).Error(err, "failed to close tracking issue", "issue", issueNumber)
	}
}

// resolveTrackingIssue returns owner, repo, issueNumber from the task's tracking issue
// or source GitHub issue.
func (r *CodingTaskReconciler) resolveTrackingIssue(task *agentsv1alpha1.CodingTask) (string, string, int) {
	if task.Status.TrackingIssue != nil {
		return task.Status.TrackingIssue.Owner, task.Status.TrackingIssue.Repo, task.Status.TrackingIssue.IssueNumber
	}
	if task.Spec.Source.Type == agentsv1alpha1.TaskSourceGitHubIssue && task.Spec.Source.GitHub != nil {
		return task.Spec.Source.GitHub.Owner, task.Spec.Source.GitHub.Repo, task.Spec.Source.GitHub.IssueNumber
	}
	return "", "", 0
}

// broadcast sends a real-time event if a Broadcaster is configured.
func (r *CodingTaskReconciler) broadcast(task *agentsv1alpha1.CodingTask) {
	if r.Broadcaster == nil {
		return
	}
	r.Broadcaster.Broadcast(BroadcastEvent{
		Type:    "task_update",
		Task:    task.Name,
		Phase:   string(task.Status.Phase),
		Message: task.Status.Message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *CodingTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.CodingTask{}).
		Owns(&agentsv1alpha1.AgentRun{}).
		Named("codingtask").
		Complete(r)
}
