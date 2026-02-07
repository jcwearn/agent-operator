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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

// CodingTaskReconciler reconciles a CodingTask object.
type CodingTaskReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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
		task.Status.TotalSteps = 4
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

// handlePlanning checks if the planning AgentRun has completed and transitions accordingly.
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
		task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
		task.Status.CurrentStep = 2
		task.Status.RetryCount = 0
		task.Status.Message = "Plan complete, starting implementation"
		r.updateAgentRunRef(task, run)

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

// handleImplementing checks if the implementation AgentRun has completed.
func (r *CodingTaskReconciler) handleImplementing(ctx context.Context, task *agentsv1alpha1.CodingTask) (ctrl.Result, error) {
	run, err := r.getLatestAgentRun(ctx, task, agentsv1alpha1.AgentRunStepImplement)
	if err != nil {
		return ctrl.Result{}, err
	}
	if run == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhaseSucceeded:
		task.Status.Phase = agentsv1alpha1.TaskPhaseTesting
		task.Status.CurrentStep = 3
		task.Status.RetryCount = 0
		task.Status.Message = "Implementation complete, running tests"
		r.updateAgentRunRef(task, run)

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
		task.Status.CurrentStep = 4
		task.Status.RetryCount = 0
		task.Status.Message = "Tests passed, creating pull request"
		r.updateAgentRunRef(task, run)

		prompt := fmt.Sprintf(`You are a pull request agent. Create a pull request for the completed work.

Original Task: %s

Plan:
%s

Test Results: %s

Instructions:
1. Check out the work branch: %s
2. Push the branch to the remote
3. Create a pull request against the base branch (%s)
4. Write a clear PR title and description summarizing the changes
5. If this task originated from a GitHub issue, reference it in the PR description
6. Output the PR URL

Output the PR URL as the first line, followed by the PR description.`,
			task.Spec.Prompt, task.Status.Plan, run.Status.Output,
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

	case agentsv1alpha1.AgentRunPhaseFailed:
		// Test failure: retry the implement step with error context.
		if task.Status.RetryCount < task.Spec.MaxRetries {
			task.Status.RetryCount++
			task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
			task.Status.CurrentStep = 2
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
		if run.Status.Output != "" {
			task.Status.PullRequest = &agentsv1alpha1.PullRequestInfo{
				URL: run.Status.Output,
			}
		}

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

// handleStepFailure handles a failed step, retrying if under the max retry count.
func (r *CodingTaskReconciler) handleStepFailure(ctx context.Context, task *agentsv1alpha1.CodingTask, run *agentsv1alpha1.AgentRun, step agentsv1alpha1.AgentRunStep) (ctrl.Result, error) {
	r.updateAgentRunRef(task, run)

	if task.Status.RetryCount < task.Spec.MaxRetries {
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

	if err := r.Status().Update(ctx, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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
			Image:              task.Spec.AgentImage,
			Timeout:            task.Spec.StepTimeout,
			Repository:         task.Spec.Repository,
			Resources:          task.Spec.Resources,
			AnthropicAPIKeyRef: task.Spec.AnthropicAPIKeyRef,
			GitCredentialsRef:  task.Spec.GitCredentialsRef,
			ServiceAccountName: task.Spec.ServiceAccountName,
			Context:            contextStr,
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

// workBranch returns the work branch name for a task.
func (r *CodingTaskReconciler) workBranch(task *agentsv1alpha1.CodingTask) string {
	if task.Spec.Repository.WorkBranch != "" {
		return task.Spec.Repository.WorkBranch
	}
	return fmt.Sprintf("ai/%s", task.Name)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CodingTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.CodingTask{}).
		Owns(&agentsv1alpha1.AgentRun{}).
		Named("codingtask").
		Complete(r)
}
