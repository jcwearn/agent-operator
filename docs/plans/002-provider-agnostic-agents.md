# Provider-Agnostic Coding Agents

> **Status:** Planning. Not yet started.
> **Prerequisite:** Builds on [001-agentic-coding-platform.md](./001-agentic-coding-platform.md) (Phases 1-3 complete).

## Context

The agent-operator is tightly coupled to Claude/Anthropic at every layer. The goal is to support Claude, OpenAI, and Gemini as first-class providers for both the agentic coding pipeline (CodingTask -> PR) and the chat proxy (`/v1/chat/completions`). Each provider should use its native CLI for best coding quality (Claude Code, Codex CLI, Gemini CLI), and the chat proxy should route to the correct backend based on model name.

**Key insight:** Claude Code CLI is the linchpin -- it provides the agentic reasoning loop, built-in tools (file read/write, bash, glob, grep), and git operations. Any approach must either replace or abstract around it.

**Separable concerns:** The chat proxy (`/v1/chat/completions` for Open WebUI) and the agentic coding pipeline are independent. Open WebUI already connects directly to OpenAI and Gemini APIs, so the chat proxy is lower priority.

## Approach: Provider CRD + Multi-Image + Multi-Provider Chat Proxy

**Agent runners:** Separate container images per provider, each using the native CLI (best quality per provider). The controller selects the right image based on `spec.provider.name`.

**Chat proxy:** Anthropic SDK for Claude models, `go-openai` SDK for OpenAI + Gemini (both support OpenAI-compatible API format). Tool-use (task CRUD) works across all providers.

**Rollout:** 4 incremental phases, each independently shippable.

## Coupling Inventory (7 surfaces)

1. **CRD types** -- `AnthropicAPIKeyRef`, `ModelConfig` defaults to "sonnet"
2. **Agent-runner Dockerfile** -- installs `@anthropic-ai/claude-code` via npm
3. **Agent-runner entrypoint.sh** -- invokes `claude --print`, requires `ANTHROPIC_API_KEY`
4. **AgentRun controller** -- injects `ANTHROPIC_API_KEY` env var into pods
5. **CodingTask controller** -- `modelForStep()` defaults to "sonnet", passes `AnthropicAPIKeyRef`
6. **GitHub notifier** -- hardcoded model names "Sonnet 4.5", "Opus 4", "Haiku 4.5"
7. **Chat proxy + Anthropic package** -- Anthropic SDK for `/v1/chat/completions`

---

## Phase A: CRD + Controller Generalization (v0.6.0)

Decouple the CRD and controller from Anthropic-specific types. Claude remains the only provider, but the abstraction is in place.

### A1. CRD Changes (`api/v1alpha1/codingtask_types.go`)
- Add `ProviderSpec` struct:
  ```go
  type ProviderSpec struct {
      // +kubebuilder:validation:Enum=claude;openai;gemini
      // +kubebuilder:default=claude
      Name string `json:"name"`
      APIKeyRef SecretReference `json:"apiKeyRef"`
  }
  ```
- Add `spec.provider` field to `CodingTaskSpec` (optional, defaults to claude)
- Deprecate `spec.anthropicApiKeyRef` -- keep it functional with defaulting logic:
  - If `spec.provider` is set, use it
  - If only `spec.anthropicApiKeyRef` is set, auto-populate `provider: {name: claude, apiKeyRef: <same ref>}`
- Change `ModelConfig.Default` from hardcoded `"sonnet"` to empty string (controller resolves per provider)

### A2. CRD Changes (`api/v1alpha1/agentrun_types.go`)
- Add `Provider string` and generic `APIKeyRef SecretReference` fields to `AgentRunSpec`
- Deprecate `AnthropicAPIKeyRef` with same backward-compat logic

### A3. New provider package (`internal/provider/`)
- `provider.go` -- `Provider` interface and registry:
  ```go
  type Provider interface {
      Name() string
      DefaultImage() string
      DefaultModel() string
      APIKeyEnvVar() string          // e.g. "ANTHROPIC_API_KEY", "OPENAI_API_KEY"
      AvailableModels() []ModelInfo  // for GitHub UI + /v1/models
  }
  ```
- `claude.go` -- Claude provider (image: `agent-runner:tag`, default model: `sonnet`, env: `ANTHROPIC_API_KEY`, models: Sonnet/Opus/Haiku)
- `openai.go` -- OpenAI provider (stub for Phase B)
- `gemini.go` -- Gemini provider (stub for Phase C)
- `registry.go` -- `Get(name string) Provider` lookup

### A4. Controller updates
- `codingtask_controller.go`:
  - `modelForStep()` -- resolve default model from provider instead of hardcoded "sonnet"
  - `createAgentRun()` -- populate `Provider` + `APIKeyRef` from `spec.provider` (with backward-compat fallback to `anthropicApiKeyRef`)
- `agentrun_controller.go`:
  - `createPod()` -- use `provider.APIKeyEnvVar()` for the env var name, `provider.DefaultImage()` for image fallback

### A5. GitHub notifier (`internal/github/notifier.go`)
- Model selection comment generated dynamically from `provider.AvailableModels()` instead of hardcoded Claude models

### A6. CRD YAMLs + k3s-cluster manifests
- Regenerate CRDs via `make manifests`
- Update `infrastructure/agent-operator/crd-codingtasks.yaml` and `crd-agentruns.yaml`
- No deployment.yaml changes needed (Claude is default)

### Files modified:
- `api/v1alpha1/codingtask_types.go`
- `api/v1alpha1/agentrun_types.go`
- `internal/controller/codingtask_controller.go`
- `internal/controller/agentrun_controller.go`
- `internal/github/notifier.go`
- `cmd/main.go` (pass provider registry to controllers)
- **New:** `internal/provider/provider.go`, `claude.go`, `openai.go`, `gemini.go`, `registry.go`

---

## Phase B: OpenAI Agent Runner (v0.7.0)

Add an OpenAI/Codex CLI agent-runner image. CodingTasks can now specify `provider.name: openai`.

### B1. New agent-runner-codex image
- `agent-runner/Dockerfile.codex` -- Ubuntu base + Node.js + `@openai/codex` (Codex CLI)
- `agent-runner/entrypoint-codex.sh` -- same env var contract (`AGENT_STEP`, `AGENT_PROMPT`, etc.), same output markers, invokes `codex --quiet --full-auto` with appropriate flags
- Handles the 4 steps (plan, implement, test, pull-request) with Codex CLI equivalents of Claude Code's `--print` mode

### B2. Provider implementation (`internal/provider/openai.go`)
- Fill in stub: image `ghcr.io/jcwearn/agent-runner-codex:tag`, default model `gpt-4.1`, env var `OPENAI_API_KEY`
- Available models: `gpt-4.1`, `o3`, `o4-mini`, etc.

### B3. GitHub Actions CI
- New workflow to build and push `ghcr.io/jcwearn/agent-runner-codex` image

### B4. k3s-cluster secrets
- Add `openai-api-key` SOPS-encrypted secret to `infrastructure/agent-operator/secrets.sops.yaml`

### Files modified:
- `internal/provider/openai.go`
- **New:** `agent-runner/Dockerfile.codex`, `agent-runner/entrypoint-codex.sh`
- **New:** `.github/workflows/build-agent-runner-codex.yaml`

---

## Phase C: Gemini Agent Runner (v0.8.0)

Add a Gemini CLI agent-runner image. CodingTasks can now specify `provider.name: gemini`.

### C1. New agent-runner-gemini image
- `agent-runner/Dockerfile.gemini` -- Ubuntu base + Node.js + Gemini CLI (or use Aider with Gemini models as a pragmatic alternative if Gemini CLI is immature)
- `agent-runner/entrypoint-gemini.sh` -- same contract, invokes Gemini CLI

### C2. Provider implementation (`internal/provider/gemini.go`)
- Fill in stub: image, default model `gemini-2.5-pro`, env var `GEMINI_API_KEY`
- Available models: `gemini-2.5-pro`, `gemini-2.5-flash`, etc.

### C3. GitHub Actions CI + k3s-cluster secrets
- Build workflow for `ghcr.io/jcwearn/agent-runner-gemini`
- Add `gemini-api-key` SOPS-encrypted secret

### Files modified:
- `internal/provider/gemini.go`
- **New:** `agent-runner/Dockerfile.gemini`, `agent-runner/entrypoint-gemini.sh`
- **New:** `.github/workflows/build-agent-runner-gemini.yaml`

---

## Phase D: Multi-Provider Chat Proxy (v0.9.0)

Make the operator's `/v1/chat/completions` endpoint route to the correct LLM backend based on model name. Tool-use (task CRUD) works with all providers.

### D1. New OpenAI client package (`internal/openaicompat/`)
- Wraps `github.com/sashabaranov/go-openai` SDK
- `client.go` -- `Chat()` and `ChatStream()` methods, configured with base URL + API key
- Used for both OpenAI (`api.openai.com`) and Gemini (`generativelanguage.googleapis.com/v1beta/openai`)
- Tool-use support: OpenAI and Gemini both support function calling in the OpenAI format

### D2. Chat router (`internal/server/chat_router.go`)
- New `ChatRouter` that maps model prefixes to backends:
  - `claude-*` -> Anthropic SDK (existing `internal/anthropic/` client)
  - `gpt-*`, `o1-*`, `o3-*`, `o4-*` -> OpenAI via `internal/openaicompat/` client
  - `gemini-*` -> Gemini via `internal/openaicompat/` client (different base URL)
- Implements a unified `Chat(model, messages, tools)` interface
- Tool execution loop works identically regardless of backend (tool definitions are already OpenAI-format compatible)

### D3. Refactor handlers_openai.go
- Replace direct Anthropic SDK usage with `ChatRouter`
- `handleNonStreamingChat()` and `handleStreamingChat()` use router instead of Anthropic client directly
- System prompt made generic (remove "You are Claude" reference)
- Keep existing tool definitions (create/list/get/approve coding task) -- they work with any provider's function calling

### D4. Update `/v1/models` endpoint
- `internal/anthropic/types.go` `NewModelsResponse()` replaced with dynamic model list from all provider registrations
- Returns Claude + OpenAI + Gemini models

### D5. Server wiring (`internal/server/server.go`, `cmd/main.go`)
- `APIServer` gets a `chatRouter` field instead of `anthropicClient`
- `cmd/main.go` initializes clients for each configured provider (based on which API key secrets exist)
- Providers with no configured API key are skipped gracefully

### D6. k3s-cluster Open WebUI config
- Update `apps/open-webui/helm.yaml` `OPENAI_API_BASE_URLS` -- can potentially simplify to just the operator endpoint (since it now handles all providers) or keep separate direct connections
- Update `DEFAULT_MODELS` if desired

### Files modified:
- `internal/server/handlers_openai.go` (major refactor)
- `internal/server/server.go`
- `internal/anthropic/types.go` (dynamic models list)
- `cmd/main.go`
- **New:** `internal/openaicompat/client.go`
- **New:** `internal/server/chat_router.go`

---

## Backward Compatibility Strategy

- `spec.anthropicApiKeyRef` remains functional throughout all phases via defaulting webhook/logic
- Existing CodingTask resources with no `spec.provider` default to `{name: claude, apiKeyRef: <anthropicApiKeyRef>}`
- Old-style model names ("sonnet", "opus", "haiku") continue to work for Claude provider
- New providers use fully-qualified model names (e.g., "gpt-4.1", "gemini-2.5-pro")

## Verification

- **Phase A:** `make test`, `make manifests`, deploy v0.6.0, create a CodingTask with both old (`anthropicApiKeyRef`) and new (`provider`) syntax -- both should work
- **Phase B:** Build codex image, create CodingTask with `provider.name: openai`, verify plan/implement/test/PR workflow
- **Phase C:** Same for Gemini
- **Phase D:** Chat via Open WebUI with each model prefix, verify tool-use works across all providers, verify `/v1/models` returns all available models
