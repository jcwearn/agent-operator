# Provider-Agnostic Coding Agents

> **Status:** Phase 1 complete. Ollama + Aider is the first new provider.
> **Prerequisite:** Builds on [001-agentic-coding-platform.md](./001-agentic-coding-platform.md) (Phases 1-3 complete).

## Context

The agent-operator was tightly coupled to Claude/Anthropic at every layer. The goal is to support multiple LLM providers for both the agentic coding pipeline (CodingTask -> PR) and the chat proxy (`/v1/chat/completions`).

**Approach:** Ollama with local models (qwen2.5) is the first new provider, using Aider as the agent framework. This allows experimentation with local models at zero API cost. Claude remains the default provider with full backward compatibility.

## Coupling Inventory (7 surfaces, all addressed)

1. **CRD types** -- `ProviderSpec` added alongside deprecated `AnthropicAPIKeyRef`
2. **Agent-runner image** -- Separate image per framework (Claude Code vs Aider)
3. **Agent-runner entrypoint** -- Each framework has its own entrypoint with the same env var contract
4. **AgentRun controller** -- Uses provider registry for env var injection
5. **CodingTask controller** -- Uses provider registry for image/model defaults
6. **GitHub notifier** -- Dynamic model selection from provider registry
7. **Chat proxy** -- Routes by model name to correct backend (Anthropic SDK vs Ollama)

---

## Phase 1: Ollama + Aider (v0.6.0) -- COMPLETE

### 1A. CRD + Provider Interface

**CRD changes:**
- Added `ProviderType` enum (`claude`, `ollama`, `openai`, `gemini`) to `CodingTaskSpec`
- Added `ProviderSpec` struct with `Name`, `APIKeyRef *SecretReference`, `BaseURL string`
- Added `Provider *ProviderSpec` to `CodingTaskSpec` (optional, defaults to claude)
- Added `Provider string`, `APIKeyRef *SecretReference`, `BaseURL string` to `AgentRunSpec`
- `AnthropicAPIKeyRef` kept as deprecated-but-functional on both CRDs

**Provider package (`internal/provider/`):**
- `Provider` interface: `Name()`, `DefaultImage()`, `DefaultModel()`, `DefaultModelForStep()`, `APIKeyEnvVar()`, `APIKeyRequired()`, `DefaultBaseURL()`, `AvailableModels()`, `PodEnvVars()`
- `Claude` provider: image `ghcr.io/jcwearn/agent-runner`, model "sonnet", env `ANTHROPIC_API_KEY`
- `Ollama` provider: image `ghcr.io/jcwearn/agent-runner-aider`, model "qwen2.5:7b", env `OPENAI_API_BASE` + `OPENAI_API_KEY=ollama`
- `Registry` with `Get()`, `MustGet()`, `All()`, `AllModels()`, `WithOllamaBaseURL()` option

**Controller updates:**
- `CodingTaskReconciler`: `resolveProvider()` helper, updated `agentImage()` and `modelForStep()` to use provider registry
- `AgentRunReconciler`: `buildProviderEnvVars()` method with backward compat fallback to `AnthropicAPIKeyRef`

**Notifier update:**
- Dynamic model selection comment from provider registry
- `parseModelSelections()` builds display-name-to-ID map dynamically

**Files modified:** `api/v1alpha1/codingtask_types.go`, `api/v1alpha1/agentrun_types.go`, `internal/controller/codingtask_controller.go`, `internal/controller/agentrun_controller.go`, `internal/github/notifier.go`, `cmd/main.go`
**Files created:** `internal/provider/provider.go`, `internal/provider/claude.go`, `internal/provider/ollama.go`, `internal/provider/registry.go`

### 1B. Aider Agent Runner Image

New container image using Aider instead of Claude Code, same entrypoint contract.

- `agent-runner-aider/Dockerfile`: Ubuntu 24.04 + Python 3 + Aider (`pip install aider-chat`) + GitHub CLI, same UID/GID (1000:1000), same directory layout
- `agent-runner-aider/entrypoint.sh`: Same env var contract (`AGENT_STEP`, `AGENT_REPO_URL`, etc.), uses `OPENAI_API_BASE` + `OPENAI_API_KEY` instead of `ANTHROPIC_API_KEY`, invokes `aider --message --model openai/$MODEL --yes-always --no-auto-commits`
- CI: Added `build-agent-runner-aider` job to `.github/workflows/release.yml`, pushes to `ghcr.io/jcwearn/agent-runner-aider`

### 1C. Chat Proxy Multi-Provider Routing

Routes `/v1/chat/completions` to the correct backend based on model name.

- `internal/openaicompat/client.go`: Lightweight HTTP client for OpenAI-compatible endpoints (Ollama's `/v1/chat/completions`), supports both streaming and non-streaming
- `internal/server/chat_router.go`: Routes by model ID -- Ollama model IDs go to Ollama backend, everything else to Claude
- `handlers_openai.go`: Refactored `handleChatCompletions()` to route via `chatRouter`, extracted `handleClaudeChat()` and added `handleOllamaChat()` with streaming/non-streaming variants
- `internal/anthropic/types.go`: Added `NewModelsResponseFromProviders()` for dynamic `/v1/models` from provider registry
- `internal/server/server.go`: Added `ollamaClient`, `providerRegistry`, `chatRouter` fields with `WithOllamaClient()` and `WithProviderRegistry()` options

### 1D. k3s-cluster Manifests

- `infrastructure/agent-operator/deployment.yaml`: Added `OLLAMA_BASE_URL` env var pointing to `http://ollama.ollama.svc.cluster.local:11434`
- `infrastructure/agent-operator/networkpolicy.yaml`: Added egress rule for port 11434 to ollama namespace
- Updated CRD YAMLs (`crd-agentruns.yaml`, `crd-codingtasks.yaml`) from `make manifests`

---

## Phase 2: OpenAI / Codex CLI (planned)

Add OpenAI as a provider using Codex CLI as the agent framework.

- New `agent-runner-codex` image with `@openai/codex` CLI
- `internal/provider/openai.go` provider implementation
- Chat proxy routing for `gpt-*`, `o3-*`, `o4-*` model prefixes
- k3s-cluster: `openai-api-key` SOPS-encrypted secret

## Phase 3: Gemini CLI (planned)

Add Gemini as a provider using Gemini CLI (or Aider with Gemini models).

- New `agent-runner-gemini` image
- `internal/provider/gemini.go` provider implementation
- Chat proxy routing for `gemini-*` model prefixes
- k3s-cluster: `gemini-api-key` SOPS-encrypted secret

---

## Backward Compatibility

- `spec.anthropicApiKeyRef` remains functional via controller defaulting logic
- Existing CodingTasks with no `spec.provider` default to claude
- Old-style model names ("sonnet", "opus", "haiku") continue to work
- New providers use their native model names (e.g., "qwen2.5:7b", "gpt-4.1")

## Verification

1. **Unit tests**: `make test` -- tests pass for both old-style and new-style CodingTasks
2. **Build images**: `docker build` the Aider runner, verify it works with Ollama
3. **Deploy**: Push to k3s-cluster via FluxCD, update operator image tag
4. **Agentic pipeline**: Create CodingTask with `provider.name: ollama`, verify Aider pod reaches Ollama
5. **Chat proxy**: Send request to `/v1/chat/completions` with model `qwen2.5:7b`, verify routing to Ollama
6. **Backward compat**: Existing CodingTasks with `anthropicApiKeyRef` work unchanged
