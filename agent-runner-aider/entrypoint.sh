#!/bin/bash
set -euo pipefail

# Agent Runner Entrypoint (Aider)
# This script is the entrypoint for Aider-based agent pods. It adapts behavior
# based on the AGENT_STEP environment variable (plan, implement, test, pull-request).
# Same contract as the Claude Code runner, but uses Aider with OpenAI-compatible APIs.

# Required environment variables:
#   AGENT_STEP          - The workflow step (plan, implement, test, pull-request)
#   AGENT_REPO_URL      - Git clone URL
#   AGENT_BASE_BRANCH   - Base branch (e.g., main)
#   AGENT_WORK_BRANCH   - Work branch for agent changes
#   AGENT_PROMPT        - The prompt/instructions for this step
#   OPENAI_API_BASE     - OpenAI-compatible API base URL (e.g., Ollama endpoint)
#   OPENAI_API_KEY      - API key (can be "ollama" for local Ollama)
#   GIT_TOKEN           - GitHub token for git operations
#   AGENT_OUTPUT_DIR    - Directory for output artifacts
#   AGENT_WORKSPACE_DIR - Directory for the workspace/repo clone

# Optional:
#   AGENT_MODEL         - Model to use (default: qwen2.5:3b)
#   AGENT_CONTEXT       - Additional context from previous steps

TERMINATION_MESSAGE_PATH="${AGENT_OUTPUT_DIR}/termination-message"

log() {
    echo "[agent-runner-aider] $(date -u +%Y-%m-%dT%H:%M:%SZ) $*"
}

write_output() {
    local output="$1"
    # Write full output to pod logs with markers for controller extraction.
    echo "===AGENT_OUTPUT_BEGIN==="
    echo "$output"
    echo "===AGENT_OUTPUT_END==="
    # Write to termination message (truncated to 4096 bytes for K8s limit).
    echo "$output" | head -c 4096 > "$TERMINATION_MESSAGE_PATH"
    # Also write full output to a file.
    echo "$output" > "${AGENT_OUTPUT_DIR}/output.txt"
}

fail() {
    local message="$1"
    log "FAILED: $message"
    write_output "FAILED: $message"
    exit 1
}

handle_pull_request() {
    log "Handling pull-request step directly (no LLM needed)"

    # Ensure the work branch is pushed to the remote.
    log "Pushing work branch to remote..."
    git push -u origin "$AGENT_WORK_BRANCH" 2>&1 || fail "Failed to push work branch"

    # Check if a PR already exists for this branch (retry-safe).
    EXISTING_PR=$(gh pr view "$AGENT_WORK_BRANCH" --json url --jq '.url' 2>/dev/null || true)
    if [ -n "$EXISTING_PR" ]; then
        log "PR already exists: $EXISTING_PR"
        write_output "$EXISTING_PR"
        return 0
    fi

    # Extract PR title from the prompt (look for "Original Task:" line).
    PR_TITLE=$(echo "$AGENT_PROMPT" | grep -m1 'Original Task:' | sed 's/.*Original Task:\s*//' | head -c 72 || true)
    if [ -z "$PR_TITLE" ]; then
        # Fallback: use the first non-empty line of the prompt.
        PR_TITLE=$(echo "$AGENT_PROMPT" | grep -m1 '.' | head -c 72 || true)
    fi
    if [ -z "$PR_TITLE" ]; then
        PR_TITLE="Agent: changes on $AGENT_WORK_BRANCH"
    fi

    # Build PR body by parsing structured sections from AGENT_PROMPT.
    # The operator builds the prompt as:
    #   Original Task: <description>
    #   Plan:
    #   <plan text>
    #   <newline>Test Results: ...    (optional, inline after plan)
    #   <newline>Implementation Notes: ...  (optional, inline after plan)
    #   Instructions:
    #   ...
    TASK_DESC=$(echo "$AGENT_PROMPT" | sed -n 's/^Original Task: *//p' | head -1)
    # Plan sits between "Plan:" and "Instructions:" lines, excluding metadata lines.
    PLAN_SECTION=$(echo "$AGENT_PROMPT" | awk '
        /^Plan:$/ { f=1; next }
        /^Instructions:$/ { f=0; next }
        /^Test Results:/ { next }
        /^Implementation Notes:/ { next }
        f { print }
    ')
    TEST_RESULTS=$(echo "$AGENT_PROMPT" | sed -n 's/^Test Results: *//p')
    IMPL_NOTES=$(echo "$AGENT_PROMPT" | sed -n 's/^Implementation Notes: *//p')

    PR_BODY="Automated PR created by [agent-operator](https://github.com/jcwearn/agent-operator)."

    if [ -n "$TASK_DESC" ]; then
        PR_BODY="${PR_BODY}

## Task

${TASK_DESC}"
    fi

    if [ -n "$PLAN_SECTION" ]; then
        PR_BODY="${PR_BODY}

## Plan

${PLAN_SECTION}"
    fi

    if [ -n "$TEST_RESULTS" ]; then
        PR_BODY="${PR_BODY}

## Test Results

${TEST_RESULTS}"
    fi

    if [ -n "$IMPL_NOTES" ]; then
        PR_BODY="${PR_BODY}

## Implementation Notes

${IMPL_NOTES}"
    fi

    log "Creating PR: $PR_TITLE"
    PR_URL=$(gh pr create \
        --base "$AGENT_BASE_BRANCH" \
        --head "$AGENT_WORK_BRANCH" \
        --title "$PR_TITLE" \
        --body "$PR_BODY" \
        2>&1) || fail "gh pr create failed: $PR_URL"

    log "PR created: $PR_URL"
    write_output "$PR_URL"
}

# Validate required environment variables.
for var in AGENT_STEP AGENT_REPO_URL AGENT_BASE_BRANCH AGENT_WORK_BRANCH AGENT_PROMPT OPENAI_API_BASE OPENAI_API_KEY GIT_TOKEN AGENT_OUTPUT_DIR AGENT_WORKSPACE_DIR; do
    if [ -z "${!var:-}" ]; then
        fail "Required environment variable $var is not set"
    fi
done

AGENT_MODEL="${AGENT_MODEL:-qwen2.5:7b}"

log "Starting agent step: ${AGENT_STEP}"
log "Repository: ${AGENT_REPO_URL}"
log "Base branch: ${AGENT_BASE_BRANCH}"
log "Work branch: ${AGENT_WORK_BRANCH}"
log "Model: ${AGENT_MODEL}"
log "API base: ${OPENAI_API_BASE}"

# Configure git identity.
git config --global user.email "agent@wearn.dev"
git config --global user.name "Agent Operator"

# Authenticate GitHub CLI and configure git to use it for credentials.
echo "$GIT_TOKEN" | gh auth login --with-token 2>&1 || fail "gh auth login failed"
gh auth setup-git 2>&1 || fail "gh auth setup-git failed"
export GH_TOKEN="$GIT_TOKEN"
log "GitHub CLI authenticated: $(gh auth status 2>&1 | head -1)"

# Clone the repository.
log "Cloning repository..."
cd "$AGENT_WORKSPACE_DIR"
git clone "$AGENT_REPO_URL" repo
cd repo

# Branch handling based on step.
case "$AGENT_STEP" in
    plan)
        git checkout "$AGENT_BASE_BRANCH"
        ;;
    implement)
        if git ls-remote --heads origin "$AGENT_WORK_BRANCH" | grep -q "$AGENT_WORK_BRANCH"; then
            git checkout "$AGENT_WORK_BRANCH"
        else
            git checkout -b "$AGENT_WORK_BRANCH" "origin/$AGENT_BASE_BRANCH"
        fi
        ;;
    test)
        git checkout "$AGENT_WORK_BRANCH"
        ;;
    pull-request)
        git checkout "$AGENT_WORK_BRANCH"
        ;;
    *)
        fail "Unknown step: $AGENT_STEP"
        ;;
esac

log "On branch: $(git branch --show-current)"

# Handle pull-request step directly — no LLM needed.
if [ "$AGENT_STEP" = "pull-request" ]; then
    handle_pull_request
    log "Agent step ${AGENT_STEP} completed successfully"
    exit 0
fi

# Build the prompt with context.
FULL_PROMPT="$AGENT_PROMPT"
if [ -n "${AGENT_CONTEXT:-}" ]; then
    FULL_PROMPT="${FULL_PROMPT}

Context from previous steps:
${AGENT_CONTEXT}"
fi

# Build Aider arguments.
# --yes-always: auto-accept all prompts
# --no-auto-commits: let us handle git commits
# --no-auto-lint: skip linting
# --no-suggest-shell-commands: don't suggest shell commands
AIDER_ARGS="--message"
AIDER_MODEL="openai/${AGENT_MODEL}"

log "Running Aider with model: $AIDER_MODEL"
AIDER_OUTPUT=$(aider \
    $AIDER_ARGS "$FULL_PROMPT" \
    --model "$AIDER_MODEL" \
    --yes-always \
    --no-auto-commits \
    --no-auto-lint \
    --no-suggest-shell-commands \
    --no-show-model-warnings \
    --no-pretty \
    --no-stream \
    --no-gitignore \
    2>&1) || {
    log "Aider exited with non-zero status"
    write_output "$AIDER_OUTPUT"
    exit 1
}

log "Aider completed"

# Post-processing based on step.
case "$AGENT_STEP" in
    plan)
        # Filter aider startup noise from plan output.
        FILTERED_OUTPUT=$(echo "$AIDER_OUTPUT" | grep -v -E \
            -e '^Aider v' \
            -e '^Model:' \
            -e '^Git repo:' \
            -e '^Repo-map:' \
            -e '^Use /help' \
            -e '^https?://' \
            -e 'Scanning repo' \
            -e '^\s*$' \
            -e '^─' \
            -e '\.aider' \
            || true)
        write_output "${FILTERED_OUTPUT:-$AIDER_OUTPUT}"
        ;;
    implement)
        # Push the work branch with changes.
        if [ -n "$(git status --porcelain)" ]; then
            git add -A
            git commit -m "agent: implement changes for task

$AIDER_OUTPUT" || true
        fi
        git push -u origin "$AGENT_WORK_BRANCH"
        write_output "$AIDER_OUTPUT"
        ;;
    test)
        write_output "$AIDER_OUTPUT"
        if echo "$AIDER_OUTPUT" | grep -qi "ALL TESTS PASSED"; then
            log "Tests passed"
            exit 0
        else
            log "Tests may have failed - check output"
            exit 0
        fi
        ;;
esac

log "Agent step ${AGENT_STEP} completed successfully"
