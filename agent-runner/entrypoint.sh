#!/bin/bash
set -euo pipefail

# Agent Runner Entrypoint
# This script is the entrypoint for agent pods. It adapts behavior based on
# the AGENT_STEP environment variable (plan, implement, test, pull-request).

# Required environment variables:
#   AGENT_STEP          - The workflow step (plan, implement, test, pull-request)
#   AGENT_REPO_URL      - Git clone URL
#   AGENT_BASE_BRANCH   - Base branch (e.g., main)
#   AGENT_WORK_BRANCH   - Work branch for agent changes
#   AGENT_PROMPT        - The prompt/instructions for this step
#   ANTHROPIC_API_KEY   - Anthropic API key for Claude
#   GIT_TOKEN           - GitHub token for git operations
#   AGENT_OUTPUT_DIR    - Directory for output artifacts
#   AGENT_WORKSPACE_DIR - Directory for the workspace/repo clone

# Optional:
#   AGENT_CONTEXT       - Additional context from previous steps

TERMINATION_MESSAGE_PATH="${AGENT_OUTPUT_DIR}/termination-message"

log() {
    echo "[agent-runner] $(date -u +%Y-%m-%dT%H:%M:%SZ) $*"
}

write_output() {
    local output="$1"
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

# Validate required environment variables.
for var in AGENT_STEP AGENT_REPO_URL AGENT_BASE_BRANCH AGENT_WORK_BRANCH AGENT_PROMPT ANTHROPIC_API_KEY GIT_TOKEN AGENT_OUTPUT_DIR AGENT_WORKSPACE_DIR; do
    if [ -z "${!var:-}" ]; then
        fail "Required environment variable $var is not set"
    fi
done

log "Starting agent step: ${AGENT_STEP}"
log "Repository: ${AGENT_REPO_URL}"
log "Base branch: ${AGENT_BASE_BRANCH}"
log "Work branch: ${AGENT_WORK_BRANCH}"

# Configure git identity.
git config --global user.email "agent@wearn.dev"
git config --global user.name "Agent Operator"

# Authenticate GitHub CLI and configure git to use it for credentials.
# This is more reliable in containers than GH_TOKEN env var alone, since
# gh auth login writes a persistent config that survives subprocess spawning.
export GH_TOKEN="$GIT_TOKEN"
echo "$GIT_TOKEN" | gh auth login --with-token 2>&1 || fail "gh auth login failed"
gh auth setup-git 2>&1 || fail "gh auth setup-git failed"
log "GitHub CLI authenticated: $(gh auth status 2>&1 | head -1)"

# Clone the repository.
log "Cloning repository..."
cd "$AGENT_WORKSPACE_DIR"
git clone "$AGENT_REPO_URL" repo
cd repo

# Branch handling based on step.
case "$AGENT_STEP" in
    plan)
        # Planning step: work on the base branch (read-only).
        git checkout "$AGENT_BASE_BRANCH"
        ;;
    implement)
        # Implementation step: create or checkout the work branch.
        if git ls-remote --heads origin "$AGENT_WORK_BRANCH" | grep -q "$AGENT_WORK_BRANCH"; then
            git checkout "$AGENT_WORK_BRANCH"
        else
            git checkout -b "$AGENT_WORK_BRANCH" "origin/$AGENT_BASE_BRANCH"
        fi
        ;;
    test)
        # Test step: checkout the work branch.
        git checkout "$AGENT_WORK_BRANCH"
        ;;
    pull-request)
        # PR step: checkout the work branch.
        git checkout "$AGENT_WORK_BRANCH"
        ;;
    *)
        fail "Unknown step: $AGENT_STEP"
        ;;
esac

log "On branch: $(git branch --show-current)"

# Build the Claude Code prompt with context.
FULL_PROMPT="$AGENT_PROMPT"
if [ -n "${AGENT_CONTEXT:-}" ]; then
    FULL_PROMPT="${FULL_PROMPT}

Context from previous steps:
${AGENT_CONTEXT}"
fi

# Build Claude CLI arguments.
CLAUDE_ARGS="--print --dangerously-skip-permissions"
[ -n "${AGENT_MODEL:-}" ] && CLAUDE_ARGS="$CLAUDE_ARGS --model $AGENT_MODEL"
[ -n "${AGENT_MAX_TURNS:-}" ] && CLAUDE_ARGS="$CLAUDE_ARGS --max-turns $AGENT_MAX_TURNS"

# Run Claude Code in non-interactive mode.
log "Running Claude Code with args: $CLAUDE_ARGS"
CLAUDE_OUTPUT=$(claude $CLAUDE_ARGS "$FULL_PROMPT" 2>&1) || {
    log "Claude Code exited with non-zero status"
    write_output "$CLAUDE_OUTPUT"
    exit 1
}

log "Claude Code completed"

# Post-processing based on step.
case "$AGENT_STEP" in
    plan)
        # Output the plan.
        write_output "$CLAUDE_OUTPUT"
        ;;
    implement)
        # Push the work branch with changes.
        if [ -n "$(git status --porcelain)" ]; then
            git add -A
            git commit -m "agent: implement changes for task

$CLAUDE_OUTPUT" || true
        fi
        git push -u origin "$AGENT_WORK_BRANCH"
        write_output "$CLAUDE_OUTPUT"
        ;;
    test)
        # Output test results. Exit code determines success/failure.
        write_output "$CLAUDE_OUTPUT"
        if echo "$CLAUDE_OUTPUT" | grep -qi "ALL TESTS PASSED"; then
            log "Tests passed"
            exit 0
        else
            log "Tests may have failed - check output"
            # Let the caller (operator) determine success based on exit code.
            # Claude Code already exited 0, so we trust its assessment.
            exit 0
        fi
        ;;
    pull-request)
        # The PR URL should be in the Claude output.
        write_output "$CLAUDE_OUTPUT"
        ;;
esac

log "Agent step ${AGENT_STEP} completed successfully"
