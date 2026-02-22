package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
	gogithub "github.com/google/go-github/v83/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
	anthropicpkg "github.com/jcwearn/agent-operator/internal/anthropic"
)

const (
	defaultMaxTokens  int64 = 4096
	maxToolUseRounds        = 10
	finishReasonStop        = "stop"
	defaultBranchName       = "main"
)

const claudeSystemPrompt = `You are Claude, an AI assistant integrated with an agentic coding platform. You can help users with questions and also create coding tasks that will be executed by autonomous coding agents.

When a user asks you to make changes to a codebase, fix a bug, implement a feature, or do any coding work, use the create_coding_task tool. This will create a CodingTask resource and a GitHub issue to track the work.

You can also list, inspect, and approve existing coding tasks using the available tools. Tasks can originate from either chat (you creating them) or from GitHub issues labeled "ai-task" — both are fully visible and manageable.

When creating tasks, write a clear, detailed prompt that describes what the coding agent should do. Include relevant context, file paths, and acceptance criteria when possible.`

const ollamaSystemPrompt = `You are an AI assistant integrated with an agentic coding platform. You can help users with questions about code, software engineering, and general topics.`

// handleListModels returns the list of available models from all providers.
func (s *APIServer) handleListModels(w http.ResponseWriter, _ *http.Request) {
	if s.providerRegistry != nil {
		respondJSON(w, http.StatusOK, anthropicpkg.NewModelsResponseFromProviders(s.providerRegistry))
	} else {
		respondJSON(w, http.StatusOK, anthropicpkg.NewModelsResponse())
	}
}

// handleChatCompletions handles OpenAI-compatible chat completion requests.
// Routes to the appropriate backend based on the requested model.
func (s *APIServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req anthropicpkg.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if len(req.Messages) == 0 {
		respondError(w, http.StatusBadRequest, "messages are required")
		return
	}

	// Route based on model name.
	if s.chatRouter != nil && s.chatRouter.route(req.Model) == backendOllama {
		s.handleOllamaChat(w, r, req)
		return
	}

	s.handleClaudeChat(w, r, req)
}

// handleClaudeChat handles chat completions via the Anthropic API.
func (s *APIServer) handleClaudeChat(w http.ResponseWriter, r *http.Request, req anthropicpkg.ChatCompletionRequest) {
	if s.anthropicClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Anthropic client not configured")
		return
	}

	maxTokens := defaultMaxTokens
	if req.MaxTokens > 0 {
		maxTokens = int64(req.MaxTokens)
	}

	systemBlocks, messages := anthropicpkg.TranslateMessages(req.Messages)

	// Prepend our system prompt with cache control to reduce repeated input token costs.
	systemBlocks = append([]sdkanthropic.TextBlockParam{
		{
			Text:         claudeSystemPrompt,
			CacheControl: sdkanthropic.CacheControlEphemeralParam{Type: "ephemeral"},
		},
	}, systemBlocks...)

	tools := s.buildTools()

	params := sdkanthropic.MessageNewParams{
		Model:     anthropicpkg.MapModel(req.Model),
		MaxTokens: maxTokens,
		Messages:  messages,
		System:    systemBlocks,
		Tools:     tools,
	}

	if req.Stream {
		s.handleStreamingChat(w, r, params, req.Model)
	} else {
		s.handleNonStreamingChat(w, r, params, req.Model)
	}
}

// handleOllamaChat handles chat completions via an OpenAI-compatible endpoint (Ollama).
func (s *APIServer) handleOllamaChat(w http.ResponseWriter, r *http.Request, req anthropicpkg.ChatCompletionRequest) {
	if s.ollamaClient == nil {
		respondError(w, http.StatusServiceUnavailable, "Ollama client not configured")
		return
	}

	// Prepend system prompt.
	req.Messages = append([]anthropicpkg.ChatMessage{
		{Role: "system", Content: ollamaSystemPrompt},
	}, req.Messages...)

	if req.Stream {
		s.handleOllamaStreamingChat(w, r, req)
	} else {
		s.handleOllamaNonStreamingChat(w, r, req)
	}
}

// handleOllamaNonStreamingChat proxies a non-streaming request to Ollama.
func (s *APIServer) handleOllamaNonStreamingChat(w http.ResponseWriter, r *http.Request, req anthropicpkg.ChatCompletionRequest) {
	resp, err := s.ollamaClient.Chat(r.Context(), req)
	if err != nil {
		s.log.Error(err, "ollama chat error")
		respondError(w, http.StatusBadGateway, "failed to get response from Ollama")
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

// handleOllamaStreamingChat proxies a streaming request to Ollama.
func (s *APIServer) handleOllamaStreamingChat(w http.ResponseWriter, r *http.Request, req anthropicpkg.ChatCompletionRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	events, err := s.ollamaClient.ChatStream(r.Context(), req)
	if err != nil {
		s.log.Error(err, "ollama stream error")
		respondError(w, http.StatusBadGateway, "failed to stream from Ollama")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for event := range events {
		if event.Err != nil {
			s.log.Error(event.Err, "ollama stream event error")
			break
		}
		if event.Done {
			_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()
			break
		}
		if event.Chunk != nil {
			data, _ := json.Marshal(event.Chunk)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleNonStreamingChat processes a non-streaming chat completion request.
// It runs an agentic tool-use loop: if the model returns tool_use blocks,
// we execute them and continue the conversation until a final text response.
func (s *APIServer) handleNonStreamingChat(w http.ResponseWriter, r *http.Request, params sdkanthropic.MessageNewParams, model string) {
	for range maxToolUseRounds {
		msg, err := s.anthropicClient.Chat(r.Context(), params)
		if err != nil {
			s.log.Error(err, "anthropic chat error")
			respondError(w, http.StatusBadGateway, "failed to get response from Claude")
			return
		}

		// Check for tool use.
		toolResults, hasToolUse := s.processToolCalls(r, msg)

		if !hasToolUse {
			// Final response — extract text and return.
			text := extractText(msg)
			resp := anthropicpkg.ChatCompletionResponse{
				ID:      msg.ID,
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []anthropicpkg.ChatCompletionChoice{
					{
						Index: 0,
						Message: anthropicpkg.ChatMessage{
							Role:    "assistant",
							Content: text,
						},
						FinishReason: mapStopReason(msg.StopReason),
					},
				},
				Usage: &anthropicpkg.CompletionUsage{
					PromptTokens:     int(msg.Usage.InputTokens),
					CompletionTokens: int(msg.Usage.OutputTokens),
					TotalTokens:      int(msg.Usage.InputTokens + msg.Usage.OutputTokens),
				},
			}
			respondJSON(w, http.StatusOK, resp)
			return
		}

		// Continue with tool results.
		params.Messages = append(params.Messages, msg.ToParam())
		params.Messages = append(params.Messages, sdkanthropic.NewUserMessage(toolResults...))
	}

	respondError(w, http.StatusInternalServerError, "tool-use loop exceeded maximum iterations")
}

// handleStreamingChat processes a streaming chat completion request.
func (s *APIServer) handleStreamingChat(w http.ResponseWriter, r *http.Request, params sdkanthropic.MessageNewParams, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	msgID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	// Send initial role chunk.
	s.sendSSEChunk(w, flusher, msgID, model, created, &anthropicpkg.ChatChunkChoice{
		Index: 0,
		Delta: anthropicpkg.ChatChunkDelta{Role: "assistant"},
	})

	for range maxToolUseRounds {
		stream, err := s.anthropicClient.ChatStream(r.Context(), params)
		if err != nil {
			s.log.Error(err, "anthropic stream error")
			break
		}

		var accumulated sdkanthropic.Message
		isToolUse := false

		for stream.Next() {
			event := stream.Current()
			if err := accumulated.Accumulate(event); err != nil {
				s.log.Error(err, "error accumulating stream event")
				continue
			}

			switch variant := event.AsAny().(type) {
			case sdkanthropic.ContentBlockDeltaEvent:
				switch variant.Delta.AsAny().(type) {
				case sdkanthropic.TextDelta:
					s.sendSSEChunk(w, flusher, msgID, model, created, &anthropicpkg.ChatChunkChoice{
						Index: 0,
						Delta: anthropicpkg.ChatChunkDelta{Content: variant.Delta.Text},
					})
				case sdkanthropic.InputJSONDelta:
					// Tool input being streamed — don't forward to client.
					isToolUse = true
				}
			case sdkanthropic.ContentBlockStartEvent:
				if variant.ContentBlock.Type == "tool_use" {
					isToolUse = true
				}
			}

			select {
			case <-r.Context().Done():
				return
			default:
			}
		}

		if stream.Err() != nil {
			s.log.Error(stream.Err(), "stream error")
			break
		}

		if !isToolUse || accumulated.StopReason != sdkanthropic.StopReasonToolUse {
			break
		}

		// Process tool calls and continue.
		toolResults, hasToolUse := s.processToolCalls(r, &accumulated)
		if !hasToolUse {
			break
		}

		params.Messages = append(params.Messages, accumulated.ToParam())
		params.Messages = append(params.Messages, sdkanthropic.NewUserMessage(toolResults...))
	}

	// Send final done.
	fr := finishReasonStop
	s.sendSSEChunk(w, flusher, msgID, model, created, &anthropicpkg.ChatChunkChoice{
		Index:        0,
		Delta:        anthropicpkg.ChatChunkDelta{},
		FinishReason: &fr,
	})
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (s *APIServer) sendSSEChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, choice *anthropicpkg.ChatChunkChoice) {
	chunk := anthropicpkg.ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []anthropicpkg.ChatChunkChoice{*choice},
	}
	data, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// buildTools returns the tool definitions for the chat endpoint.
func (s *APIServer) buildTools() []sdkanthropic.ToolUnionParam {
	return []sdkanthropic.ToolUnionParam{
		{OfTool: &sdkanthropic.ToolParam{
			Name:        "create_coding_task",
			Description: sdkanthropic.String("Create a new coding task that will be executed by an autonomous coding agent. Also creates a GitHub issue to track the work."),
			InputSchema: sdkanthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"repository": map[string]any{
						"type":        "string",
						"description": "The Git clone URL for the repository (e.g., https://github.com/owner/repo.git)",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Detailed instructions for the coding agent describing what to implement, fix, or change",
					},
					"branch": map[string]any{
						"type":        "string",
						"description": "The base branch to work from (defaults to 'main')",
					},
				},
				Required: []string{"repository", "prompt"},
			},
		}},
		{OfTool: &sdkanthropic.ToolParam{
			Name:        "list_coding_tasks",
			Description: sdkanthropic.String("List all coding tasks with optional filters. Shows tasks from both chat and GitHub sources."),
			InputSchema: sdkanthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"source": map[string]any{
						"type":        "string",
						"description": "Filter by source type: 'github-issue' or 'chat'",
						"enum":        []string{"github-issue", "chat"},
					},
					"phase": map[string]any{
						"type":        "string",
						"description": "Filter by task phase",
						"enum":        []string{"Pending", "Planning", "AwaitingApproval", "Implementing", "Testing", "PullRequest", "AwaitingMerge", "Complete", "Failed"},
					},
				},
			},
		}},
		{OfTool: &sdkanthropic.ToolParam{
			Name:        "get_coding_task",
			Description: sdkanthropic.String("Get detailed information about a specific coding task including its plan, status, and PR info."),
			InputSchema: sdkanthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The name of the CodingTask resource",
					},
				},
				Required: []string{"name"},
			},
		}},
		{OfTool: &sdkanthropic.ToolParam{
			Name:        "approve_coding_task",
			Description: sdkanthropic.String("Approve a coding task's plan so it proceeds to implementation. Only works for tasks in AwaitingApproval phase."),
			InputSchema: sdkanthropic.ToolInputSchemaParam{
				Properties: map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "The name of the CodingTask resource to approve",
					},
					"run_tests": map[string]any{
						"type":        "boolean",
						"description": "Whether to run tests before creating the PR (defaults to false)",
					},
				},
				Required: []string{"name"},
			},
			// Cache breakpoint: system prompt + all tools are static, so cache them together.
			CacheControl: sdkanthropic.CacheControlEphemeralParam{Type: "ephemeral"},
		}},
	}
}

// processToolCalls executes tool calls from a message and returns tool result blocks.
func (s *APIServer) processToolCalls(r *http.Request, msg *sdkanthropic.Message) ([]sdkanthropic.ContentBlockParamUnion, bool) {
	results := make([]sdkanthropic.ContentBlockParamUnion, 0, len(msg.Content))
	hasToolUse := false

	for _, block := range msg.Content {
		if block.Type != "tool_use" {
			continue
		}
		hasToolUse = true
		toolUse := block.AsAny().(sdkanthropic.ToolUseBlock)

		result, isErr := s.executeTool(r, toolUse.Name, toolUse.Input)
		results = append(results, sdkanthropic.NewToolResultBlock(toolUse.ID, result, isErr))
	}

	return results, hasToolUse
}

// executeTool dispatches a tool call and returns the result string and whether it's an error.
func (s *APIServer) executeTool(r *http.Request, name string, input json.RawMessage) (string, bool) {
	switch name {
	case "create_coding_task":
		return s.toolCreateTask(r, input)
	case "list_coding_tasks":
		return s.toolListTasks(r, input)
	case "get_coding_task":
		return s.toolGetTask(r, input)
	case "approve_coding_task":
		return s.toolApproveTask(r, input)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}
}

type createTaskInput struct {
	Repository string `json:"repository"`
	Prompt     string `json:"prompt"`
	Branch     string `json:"branch"`
}

func (s *APIServer) toolCreateTask(r *http.Request, input json.RawMessage) (string, bool) {
	var in createTaskInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}
	if in.Repository == "" || in.Prompt == "" {
		return "repository and prompt are required", true
	}

	branch := in.Branch
	if branch == "" {
		branch = defaultBranchName
	}

	taskName := fmt.Sprintf("task-%d", metav1.Now().UnixMilli())

	task := &agentsv1alpha1.CodingTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      taskName,
			Namespace: s.taskNamespace,
		},
		Spec: agentsv1alpha1.CodingTaskSpec{
			Source: agentsv1alpha1.TaskSource{
				Type: agentsv1alpha1.TaskSourceChat,
				Chat: &agentsv1alpha1.ChatSource{
					SessionID: "open-webui",
					UserID:    "chat",
				},
			},
			Repository: agentsv1alpha1.RepositorySpec{
				URL:    in.Repository,
				Branch: branch,
			},
			Prompt: in.Prompt,
			AnthropicAPIKeyRef: agentsv1alpha1.SecretReference{
				Name: s.anthropicSecretName,
				Key:  s.anthropicSecretKey,
			},
			GitCredentialsRef: agentsv1alpha1.SecretReference{
				Name: s.gitCredentialsSecretName,
				Key:  s.gitCredentialsSecretKey,
			},
		},
	}

	if err := s.client.Create(r.Context(), task); err != nil {
		s.log.Error(err, "failed to create CodingTask via tool")
		return fmt.Sprintf("failed to create task: %v", err), true
	}

	result := map[string]any{
		"name":      task.Name,
		"namespace": task.Namespace,
		"phase":     "Pending",
	}

	// Create a tracking GitHub issue if GitHub client is available.
	if s.githubClient != nil {
		owner, repo := parseRepoURL(in.Repository)
		if owner != "" && repo != "" {
			issue, _, err := s.githubClient.Issues.Create(r.Context(), owner, repo, &gogithub.IssueRequest{
				Title:  gogithub.Ptr(fmt.Sprintf("[AI Task] %s", truncate(in.Prompt, 80))),
				Body:   gogithub.Ptr(fmt.Sprintf("Created from chat via agent-operator.\n\n**Task:** `%s`\n\n**Prompt:**\n%s", taskName, in.Prompt)),
				Labels: &[]string{"ai-task"},
			})
			if err != nil {
				s.log.Error(err, "failed to create tracking GitHub issue", "owner", owner, "repo", repo)
			} else {
				// Update the task status with the tracking issue reference.
				task.Status.TrackingIssue = &agentsv1alpha1.GitHubIssueRef{
					Owner:       owner,
					Repo:        repo,
					IssueNumber: issue.GetNumber(),
					URL:         issue.GetHTMLURL(),
				}
				if err := s.client.Status().Update(r.Context(), task); err != nil {
					s.log.Error(err, "failed to update task with tracking issue")
				}
				result["issue_url"] = issue.GetHTMLURL()
				result["issue_number"] = issue.GetNumber()
			}
		}
	}

	data, _ := json.Marshal(result)
	return string(data), false
}

type listTasksInput struct {
	Source string `json:"source"`
	Phase  string `json:"phase"`
}

func (s *APIServer) toolListTasks(r *http.Request, input json.RawMessage) (string, bool) {
	var in listTasksInput
	_ = json.Unmarshal(input, &in) // ignore errors — filters are optional

	var taskList agentsv1alpha1.CodingTaskList
	if err := s.client.List(r.Context(), &taskList, client.InNamespace(s.taskNamespace)); err != nil {
		return fmt.Sprintf("failed to list tasks: %v", err), true
	}

	results := make([]taskSummary, 0, len(taskList.Items))
	for _, t := range taskList.Items {
		if in.Source != "" && string(t.Spec.Source.Type) != in.Source {
			continue
		}
		if in.Phase != "" && string(t.Status.Phase) != in.Phase {
			continue
		}
		results = append(results, toTaskSummary(&t))
	}

	data, _ := json.Marshal(results)
	return string(data), false
}

type getTaskInput struct {
	Name string `json:"name"`
}

func (s *APIServer) toolGetTask(r *http.Request, input json.RawMessage) (string, bool) {
	var in getTaskInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}

	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Name: in.Name, Namespace: s.taskNamespace}
	if err := s.client.Get(r.Context(), key, &task); err != nil {
		return fmt.Sprintf("task not found: %s", in.Name), true
	}

	data, _ := json.Marshal(toTaskDetail(&task))
	return string(data), false
}

type approveTaskInput struct {
	Name     string `json:"name"`
	RunTests bool   `json:"run_tests"`
}

func (s *APIServer) toolApproveTask(r *http.Request, input json.RawMessage) (string, bool) {
	var in approveTaskInput
	if err := json.Unmarshal(input, &in); err != nil {
		return fmt.Sprintf("invalid input: %v", err), true
	}

	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Name: in.Name, Namespace: s.taskNamespace}
	if err := s.client.Get(r.Context(), key, &task); err != nil {
		return fmt.Sprintf("task not found: %s", in.Name), true
	}

	if task.Status.Phase != agentsv1alpha1.TaskPhaseAwaitingApproval {
		return fmt.Sprintf("task is in %s phase, not AwaitingApproval", task.Status.Phase), true
	}

	task.Status.Phase = agentsv1alpha1.TaskPhaseImplementing
	task.Status.CurrentStep = 3
	task.Status.RunTests = in.RunTests
	if in.RunTests {
		task.Status.TotalSteps = 5
	} else {
		task.Status.TotalSteps = 4
	}
	task.Status.Message = "Plan approved via chat, starting implementation"
	if err := s.client.Status().Update(r.Context(), &task); err != nil {
		return fmt.Sprintf("failed to approve task: %v", err), true
	}

	result := map[string]any{
		"name":     task.Name,
		"phase":    string(task.Status.Phase),
		"runTests": task.Status.RunTests,
		"message":  task.Status.Message,
	}
	data, _ := json.Marshal(result)
	return string(data), false
}

// extractText extracts all text content from a message.
func extractText(msg *sdkanthropic.Message) string {
	var b strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// mapStopReason maps Anthropic stop reasons to OpenAI finish reasons.
func mapStopReason(reason sdkanthropic.StopReason) string {
	switch reason {
	case sdkanthropic.StopReasonEndTurn:
		return finishReasonStop
	case sdkanthropic.StopReasonMaxTokens:
		return "length"
	case sdkanthropic.StopReasonToolUse:
		return "tool_calls"
	default:
		return finishReasonStop
	}
}

// parseRepoURL extracts owner and repo from a GitHub URL.
func parseRepoURL(repoURL string) (string, string) {
	// Handle https://github.com/owner/repo.git and similar patterns.
	patterns := []string{
		"github.com/",
	}
	for _, p := range patterns {
		idx := len(p)
		start := -1
		for i := 0; i <= len(repoURL)-idx; i++ {
			if repoURL[i:i+idx] == p {
				start = i + idx
				break
			}
		}
		if start == -1 {
			continue
		}

		rest := repoURL[start:]
		// Remove trailing .git
		if len(rest) > 4 && rest[len(rest)-4:] == ".git" {
			rest = rest[:len(rest)-4]
		}
		// Split by /
		for i, c := range rest {
			if c == '/' {
				owner := rest[:i]
				repo := rest[i+1:]
				// Remove any trailing slashes or paths
				for j, c2 := range repo {
					if c2 == '/' {
						repo = repo[:j]
						break
					}
				}
				if owner != "" && repo != "" {
					return owner, repo
				}
			}
		}
	}
	return "", ""
}

// truncate truncates a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
