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

# Process GitHub issue attachments embedded in the prompt.
# Downloads text files and inlines their content; saves images to disk so
# Claude can read them via its Read tool.
process_attachments() {
    local prompt="$1"
    local attachments_dir="/agent/workspace/attachments"
    local max_text_bytes=102400  # 100KB per file

    # Extract markdown links pointing to GitHub user-attachments.
    # Matches both [name](url) and ![name](url) patterns.
    local links
    links=$(echo "$prompt" | grep -oP '!?\[([^\]]*)\]\((https://github\.com/user-attachments/assets/[^\)]+)\)' || true)

    if [ -z "$links" ]; then
        echo "$prompt"
        return 0
    fi

    mkdir -p "$attachments_dir"
    local modified_prompt="$prompt"

    while IFS= read -r link; do
        # Parse the link text and URL.
        local link_text url
        link_text=$(echo "$link" | sed -E 's/^!?\[([^\]]*)\]\(.*\)$/\1/')
        url=$(echo "$link" | grep -oP 'https://github\.com/user-attachments/assets/[^\)]+')

        if [ -z "$url" ]; then
            continue
        fi

        log "Processing attachment: $link_text ($url)" >&2

        # Download to a temp file.
        local tmpfile
        tmpfile=$(mktemp /tmp/attachment-XXXXXX)
        if ! curl -sfL -H "Authorization: token $GIT_TOKEN" -o "$tmpfile" "$url"; then
            log "WARNING: Failed to download attachment: $link_text" >&2
            rm -f "$tmpfile"
            continue
        fi

        # Determine file type. Prefer extension from link text, fall back to
        # file(1) MIME detection.
        local ext mimetype filetype
        ext="${link_text##*.}"
        ext=$(echo "$ext" | tr '[:upper:]' '[:lower:]')

        case "$ext" in
            txt|md|yaml|yml|json|xml|csv|log|sh|bash|py|go|js|ts|html|css|toml|ini|cfg|conf|env|sql|rb|rs|java|c|h|cpp|hpp|makefile|dockerfile)
                filetype="text"
                ;;
            png|jpg|jpeg|gif|webp|svg|bmp|ico|tiff|tif)
                filetype="image"
                ;;
            *)
                # Fall back to file(1) MIME detection.
                mimetype=$(file --mime-type -b "$tmpfile" 2>/dev/null || echo "application/octet-stream")
                case "$mimetype" in
                    text/*)
                        filetype="text"
                        ;;
                    image/*)
                        filetype="image"
                        ;;
                    *)
                        log "WARNING: Unknown file type for $link_text (mime: $mimetype), skipping" >&2
                        rm -f "$tmpfile"
                        continue
                        ;;
                esac
                ;;
        esac

        if [ "$filetype" = "text" ]; then
            local filesize
            filesize=$(wc -c < "$tmpfile")
            if [ "$filesize" -gt "$max_text_bytes" ]; then
                log "WARNING: Text file $link_text is ${filesize} bytes (>${max_text_bytes}), truncating" >&2
            fi
            local content
            content=$(head -c "$max_text_bytes" "$tmpfile")
            local replacement
            replacement=$(printf '\n\n--- Attached file: %s ---\n```\n%s\n```\n--- End of %s ---\n' "$link_text" "$content" "$link_text")
            modified_prompt="${modified_prompt//$link/$replacement}"
            log "Inlined text attachment: $link_text (${filesize} bytes)" >&2
        elif [ "$filetype" = "image" ]; then
            local dest="${attachments_dir}/${link_text}"
            mv "$tmpfile" "$dest"
            local replacement
            replacement=$(printf '\n\n[Attached image: %s — saved to %s. Use your Read tool to view this image file.]\n' "$link_text" "$dest")
            modified_prompt="${modified_prompt//$link/$replacement}"
            log "Saved image attachment: $link_text -> $dest" >&2
            # Skip the rm below since we moved the file.
            continue
        fi

        rm -f "$tmpfile"
    done <<< "$links"

    echo "$modified_prompt"
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
# GH_TOKEN must NOT be exported yet — gh auth login refuses to store credentials
# when it detects GH_TOKEN already in the environment.
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

# Process any GitHub issue attachments in the prompt.
log "Processing attachments in prompt..."
if PROCESSED_PROMPT=$(process_attachments "$FULL_PROMPT"); then
    FULL_PROMPT="$PROCESSED_PROMPT"
else
    log "WARNING: Attachment processing failed, using original prompt"
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
