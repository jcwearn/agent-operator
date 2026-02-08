# Agent Operator Helm Chart

A Kubernetes operator that orchestrates agentic coding workflows using Claude. This Helm chart provides an easy way to install and configure the agent-operator in your Kubernetes cluster.

## Prerequisites

- Kubernetes 1.20+
- Helm 3.0+
- An Anthropic API key
- A GitHub token with repo access

## Installation

### 1. Add the Helm repository (when published)

```bash
# This will be available once the chart is published
helm repo add agent-operator https://jcwearn.github.io/agent-operator
helm repo update
```

### 2. Create a namespace

```bash
kubectl create namespace agent-system
```

### 3. Create secrets

You need to provide an Anthropic API key and GitHub token:

```bash
kubectl create secret generic agent-secrets \
  --namespace agent-system \
  --from-literal=anthropic-api-key=sk-ant-... \
  --from-literal=github-token=ghp_...
```

### 4. Install the chart

```bash
helm install agent-operator ./charts/agent-operator \
  --namespace agent-system \
  --set secrets.name=agent-secrets
```

Or with a custom values file:

```bash
helm install agent-operator ./charts/agent-operator \
  --namespace agent-system \
  --values my-values.yaml
```

## Configuration

The following table lists the configurable parameters of the agent-operator chart and their default values.

### Operator Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `operatorImage.repository` | Operator container image repository | `ghcr.io/jcwearn/agent-operator` |
| `operatorImage.tag` | Operator container image tag | `Chart.appVersion` |
| `operatorImage.pullPolicy` | Image pull policy | `IfNotPresent` |
| `replicaCount` | Number of operator replicas | `1` |
| `resources.limits.cpu` | CPU limit for operator | `500m` |
| `resources.limits.memory` | Memory limit for operator | `128Mi` |
| `resources.requests.cpu` | CPU request for operator | `10m` |
| `resources.requests.memory` | Memory request for operator | `64Mi` |

### Agent Runner Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `agentRunnerImage.repository` | Agent runner container image repository | `ghcr.io/jcwearn/agent-runner` |
| `agentRunnerImage.tag` | Agent runner container image tag | `Chart.appVersion` |
| `agentRunnerImage.pullPolicy` | Image pull policy | `IfNotPresent` |
| `defaults.resources.cpu` | Default CPU limit for agent pods | `4` |
| `defaults.resources.memory` | Default memory limit for agent pods | `8Gi` |
| `defaults.stepTimeout` | Default timeout for each agent step | `30m` |
| `defaults.maxRetries` | Default max retries per step | `3` |

### RBAC Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rbac.create` | Create RBAC resources | `true` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `""` (generated) |
| `serviceAccount.annotations` | Service account annotations | `{}` |

### Secrets Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `secrets.name` | Name of the secret containing API keys | `agent-secrets` |
| `secrets.create` | Create the secret (not recommended for production) | `false` |

### Network Policy

| Parameter | Description | Default |
|-----------|-------------|---------|
| `networkPolicy.enabled` | Enable network policy for agent pods | `false` |
| `networkPolicy.egress` | Egress rules | HTTPS, SSH, DNS |

## Usage

After installing the chart, you can create a CodingTask:

```yaml
apiVersion: agents.wearn.dev/v1alpha1
kind: CodingTask
metadata:
  name: add-feature
  namespace: agent-system
spec:
  source:
    type: chat
    chat:
      sessionId: "my-session"
      userId: "username"
  repository:
    url: https://github.com/username/repo.git
    branch: main
  prompt: "Add a health check endpoint"
  anthropicApiKeyRef:
    name: agent-secrets
    key: anthropic-api-key
  gitCredentialsRef:
    name: agent-secrets
    key: github-token
```

Apply it:

```bash
kubectl apply -f codingtask.yaml
```

Watch the progress:

```bash
# Watch CodingTask status
kubectl get codingtasks -n agent-system -w

# Watch AgentRun status
kubectl get agentruns -n agent-system -w

# View logs
kubectl logs -n agent-system -l agents.wearn.dev/task=add-feature -f
```

## Upgrading

To upgrade the chart:

```bash
helm upgrade agent-operator ./charts/agent-operator \
  --namespace agent-system \
  --reuse-values
```

## Uninstalling

To uninstall the chart:

```bash
helm uninstall agent-operator --namespace agent-system
```

Note: This will not delete the CRDs. To delete them:

```bash
kubectl delete crd codingtasks.agents.wearn.dev
kubectl delete crd agentruns.agents.wearn.dev
```

## Example Values

### Minimal Configuration

```yaml
secrets:
  name: agent-secrets
```

### Production Configuration

```yaml
operatorImage:
  repository: ghcr.io/jcwearn/agent-operator
  tag: "0.1.0"

agentRunnerImage:
  repository: ghcr.io/jcwearn/agent-runner
  tag: "0.1.0"

replicaCount: 2

resources:
  limits:
    cpu: 1
    memory: 256Mi
  requests:
    cpu: 100m
    memory: 128Mi

defaults:
  resources:
    cpu: "8"
    memory: 16Gi
  stepTimeout: 45m
  maxRetries: 5

secrets:
  name: agent-secrets

networkPolicy:
  enabled: true

affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        labelSelector:
          matchExpressions:
          - key: app.kubernetes.io/name
            operator: In
            values:
            - agent-operator
        topologyKey: kubernetes.io/hostname
```

## Troubleshooting

### Operator not starting

Check the operator logs:

```bash
kubectl logs -n agent-system -l app.kubernetes.io/name=agent-operator
```

### Agent pods failing

Check the agent run status:

```bash
kubectl get agentruns -n agent-system
kubectl describe agentrun <name> -n agent-system
```

Check agent pod logs:

```bash
kubectl logs -n agent-system <pod-name>
```

### Permission issues

Ensure the secrets exist and have the correct keys:

```bash
kubectl get secret agent-secrets -n agent-system -o yaml
```

## Contributing

Please see the main repository for contribution guidelines.

## License

See the LICENSE file in the repository root.
