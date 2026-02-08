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
	NotifyStepUpdate(ctx context.Context, owner, repo string, issue int, step, status, msg string) error
	NotifyComplete(ctx context.Context, owner, repo string, issue int, prURL string) error
	NotifyFailed(ctx context.Context, owner, repo string, issue int, reason string) error
}

// ApprovalChecker checks whether a plan has been approved via GitHub reactions or comments.
type ApprovalChecker interface {
	CheckApproval(ctx context.Context, owner, repo string, issue int, commentID int64) (approved bool, runTests bool, feedback string, err error)
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
	Scheme            *runtime.Scheme
	Notifier          Notifier        // optional, nil-safe
	Broadcaster       Broadcaster     // optional, nil-safe
	ApprovalChecker   ApprovalChecker // optional; if nil, plans are auto-approved
	DefaultAgentImage string          // default agent-runner image; used when CodingTask.Spec.AgentImage is empty
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
	default:
		log.Info("unknown phase", "phase", task.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handlePending transitions a new task to the Planning phase by creating a plan AgentRun.
func (r *CodingTaskReconciler) handlePending(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
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

	prompt := fmt.Sprintf(`You are a planning agent. Analyze the following task and the repository codebase, then produce a detailed implementation plan.

Task: %s

Instructions:
1. Clone and explore the repository structure
2. Identify the files that need to be created or modified
3. Break the work into clear, ordered steps
4. Consider edge cases and testing requirements
5. Output your plan as structured markdown
6. If there are technical or product decisions to make, list them clearly as questions so the reviewer can provide guidance before implementation begins

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
		task.Status.Plan = run.Status.Output
		task.Status.Phase = agentsv1alpha1.TaskPhaseAwaitingApproval
		task.Status.CurrentStep = 2
		task.Status.RetryCount = 0
		task.Status.Message = "Plan ready, awaiting approval"
		r.updateAgentRunRef(task, run)
		r.broadcast(task)

		// Post the plan as a comment and store the comment ID for approval tracking.
		r.notifyPlanReady(ctx, task, run.Status.Output)

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
			r.notifyPlanReady(ctx, task, task.Status.Plan)
			if task.Status.PlanCommentID != nil {
				if err := r.Status().Update(ctx, task); err != nil {
					return ctrl.Result{}, err
				}
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
	approved, runTests, feedback, err := r.ApprovalChecker.CheckApproval(ctx, gh.Owner, gh.Repo, gh.IssueNumber, *task.Status.PlanCommentID)
	if err != nil {
		log.Error(err, "failed to check approval")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if approved {
		log.Info("plan approved via reaction", "runTests", runTests)
		task.Status.RunTests = runTests
		if runTests {
			task.Status.TotalSteps = 5
		} else {
			task.Status.TotalSteps = 4
		}
		return r.transitionToImplementing(ctx, task)
	}

	if feedback != "" {
		// Guard: if a plan run is already active (e.g., from a previous re-plan attempt), don't create another.
		if r.hasActiveRunForStep(task, agentsv1alpha1.AgentRunStepPlan) {
			log.Info("plan run already active, skipping duplicate re-plan creation")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Feedback received — re-plan with the feedback incorporated.
		log.Info("feedback received, re-planning", "feedback", feedback)
		task.Status.Phase = agentsv1alpha1.TaskPhasePlanning
		task.Status.CurrentStep = 1
		task.Status.RetryCount = 0
		task.Status.PlanCommentID = nil
		task.Status.Message = "Re-planning with reviewer feedback"
		r.broadcast(task)

		prompt := fmt.Sprintf(`You are a planning agent. Your previous plan received feedback from a reviewer. Revise the plan based on their feedback.

Task: %s

Previous Plan:
%s

Reviewer Feedback:
%s

Instructions:
1. Address the reviewer's feedback
2. Revise the plan accordingly
3. Keep the same structured markdown format
4. If there are technical or product decisions to make, list them clearly as questions so the reviewer can provide guidance before implementation begins

Output ONLY the revised plan — no code implementation.`, task.Spec.Prompt, task.Status.Plan, feedback)

		run, err := r.createAgentRun(ctx, task, agentsv1alpha1.AgentRunStepPlan, prompt, feedback)
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

	prompt := fmt.Sprintf(`You are an implementation agent. Implement the following plan in the repository.

Original Task: %s

Plan:
%s

Instructions:
1. Create a new branch: %s
2. Implement all changes described in the plan
3. Write tests that cover the changes
4. Commit your changes with a descriptive commit message
5. Do NOT open a pull request — that will be handled separately

Output a summary of what you implemented and any notes for the tester.`, task.Spec.Prompt, task.Status.Plan, r.workBranch(task))

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
			r.notify(ctx, task, "implement-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
				return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Implementation", "Succeeded", "Creating pull request (tests skipped)")
			})
			return r.createPullRequestRun(ctx, task, run.Status.Output, "")
		}

		task.Status.Phase = agentsv1alpha1.TaskPhaseTesting
		task.Status.CurrentStep = task.Status.TotalSteps - 1
		task.Status.Message = "Implementation complete, running tests"
		r.broadcast(task)
		r.notify(ctx, task, "implement-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Implementation", "Succeeded", "Starting tests")
		})

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
		r.notify(ctx, task, "test-succeeded", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyStepUpdate(ctx, owner, repo, issue, "Testing", "Succeeded", "Creating pull request")
		})

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
		now := metav1.Now()
		task.Status.Phase = agentsv1alpha1.TaskPhaseComplete
		task.Status.CompletedAt = &now
		task.Status.Message = "Pull request created successfully"
		r.updateAgentRunRef(task, run)

		// The output should contain the PR URL on the first line.
		prURL := ""
		if run.Status.Output != "" {
			prURL = run.Status.Output
			task.Status.PullRequest = &agentsv1alpha1.PullRequestInfo{
				URL: prURL,
			}
		}

		r.broadcast(task)
		r.notify(ctx, task, "complete", func(ctx context.Context, owner, repo string, issue int) error {
			return r.Notifier.NotifyComplete(ctx, owner, repo, issue, prURL)
		})

		if err := r.Status().Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

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
	r.notify(ctx, task, "failed", func(ctx context.Context, owner, repo string, issue int) error {
		return r.Notifier.NotifyFailed(ctx, owner, repo, issue, message)
	})

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// modelForStep returns the model to use for a given workflow step, falling back to the default.
func (r *CodingTaskReconciler) modelForStep(task *agentsv1alpha1.CodingTask, step agentsv1alpha1.AgentRunStep) string {
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
	return "sonnet"
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
			MaxTurns:           task.Spec.Model.MaxTurns,
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
func (r *CodingTaskReconciler) notifyPlanReady(ctx context.Context, task *agentsv1alpha1.CodingTask, plan string) {
	if r.Notifier == nil {
		return
	}
	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		return
	}
	if slices.Contains(task.Status.NotifiedPhases, "plan-ready") {
		return
	}
	gh := task.Spec.Source.GitHub
	commentID, err := r.Notifier.NotifyPlanReady(ctx, gh.Owner, gh.Repo, gh.IssueNumber, plan)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to post plan comment")
		return
	}
	task.Status.PlanCommentID = &commentID
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, "plan-ready")
}

// notify sends a notification if the task originated from a GitHub issue and a Notifier is configured.
// It deduplicates using notifyKey: if the key is already in NotifiedPhases, the notification is skipped.
// On success the key is appended so it won't fire again on re-reconciliation.
func (r *CodingTaskReconciler) notify(ctx context.Context, task *agentsv1alpha1.CodingTask, notifyKey string, fn func(ctx context.Context, owner, repo string, issue int) error) {
	if r.Notifier == nil {
		return
	}
	if task.Spec.Source.Type != agentsv1alpha1.TaskSourceGitHubIssue || task.Spec.Source.GitHub == nil {
		return
	}
	if slices.Contains(task.Status.NotifiedPhases, notifyKey) {
		return
	}
	gh := task.Spec.Source.GitHub
	if err := fn(ctx, gh.Owner, gh.Repo, gh.IssueNumber); err != nil {
		logf.FromContext(ctx).Error(err, "failed to send notification", "key", notifyKey)
		return // Don't mark as notified so it retries next reconcile.
	}
	task.Status.NotifiedPhases = append(task.Status.NotifiedPhases, notifyKey)
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
