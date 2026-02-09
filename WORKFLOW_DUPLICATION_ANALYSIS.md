# GitHub Workflows Duplication Analysis

## Problem Summary
Duplicate workflow runs are occurring on PRs against main due to overlapping trigger configurations.

## Root Cause
The workflows have conflicting trigger patterns:

### Current Configuration

| Workflow | Triggers | Branch Filter |
|----------|----------|---------------|
| build-images.yml | `pull_request` | `branches: [main]` |
| lint.yml | `push` + `pull_request` | None |
| test.yml | `push` + `pull_request` | None |
| test-e2e.yml | `push` + `pull_request` | None |

### What Happens on a PR to Main

1. When PR is opened: All workflows run (triggered by `pull_request` event)
2. When commits are pushed to PR branch: lint.yml, test.yml, test-e2e.yml run again (triggered by `push` event)

This results in duplicate workflow runs for lint, test, and e2e-test jobs.

## Recommended Solution

**Option 1: Use pull_request only for PR validation** (Recommended)

Change lint.yml, test.yml, and test-e2e.yml to use the same pattern as build-images.yml:

```yaml
on:
  pull_request:
    branches: [main]
```

This ensures workflows run once per PR, not on every push to the PR branch.

**Option 2: Use push for main branch only, pull_request for PRs**

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

This runs workflows on pushes to main (after merge) and on PR creation/updates, but avoids duplication since PR commits don't push to main.

**Option 3: Use paths-ignore to reduce unnecessary runs** (Additional optimization)

If certain files (like docs) don't need CI validation, add:

```yaml
on:
  pull_request:
    branches: [main]
    paths-ignore:
      - '**.md'
      - 'docs/**'
```

## Files Requiring Changes

1. `.github/workflows/lint.yml` - Remove `push` trigger or add branch filter
2. `.github/workflows/test.yml` - Remove `push` trigger or add branch filter
3. `.github/workflows/test-e2e.yml` - Remove `push` trigger or add branch filter
4. `.github/workflows/build-images.yml` - No changes needed (already correct)

## Implementation Note

⚠️ **Workflow Permission Required**: Modifying workflow files requires the GitHub App to have `workflows` permission. The current error indicates this permission is not granted:

```
refusing to allow a GitHub App to create or update workflow `.github/workflows/lint.yml` without `workflows` permission
```

To implement these changes, either:
1. Grant the GitHub App `workflows` permission in repository settings
2. Manually apply the recommended changes to the workflow files
3. Create a PR from a personal account with the changes
