# GitHub Workflow Duplication Analysis

## Problem

On pull requests to main (e.g., [PR #26](https://github.com/jcwearn/agent-operator/pull/26)), workflows are running twice, causing unnecessary CI resource usage and confusing status checks.

## Root Cause

Three workflow files are configured to trigger on **both** `push` and `pull_request` events without branch restrictions:

1. `.github/workflows/lint.yml:3-6`
2. `.github/workflows/test.yml:3-6`
3. `.github/workflows/test-e2e.yml:3-6`

```yaml
on:
  push:
  pull_request:
```

### Why This Causes Duplicates

When a pull request is created or updated against main:
1. The `pull_request` event fires → workflows run once
2. If the branch is in the same repository (not a fork), the `push` event ALSO fires → workflows run again
3. Result: 2 runs of Lint, Tests, and E2E Tests for the same code change

### Correctly Configured Workflow

The `build-images.yml` workflow is correctly configured to only run on pull requests to main:

```yaml
on:
  pull_request:
    branches: [main]
```

This workflow does NOT have duplicates because it only listens to the `pull_request` event.

## Recommended Solutions

### Option 1: PR-Only for PRs, Push-Only for Main (Recommended)

Update the three workflows to run on pull requests to main, and pushes to main:

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

**Benefits**:
- No duplicates on PRs (only `pull_request` event runs)
- Still validates pushes directly to main
- Consistent with `build-images.yml` pattern

### Option 2: PR-Only (Simpler)

Update the three workflows to only run on pull requests:

```yaml
on:
  pull_request:
    branches: [main]
```

**Benefits**:
- Simpler configuration
- No duplicates
- Still validates all changes before merge

**Trade-offs**:
- Direct pushes to main won't trigger CI (but this is usually not a concern if main is protected)

### Option 3: Use Concurrency Groups

Keep current triggers but add concurrency control to cancel redundant runs:

```yaml
on:
  push:
  pull_request:

concurrency:
  group: ${{ github.workflow }}-${{ github.event.pull_request.number || github.sha }}
  cancel-in-progress: true
```

**Trade-offs**:
- Still triggers twice, but cancels one
- More complex to understand
- Not recommended - better to prevent the duplicate trigger

## Implementation

### Files to Update

1. `.github/workflows/lint.yml`
2. `.github/workflows/test.yml`
3. `.github/workflows/test-e2e.yml`

### Recommended Changes

For each file, replace:
```yaml
on:
  push:
  pull_request:
```

With:
```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

## Why Previous Fix Failed

The GitHub App being used lacks the `workflows` permission, which is required to modify workflow files in `.github/workflows/`. This is a GitHub security restriction.

**Error message**:
```
! [remote rejected] ai/gh-jcwearn-agent-operator-27 -> ai/gh-jcwearn-agent-operator-27
(refusing to allow a GitHub App to create or update workflow `.github/workflows/lint.yml`
without `workflows` permission)
```

### How to Apply the Fix

**Option A**: Grant the GitHub App `workflows` permission in the repository settings

**Option B**: Apply the changes manually:
1. Check out the branch
2. Edit the three workflow files as described above
3. Commit and push the changes
4. The bot can continue from there

**Option C**: Use a personal access token (PAT) instead of the GitHub App token for this specific operation
