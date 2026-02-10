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
	"io"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

const (
	outputVolumeName    = "agent-output"
	outputMountPath     = "/agent/output"
	workspaceMountPath  = "/agent/workspace"
	workspaceVolumeName = "workspace"
)

// GitTokenProvider generates short-lived tokens for git operations.
type GitTokenProvider interface {
	Token() (string, error)
}

// AgentRunReconciler reconciles an AgentRun object.
type AgentRunReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Clientset          kubernetes.Interface // for reading pod logs (full output extraction)
	GitTokenProvider   GitTokenProvider     // optional; if set, injects a fresh token instead of using GitCredentialsRef
	PodRetentionPeriod time.Duration        // how long to keep succeeded pods; 0 means delete immediately
}

// +kubebuilder:rbac:groups=agents.wearn.dev,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agents.wearn.dev,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agents.wearn.dev,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete

func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var run agentsv1alpha1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Terminal states — clean up succeeded pods after retention period.
	if run.Status.Phase == agentsv1alpha1.AgentRunPhaseSucceeded ||
		run.Status.Phase == agentsv1alpha1.AgentRunPhaseFailed {
		return r.handleTerminal(ctx, &run)
	}

	// Initialize if new.
	if run.Status.Phase == "" {
		run.Status.Phase = agentsv1alpha1.AgentRunPhasePending
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Find existing pod for this run.
	pod, err := r.findPod(ctx, &run)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch run.Status.Phase {
	case agentsv1alpha1.AgentRunPhasePending:
		if pod != nil {
			// Pod already exists, transition to Running.
			run.Status.Phase = agentsv1alpha1.AgentRunPhaseRunning
			now := metav1.Now()
			run.Status.StartedAt = &now
			run.Status.PodName = pod.Name
			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Create the agent pod.
		log.Info("creating agent pod", "step", run.Spec.Step)
		pod, err = r.createPod(ctx, &run)
		if err != nil {
			run.Status.Phase = agentsv1alpha1.AgentRunPhaseFailed
			run.Status.Message = fmt.Sprintf("failed to create pod: %v", err)
			now := metav1.Now()
			run.Status.CompletedAt = &now
			if statusErr := r.Status().Update(ctx, &run); statusErr != nil {
				return ctrl.Result{}, statusErr
			}
			return ctrl.Result{}, nil
		}

		run.Status.Phase = agentsv1alpha1.AgentRunPhaseRunning
		now := metav1.Now()
		run.Status.StartedAt = &now
		run.Status.PodName = pod.Name
		run.Status.Message = "Agent pod created"
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case agentsv1alpha1.AgentRunPhaseRunning:
		if pod == nil {
			// Pod disappeared unexpectedly.
			run.Status.Phase = agentsv1alpha1.AgentRunPhaseFailed
			run.Status.Message = "agent pod disappeared unexpectedly"
			now := metav1.Now()
			run.Status.CompletedAt = &now
			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		// Check pod status.
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			log.Info("agent pod completed successfully", "pod", pod.Name)
			run.Status.Phase = agentsv1alpha1.AgentRunPhaseSucceeded
			run.Status.Message = "Agent completed successfully"
			now := metav1.Now()
			run.Status.CompletedAt = &now

			// Extract output from pod logs or termination message.
			output := r.extractOutput(ctx, pod)
			run.Status.Output = output

			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil

		case corev1.PodFailed:
			log.Info("agent pod failed", "pod", pod.Name)
			run.Status.Phase = agentsv1alpha1.AgentRunPhaseFailed
			now := metav1.Now()
			run.Status.CompletedAt = &now

			output := r.extractOutput(ctx, pod)
			run.Status.Output = output
			run.Status.Message = fmt.Sprintf("Agent pod failed: %s", pod.Status.Message)

			if len(pod.Status.ContainerStatuses) > 0 {
				cs := pod.Status.ContainerStatuses[0]
				if cs.State.Terminated != nil {
					exitCode := cs.State.Terminated.ExitCode
					run.Status.ExitCode = &exitCode
					if cs.State.Terminated.Message != "" {
						run.Status.Message = cs.State.Terminated.Message
					}
				}
			}

			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil

		default:
			// Still running — check for timeout.
			if run.Status.StartedAt != nil {
				timeout, err := time.ParseDuration(run.Spec.Timeout)
				if err != nil {
					timeout = 30 * time.Minute
				}
				if time.Since(run.Status.StartedAt.Time) > timeout {
					log.Info("agent pod timed out, deleting", "pod", pod.Name)
					if err := r.Delete(ctx, pod); err != nil {
						log.Error(err, "failed to delete timed-out pod")
					}
					run.Status.Phase = agentsv1alpha1.AgentRunPhaseFailed
					run.Status.Message = fmt.Sprintf("agent timed out after %s", run.Spec.Timeout)
					now := metav1.Now()
					run.Status.CompletedAt = &now
					if err := r.Status().Update(ctx, &run); err != nil {
						return ctrl.Result{}, err
					}
					return ctrl.Result{}, nil
				}
			}

			run.Status.Message = fmt.Sprintf("Agent pod is %s", pod.Status.Phase)
			if err := r.Status().Update(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}

	default:
		log.Info("unknown AgentRun phase", "phase", run.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// handleTerminal manages cleanup for terminal AgentRuns.
// Failed runs are never auto-deleted. Succeeded runs have their pods deleted
// after PodRetentionPeriod has elapsed since completion.
func (r *AgentRunReconciler) handleTerminal(ctx context.Context, run *agentsv1alpha1.AgentRun) (ctrl.Result, error) {
	// Never auto-delete pods for failed runs.
	if run.Status.Phase == agentsv1alpha1.AgentRunPhaseFailed {
		return ctrl.Result{}, nil
	}

	// Succeeded run — check if pod should be cleaned up.
	pod, err := r.findPod(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod == nil {
		// Already cleaned up.
		return ctrl.Result{}, nil
	}

	if run.Status.CompletedAt == nil {
		return ctrl.Result{}, nil
	}

	elapsed := time.Since(run.Status.CompletedAt.Time)
	if elapsed < r.PodRetentionPeriod {
		// Not yet — requeue for when retention expires.
		return ctrl.Result{RequeueAfter: r.PodRetentionPeriod - elapsed}, nil
	}

	// Retention expired — delete the pod.
	log := logf.FromContext(ctx)
	log.Info("deleting completed pod after retention period", "pod", pod.Name, "elapsed", elapsed)
	if err := r.Delete(ctx, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// createPod creates an ephemeral pod for the agent run.
func (r *AgentRunReconciler) createPod(ctx context.Context, run *agentsv1alpha1.AgentRun) (*corev1.Pod, error) {
	podName := fmt.Sprintf("%s-pod", run.Name)

	// Parse resource limits.
	cpuLimit := resource.MustParse(run.Spec.Resources.CPU)
	memLimit := resource.MustParse(run.Spec.Resources.Memory)

	// Halve requests vs limits.
	cpuRequest := cpuLimit.DeepCopy()
	cpuRequest.Set(cpuLimit.Value() / 2)
	memRequest := memLimit.DeepCopy()
	memRequest.Set(memLimit.Value() / 2)

	// Build git token env vars — prefer fresh installation token from GitTokenProvider,
	// fall back to static secret ref. When both a GitTokenProvider and GitCredentialsRef
	// are configured, inject the PAT as GITHUB_PAT for attachment downloads.
	gitTokenEnvVars, err := r.buildGitTokenEnvVars(run)
	if err != nil {
		return nil, fmt.Errorf("building git token env vars: %w", err)
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: run.Namespace,
			Labels: map[string]string{
				"agents.wearn.dev/agentrun": run.Name,
				"agents.wearn.dev/task":     run.Spec.TaskRef,
				"agents.wearn.dev/step":     string(run.Spec.Step),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: run.Spec.ServiceAccountName,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: boolPtr(true),
				RunAsUser:    int64Ptr(1000),
				FSGroup:      int64Ptr(1000),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{
				{
					Name:            "agent",
					Image:           run.Spec.Image,
					ImagePullPolicy: corev1.PullAlways,
					Env: append([]corev1.EnvVar{
						{
							Name: "ANTHROPIC_API_KEY",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: run.Spec.AnthropicAPIKeyRef.Name,
									},
									Key: run.Spec.AnthropicAPIKeyRef.Key,
								},
							},
						},
					}, append(gitTokenEnvVars, []corev1.EnvVar{
						{Name: "AGENT_STEP", Value: string(run.Spec.Step)},
						{Name: "AGENT_REPO_URL", Value: run.Spec.Repository.URL},
						{Name: "AGENT_BASE_BRANCH", Value: run.Spec.Repository.Branch},
						{Name: "AGENT_WORK_BRANCH", Value: run.Spec.Repository.WorkBranch},
						{Name: "AGENT_PROMPT", Value: run.Spec.Prompt},
						{Name: "AGENT_CONTEXT", Value: run.Spec.Context},
						{Name: "AGENT_MODEL", Value: run.Spec.Model},
						{Name: "AGENT_MAX_TURNS", Value: intPtrToString(run.Spec.MaxTurns)},
						{Name: "AGENT_OUTPUT_DIR", Value: outputMountPath},
						{Name: "AGENT_WORKSPACE_DIR", Value: workspaceMountPath},
					}...)...),
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    cpuRequest,
							corev1.ResourceMemory: memRequest,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    cpuLimit,
							corev1.ResourceMemory: memLimit,
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: boolPtr(false),
						ReadOnlyRootFilesystem:   boolPtr(false),
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      workspaceVolumeName,
							MountPath: workspaceMountPath,
						},
						{
							Name:      outputVolumeName,
							MountPath: outputMountPath,
						},
					},
					// Termination message allows the pod to write output to a file
					// that Kubernetes captures and exposes in the pod status.
					TerminationMessagePath:   "/agent/output/termination-message",
					TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: workspaceVolumeName,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: outputVolumeName,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	// Set owner reference so pods are cleaned up with the AgentRun.
	if err := controllerutil.SetControllerReference(run, pod, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference: %w", err)
	}

	if err := r.Create(ctx, pod); err != nil {
		return nil, fmt.Errorf("creating pod: %w", err)
	}

	return pod, nil
}

// findPod finds the pod associated with this AgentRun.
func (r *AgentRunReconciler) findPod(ctx context.Context, run *agentsv1alpha1.AgentRun) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(run.Namespace),
		client.MatchingLabels{
			"agents.wearn.dev/agentrun": run.Name,
		},
	); err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	if len(pods.Items) == 0 {
		return nil, nil
	}

	return &pods.Items[0], nil
}

// extractOutput reads the full agent output, trying pod logs first (no size limit)
// and falling back to the termination message (4096-byte K8s limit) for backwards
// compatibility with older agent-runner images that don't emit log markers.
func (r *AgentRunReconciler) extractOutput(ctx context.Context, pod *corev1.Pod) string {
	log := logf.FromContext(ctx)

	if output, err := r.extractOutputFromLogs(ctx, pod); err != nil {
		log.V(1).Info("failed to extract output from pod logs, falling back to termination message", "pod", pod.Name, "error", err)
	} else if output != "" {
		return output
	}

	// Fallback: read from termination message.
	if len(pod.Status.ContainerStatuses) > 0 {
		cs := pod.Status.ContainerStatuses[0]
		if cs.State.Terminated != nil && cs.State.Terminated.Message != "" {
			return cs.State.Terminated.Message
		}
	}
	return ""
}

const (
	outputBeginMarker = "===AGENT_OUTPUT_BEGIN==="
	outputEndMarker   = "===AGENT_OUTPUT_END==="
)

// extractOutputFromLogs reads the full agent output from pod logs by parsing the
// content between ===AGENT_OUTPUT_BEGIN=== and ===AGENT_OUTPUT_END=== markers.
// This avoids the 4096-byte Kubernetes termination message limit.
func (r *AgentRunReconciler) extractOutputFromLogs(ctx context.Context, pod *corev1.Pod) (string, error) {
	if r.Clientset == nil {
		return "", fmt.Errorf("clientset not configured")
	}

	stream, err := r.Clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("opening log stream: %w", err)
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("reading log stream: %w", err)
	}

	logs := string(data)

	// Find the last occurrence of the markers (in case there are retries/multiple outputs).
	beginIdx := strings.LastIndex(logs, outputBeginMarker)
	if beginIdx == -1 {
		return "", nil
	}
	afterBegin := beginIdx + len(outputBeginMarker)
	// Skip the newline after the begin marker.
	if afterBegin < len(logs) && logs[afterBegin] == '\n' {
		afterBegin++
	}

	endIdx := strings.LastIndex(logs, outputEndMarker)
	if endIdx == -1 || endIdx <= beginIdx {
		return "", nil
	}

	output := logs[afterBegin:endIdx]
	// Trim trailing newline that echo adds.
	output = strings.TrimSuffix(output, "\n")
	return output, nil
}

// buildGitTokenEnvVars returns the GIT_TOKEN env var (and optionally GITHUB_PAT).
// If a GitTokenProvider is configured, it mints a fresh installation token (valid ~1 hour)
// for GIT_TOKEN. When both a GitTokenProvider and GitCredentialsRef are available, the PAT
// from GitCredentialsRef is also injected as GITHUB_PAT for attachment downloads (GitHub
// App installation tokens lack access to user-attachments URLs on private repos).
// If no GitTokenProvider is configured, falls back to using GitCredentialsRef for GIT_TOKEN.
func (r *AgentRunReconciler) buildGitTokenEnvVars(run *agentsv1alpha1.AgentRun) ([]corev1.EnvVar, error) {
	if r.GitTokenProvider != nil {
		token, err := r.GitTokenProvider.Token()
		if err != nil {
			return nil, fmt.Errorf("minting installation token: %w", err)
		}
		envVars := []corev1.EnvVar{
			{Name: "GIT_TOKEN", Value: token},
		}
		// When a PAT is also available, inject it for attachment downloads.
		if run.Spec.GitCredentialsRef.Name != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name: "GITHUB_PAT",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: run.Spec.GitCredentialsRef.Name,
						},
						Key: run.Spec.GitCredentialsRef.Key,
					},
				},
			})
		}
		return envVars, nil
	}

	// Fallback: use static secret ref.
	return []corev1.EnvVar{
		{
			Name: "GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: run.Spec.GitCredentialsRef.Name,
					},
					Key: run.Spec.GitCredentialsRef.Key,
				},
			},
		},
	}, nil
}

func boolPtr(b bool) *bool    { return &b }
func int64Ptr(i int64) *int64 { return &i }

func intPtrToString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1alpha1.AgentRun{}).
		Owns(&corev1.Pod{}).
		Named("agentrun").
		Complete(r)
}
