# agent-operator

A Kubernetes operator that orchestrates agentic coding workflows using Claude. Given a task prompt and a target repository, it runs a multi-step pipeline — plan, implement, test, and open a pull request — entirely within sandboxed pods on your cluster.

## How It Works

The operator defines two Custom Resource Definitions:

- **CodingTask** — A complete coding workflow. You provide a prompt, a repo URL, and credentials. The operator drives it through: `Pending → Planning → Implementing → Testing → PullRequest → Complete`.
- **AgentRun** — A single step in that workflow. The CodingTask controller creates these automatically. Each AgentRun spawns an ephemeral pod that runs Claude Code against the repo.

The workflow:

```
CodingTask created
  └→ Planning AgentRun → pod analyzes repo, outputs a plan
  └→ Implementing AgentRun → pod implements the plan, commits to a work branch
  └→ Testing AgentRun → pod runs test suite
      ├→ pass: continue
      └→ fail: retry implementation (up to maxRetries)
  └→ PullRequest AgentRun → pod pushes branch, opens PR
  └→ CodingTask status → Complete (with PR URL)
```

If any step fails beyond the retry limit, the task moves to `Failed` with a message explaining what went wrong.

## Repository Structure

```
.
├── api/v1alpha1/               # CRD type definitions
│   ├── codingtask_types.go     # CodingTask spec, status, enums
│   ├── agentrun_types.go       # AgentRun spec, status, enums
│   └── groupversion_info.go    # API group registration (agents.wearn.dev/v1alpha1)
├── internal/controller/        # Reconciliation logic
│   ├── codingtask_controller.go    # Workflow state machine, creates AgentRuns
│   └── agentrun_controller.go      # Pod lifecycle, timeout enforcement, output capture
├── cmd/main.go                 # Operator entrypoint
├── agent-runner/               # Container image for agent pods
│   ├── Dockerfile              # Ubuntu 24.04 + Node.js + Claude Code CLI + gh
│   └── entrypoint.sh           # Step-aware entrypoint (plan/implement/test/pr)
├── config/                     # Kubebuilder-generated Kubernetes manifests
│   ├── crd/bases/              # Generated CRD YAML
│   ├── rbac/                   # RBAC roles and bindings
│   ├── manager/                # Operator deployment
│   ├── samples/                # Example CodingTask and AgentRun resources
│   └── default/                # Kustomize overlay combining everything
├── Dockerfile                  # Operator container image
├── Makefile                    # Build, test, generate, deploy targets
└── .github/workflows/          # CI/CD
    ├── build-images.yml        # Build + push operator and agent-runner to GHCR
    ├── lint.yml                # golangci-lint
    ├── test.yml                # Unit tests (envtest)
    └── test-e2e.yml            # End-to-end tests
```

## Built With

- **[Kubebuilder](https://kubebuilder.io/)** (v4.11.1) — Scaffolding, code generation, CRD generation
- **Go** (1.25) with **controller-runtime** (v0.23) — Operator framework
- **[Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code)** — AI coding agent inside each pod
- **GitHub CLI (`gh`)** — PR creation and git operations in agent pods

## Prerequisites

- A Kubernetes cluster (tested on k3s)
- An Anthropic API key (stored as a Kubernetes Secret)
- A GitHub token with repo access (stored as a Kubernetes Secret)
- Container images pushed to a registry accessible from the cluster

## Quick Start

### 1. Install CRDs

```bash
make manifests
kubectl apply -f config/crd/bases/
```

### 2. Create secrets

```bash
kubectl create namespace agent-system

kubectl create secret generic agent-secrets \
  --namespace agent-system \
  --from-literal=anthropic-api-key=sk-ant-... \
  --from-literal=github-token=ghp_...
```

### 3. Create a ServiceAccount for agent pods

```bash
kubectl create serviceaccount agent-runner --namespace agent-system
```

### 4. Run the operator

For local development (runs outside the cluster, connects via kubeconfig):

```bash
make run
```

Or deploy to the cluster:

```bash
make docker-build docker-push IMG=ghcr.io/jcwearn/agent-operator:latest
make deploy IMG=ghcr.io/jcwearn/agent-operator:latest
```

### 5. Create a CodingTask

```yaml
apiVersion: agents.wearn.dev/v1alpha1
kind: CodingTask
metadata:
  name: add-healthcheck
  namespace: agent-system
spec:
  source:
    type: chat
    chat:
      sessionId: "manual-test"
      userId: "jackson"
  repository:
    url: https://github.com/jcwearn/example-repo.git
    branch: main
  prompt: "Add a /healthz endpoint that returns 200 OK"
  anthropicApiKeyRef:
    name: agent-secrets
    key: anthropic-api-key
  gitCredentialsRef:
    name: agent-secrets
    key: github-token
```

```bash
kubectl apply -f codingtask.yaml
```

### 6. Watch progress

```bash
# Task-level status
kubectl get codingtasks -n agent-system -w

# Individual step runs
kubectl get agentruns -n agent-system -w

# Agent pod logs
kubectl logs -n agent-system -l agents.wearn.dev/task=add-healthcheck -f
```

## CRD Reference

### CodingTask

| Field | Description | Default |
|---|---|---|
| `spec.source` | Where the task came from (`github-issue` or `chat`) | required |
| `spec.repository.url` | Git clone URL | required |
| `spec.repository.branch` | Base branch | `main` |
| `spec.repository.workBranch` | Branch for agent changes | `ai/<task-name>` |
| `spec.prompt` | Task instructions | required |
| `spec.agentImage` | Container image for agent pods | `ghcr.io/jcwearn/agent-runner:latest` |
| `spec.resources.cpu` | CPU limit per agent pod | `4` |
| `spec.resources.memory` | Memory limit per agent pod | `8Gi` |
| `spec.anthropicApiKeyRef` | Secret reference for Anthropic API key | required |
| `spec.gitCredentialsRef` | Secret reference for GitHub token | required |
| `spec.maxRetries` | Max retries per step | `3` |
| `spec.stepTimeout` | Timeout per step | `30m` |

### AgentRun

Created automatically by the CodingTask controller. Each run corresponds to one step (`plan`, `implement`, `test`, or `pull-request`) and spawns a single pod.

Key status fields: `phase` (Pending/Running/Succeeded/Failed), `podName`, `output`, `exitCode`.

## Development

```bash
# Generate CRD manifests and deepcopy after changing types
make generate manifests

# Run unit tests (uses envtest)
make test

# Run linter
make lint

# Build the operator binary
make build
```

## Agent Runner Image

The `agent-runner/` directory contains the Docker image that runs inside each agent pod. It includes:

- **Ubuntu 24.04** base
- **Node.js 22** + **Claude Code CLI**
- **GitHub CLI** (`gh`)
- **Git** with credential helper

The entrypoint script (`entrypoint.sh`) reads environment variables set by the operator to determine what step to run, which repo to clone, and what prompt to send to Claude. It writes output to the pod's termination message so the operator can capture results.

## Pod Security

Agent pods run with:

- `runAsNonRoot: true` (UID 1000)
- `seccompProfile: RuntimeDefault`
- `allowPrivilegeEscalation: false`
- All capabilities dropped
- Network policy restricting egress to HTTPS (443), SSH (22), and DNS only
