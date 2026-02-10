# Agentic Coding Platform - Comprehensive Architecture Plan

> **Status:** Phases 1-3 COMPLETE. Phase 4+ pending.

## Summary

A Kubernetes-native agentic coding platform running on the k3s cluster. A custom Go operator orchestrates multi-step coding workflows (plan -> implement -> test -> PR) triggered from Open WebUI or GitHub issues. Claude handles all LLM tasks. Agent work runs in isolated, ephemeral pods.

## Key Decisions

- **Chat UI:** Open WebUI (with OpenAI-compatible API backend)
- **Agent Runtime:** Custom K8s operator in Go (Kubebuilder)
- **LLM Backend:** Claude only (Anthropic API) - see [002-provider-agnostic-agents.md](./002-provider-agnostic-agents.md) for multi-provider plan
- **GitHub Trigger:** Label-triggered (`ai-task` label on issues)
- **Operator Source:** Separate repo (`agent-operator`)
- **Ephemeral Test Envs:** For external projects (future phase)

## Architecture Overview

```
+-------------------+     +--------------------+
|   Open WebUI      |     |  GitHub Webhooks   |
|  (Chat + Tasks)   |     |  (Issue Labels)    |
+--------+----------+     +--------+-----------+
         |                         |
         v                         v
+--------------------------------------------+
|           Agent API Server                 |
|  (REST + WebSocket + GitHub Webhook)       |
+--------------------+-----------------------+
                     | creates/watches
                     v
+--------------------------------------------+
|        Agent Operator (Go)                 |
|  +--------------------------------------+  |
|  |  CRDs:                               |  |
|  |  - CodingTask                        |  |
|  |  - AgentRun                          |  |
|  +--------------------------------------+  |
|  Controller: manages pod lifecycle,        |
|  step transitions, status reporting        |
+--------------------+-----------------------+
                     | spawns
                     v
+--------------------------------------------+
|         Agent Pods (Ephemeral)             |
|  +----------+ +----------+ +----------+   |
|  | Planner  |>|Implement |>| PR Agent |   |
|  |  Agent   | | + Test   | |          |   |
|  +----------+ +----------+ +----------+   |
|  Each pod: sandboxed, git creds,           |
|  Claude API, repo clone, tools             |
+--------------------------------------------+
```

## CRDs

### CodingTask - Top-level task representation

```yaml
apiVersion: agents.wearn.dev/v1alpha1
kind: CodingTask
metadata:
  name: fix-auth-bug-123
  namespace: agent-system
spec:
  source:
    type: github-issue | chat
    github:
      owner: jcwearn
      repo: some-project
      issueNumber: 123
    chat:
      sessionId: "abc-123"
      userId: "jackson"
  repository:
    url: https://github.com/jcwearn/some-project.git
    branch: main
    workBranch: ai/fix-auth-bug-123
  prompt: "Fix the authentication bug where users can't log in after password reset"
  resources:
    cpu: "4"
    memory: "8Gi"
    storage: "20Gi"
status:
  phase: Pending | Planning | Implementing | Testing | PullRequest | Complete | Failed
  plan: "..."
  planApproved: false
  currentStep: 2
  totalSteps: 4
  agentRuns: [...]
  pullRequest:
    url: "https://github.com/..."
    number: 456
  conditions: [...]
```

### AgentRun - Individual agent execution step

```yaml
apiVersion: agents.wearn.dev/v1alpha1
kind: AgentRun
metadata:
  name: fix-auth-bug-123-plan
  namespace: agent-system
  ownerReferences:
    - kind: CodingTask
spec:
  taskRef: fix-auth-bug-123
  step: plan | implement | test | pull-request
  prompt: "..."
  image: ghcr.io/jcwearn/agent-runner:latest
  timeout: 30m
status:
  phase: Running | Succeeded | Failed
  podName: fix-auth-bug-123-plan-xyz
  startedAt: ...
  completedAt: ...
  output: "..."
  logs: "..."
```

## API Server Endpoints

```
POST   /api/v1/tasks              # Create a CodingTask from chat
GET    /api/v1/tasks              # List tasks (with filtering)
GET    /api/v1/tasks/:id          # Get task details + status
GET    /api/v1/tasks/:id/logs     # Stream logs (WebSocket)
DELETE /api/v1/tasks/:id          # Cancel a task
POST   /api/v1/tasks/:id/approve  # Approve a plan
POST   /api/v1/webhooks/github    # GitHub webhook receiver
GET    /api/v1/ws                 # WebSocket for real-time updates
```

## Pod Security

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 1000
  fsGroup: 1000
  seccompProfile:
    type: RuntimeDefault
containerSecurityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: false
  capabilities:
    drop: ["ALL"]
```

Network policies restrict agent pods to only reach GitHub API, Anthropic API, and internal DNS.

## Cluster Deployment (k3s-cluster repo)

```
infrastructure/
  └── agent-operator/
      ├── namespace.yaml
      ├── deployment.yaml
      ├── crds/
      ├── rbac.yaml
      ├── networkpolicy.yaml
      ├── secrets.sops.yaml
      └── kustomization.yaml

apps/
  └── open-webui/
      ├── namespace.yaml
      ├── helm.yaml
      ├── pvc.yaml
      ├── secrets.sops.yaml
      └── kustomization.yaml
```

## Implementation Phases

### Phase 1: Foundation (Operator + CRDs) -- COMPLETE
- Scaffold Go operator with Kubebuilder in new `agent-operator` repo
- Define CodingTask and AgentRun CRDs
- Implement CodingTask controller (state machine)
- Implement AgentRun controller (pod lifecycle)
- Build the agent runner Docker image with Claude Code CLI
- Deploy operator to cluster via FluxCD
- **Milestone:** Can manually create a CodingTask CR via kubectl and watch full flow execute

### Phase 2: API Server + GitHub Integration -- COMPLETE
- Build API server with REST + WebSocket endpoints
- Implement GitHub webhook handler (label-triggered)
- Create GitHub App and install on target repos
- Wire up GitHub issue -> CodingTask CR creation
- Add issue commenting (plan, status updates, PR link)
- **Milestone:** Adding `ai-task` label to a GitHub issue triggers full workflow and produces a PR

### Phase 3: Open WebUI + Chat Interface -- COMPLETE
- Deploy Open WebUI to cluster
- Implement OpenAI-compatible endpoint in API server
- Configure Open WebUI to use API server as backend
- Add real-time task status streaming via WebSocket
- **Milestone:** Can chat with Claude via Open WebUI and trigger coding tasks

### Phase 4: Helm Chart + Deployment Automation
- Package the operator as a Helm chart (CRDs, RBAC, Deployment, NetworkPolicy, ServiceAccount)
- Host the chart as an OCI artifact on GHCR (`ghcr.io/jcwearn/charts/agent-operator`)
- GitHub Action in operator repo: on release tag, build chart and push to GHCR
- Replace static manifests in k3s-cluster repo with HelmRepository + HelmRelease (matching existing infra pattern)
- CRD updates flow automatically: operator release -> chart published -> Flux reconciles HelmRelease -> CRDs + operator updated
- **Milestone:** Operator versioned and deployed via Helm like all other infrastructure components

### Phase 5: Polish + Reliability
- Prometheus metrics for operator
- Grafana dashboard
- Retry logic tuning
- Log aggregation
- ntfy notifications for task completion/failure

### Phase 6: Ephemeral Test Environments (Future)
- Preview environment controller
- Namespace-per-PR with auto-cleanup
- Ingress generation (pr-{n}.preview.wearn.dev)
- PR comment with preview URL

## GitHub Issue -> PR Workflow

1. Developer creates GitHub issue describing the task
2. Developer adds label "ai-task"
3. GitHub sends webhook to API server
4. API server creates CodingTask CR
5. Operator spawns Planning agent pod -> generates plan
6. Plan posted as issue comment
7. Implementation agent implements changes + writes tests
8. Test agent runs tests (retry up to 3x on failure)
9. PR agent pushes branch, creates PR linked to issue
10. Comments on original issue with PR link
11. CodingTask status -> Complete
