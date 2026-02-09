# Duplicate GitHub Workflow Runs - Analysis and Recommendations

## Problem Summary

Pull requests against the `main` branch are experiencing duplicate workflow runs. For example, on [PR #26](https://github.com/jcwearn/agent-operator/pull/26), the Build Images, Lint, Tests, and E2E Tests workflows all ran multiple times.

## Root Cause

The duplication occurs because several workflows are configured with both `push` and `pull_request` triggers without branch filtering:

### Current Workflow Configurations

1. **build-images.yml** ✅ Correctly configured
   - Trigger: `pull_request` only, branches: `[main]`
   - Result: Runs once per PR

2. **lint.yml** ❌ Causes duplicates
   - Trigger: `push` AND `pull_request` (no branch filtering)
   - Result: Runs on both push to PR branch AND on pull_request event

3. **test.yml** ❌ Causes duplicates
   - Trigger: `push` AND `pull_request` (no branch filtering)
   - Result: Runs on both push to PR branch AND on pull_request event

4. **test-e2e.yml** ❌ Causes duplicates
   - Trigger: `push` AND `pull_request` (no branch filtering)
   - Result: Runs on both push to PR branch AND on pull_request event

## Why This Happens

When you push commits to a PR branch:
1. GitHub triggers a `push` event for that branch
2. GitHub also triggers a `pull_request` event (synchronize) for the open PR
3. Workflows with both triggers run twice - once for each event

## Recommended Solutions

### Option 1: Use `pull_request` trigger only (Recommended)

Remove the `push` trigger from workflows that should run on PRs. This is the cleanest solution and matches the pattern already used in `build-images.yml`.

**For lint.yml, test.yml, and test-e2e.yml**, change:
```yaml
on:
  push:
  pull_request:
```

To:
```yaml
on:
  pull_request:
    branches: [main]
```

**Pros:**
- Eliminates all duplicate runs
- Clear and explicit behavior
- Matches existing pattern in build-images.yml

**Cons:**
- Workflows won't run on direct pushes to main (if that's desired)

### Option 2: Keep both triggers but add branch filtering

If you want workflows to run on direct pushes to main AND on PRs, use branch filtering:

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

**Pros:**
- Workflows run on direct main branch pushes
- Workflows run once per PR
- No duplicates

**Cons:**
- More configuration
- May be unnecessary if all changes go through PRs

### Option 3: Use paths-ignore or path filtering

If certain workflows should only run when specific files change, add path filters:

```yaml
on:
  pull_request:
    branches: [main]
    paths:
      - '**/*.go'
      - 'go.mod'
      - 'go.sum'
```

## Implementation Note

⚠️ **Permission Issue**: The GitHub App currently lacks the `workflows` permission required to modify workflow files. To implement these fixes:

1. Grant the GitHub App `workflows` permission, OR
2. Manually apply the changes to the workflow files in `.github/workflows/`

## Recommended Action Plan

1. Update `lint.yml` to use `pull_request` trigger only with `branches: [main]`
2. Update `test.yml` to use `pull_request` trigger only with `branches: [main]`
3. Update `test-e2e.yml` to use `pull_request` trigger only with `branches: [main]`
4. Consider if any workflows need to run on direct pushes to main (likely not if PRs are required)

## Files to Modify

- `.github/workflows/lint.yml`
- `.github/workflows/test.yml`
- `.github/workflows/test-e2e.yml`

## Example Fix for lint.yml

```yaml
name: Lint

on:
  pull_request:
    branches: [main]

jobs:
  lint:
    name: Run on Ubuntu
    runs-on: ubuntu-latest
    steps:
      - name: Clone the code
        uses: actions/checkout@v4

      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Run linter
        uses: golangci/golangci-lint-action@v8
        with:
          version: v2.7.2
```

Apply the same pattern to test.yml and test-e2e.yml.
