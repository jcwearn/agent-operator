# Helm Chart Release Process

This document describes how to package and publish Helm chart releases for agent-operator.

## Overview

The Helm chart is located in `charts/agent-operator/` and should be released alongside the operator Docker images. The chart version should match the operator version for consistency.

## Manual Release Process

Since the GitHub App workflow doesn't have permissions to modify workflow files, the Helm chart can be packaged and released manually or through a separate workflow.

### 1. Update Chart Version

Before creating a release, update the version in `charts/agent-operator/Chart.yaml`:

```yaml
version: 0.1.0      # Chart version
appVersion: "0.1.0" # Application version (should match the tag)
```

### 2. Package the Chart

```bash
# Package the chart
helm package charts/agent-operator -d charts/packages/

# This creates: charts/packages/agent-operator-0.1.0.tgz
```

### 3. Generate/Update Chart Index

```bash
# Generate index for the chart repository
helm repo index charts/packages/ --url https://github.com/jcwearn/agent-operator/releases/download/v0.1.0/

# This creates: charts/packages/index.yaml
```

### 4. Upload to GitHub Release

When creating a release (via the existing release workflow), attach the packaged chart:

```bash
# After the release is created, upload the chart package
gh release upload v0.1.0 charts/packages/agent-operator-0.1.0.tgz
```

## Automated Release (Optional)

To automate Helm chart releases, you can create a separate workflow file that doesn't require workflow modification permissions. This would need to be added manually by someone with the appropriate permissions.

### Recommended Workflow Structure

Create `.github/workflows/helm-release.yml`:

```yaml
name: Helm Release

on:
  release:
    types: [published]

jobs:
  helm-package:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4

      - name: Install Helm
        uses: azure/setup-helm@v4
        with:
          version: v3.14.0

      - name: Update chart versions
        run: |
          VERSION="${{ github.event.release.tag_name }}"
          SEMVER="${VERSION#v}"

          # Update Chart.yaml
          sed -i "s/^version: .*/version: ${SEMVER}/" charts/agent-operator/Chart.yaml
          sed -i "s/^appVersion: .*/appVersion: \"${SEMVER}\"/" charts/agent-operator/Chart.yaml

      - name: Package chart
        run: |
          mkdir -p charts/packages
          helm package charts/agent-operator -d charts/packages/

      - name: Generate index
        run: |
          helm repo index charts/packages/ --url https://github.com/${{ github.repository }}/releases/download/${{ github.event.release.tag_name }}/

      - name: Upload chart to release
        env:
          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release upload ${{ github.event.release.tag_name }} charts/packages/*.tgz
```

## Using the Helm Chart

### From GitHub Release

Users can install directly from the GitHub release:

```bash
# Download the chart
wget https://github.com/jcwearn/agent-operator/releases/download/v0.1.0/agent-operator-0.1.0.tgz

# Install from the downloaded file
helm install agent-operator agent-operator-0.1.0.tgz --namespace agent-system --create-namespace
```

### From Source

Users can also install directly from the source repository:

```bash
git clone https://github.com/jcwearn/agent-operator.git
cd agent-operator
helm install agent-operator ./charts/agent-operator --namespace agent-system --create-namespace
```

## Chart Repository (Optional)

For a better user experience, consider setting up GitHub Pages to host a Helm chart repository:

### 1. Create gh-pages branch

```bash
git checkout --orphan gh-pages
git rm -rf .
echo "# Agent Operator Helm Repository" > README.md
git add README.md
git commit -m "Initial commit"
git push origin gh-pages
```

### 2. Update workflow to publish to gh-pages

Modify the Helm release workflow to push the index to the gh-pages branch:

```yaml
- name: Publish to GitHub Pages
  run: |
    git config user.name "github-actions[bot]"
    git config user.email "github-actions[bot]@users.noreply.github.com"
    git checkout gh-pages
    cp charts/packages/index.yaml .
    cp charts/packages/*.tgz .
    git add index.yaml *.tgz
    git commit -m "Release ${{ github.event.release.tag_name }}"
    git push origin gh-pages
```

### 3. Enable GitHub Pages

In repository settings, enable GitHub Pages from the gh-pages branch.

### 4. Users can then add the repo

```bash
helm repo add agent-operator https://jcwearn.github.io/agent-operator
helm repo update
helm install agent-operator agent-operator/agent-operator --namespace agent-system
```

## Version Management

- **Chart version**: Semantic version of the chart itself (e.g., 0.1.0)
- **AppVersion**: Version of the agent-operator application (should match the Docker image tag)

Both should be updated together for each release to maintain consistency.

## Testing Before Release

Always test the packaged chart before releasing:

```bash
# Lint the chart
helm lint charts/agent-operator

# Template and check output
helm template test-release charts/agent-operator --namespace test-system

# Test install in a cluster
helm install test-release charts/agent-operator \
  --namespace test-system \
  --create-namespace \
  --dry-run --debug
```

## Rollback

If a chart release has issues:

```bash
# Users can rollback to previous version
helm rollback agent-operator --namespace agent-system

# Or install a specific older version
helm install agent-operator agent-operator-0.0.9.tgz --namespace agent-system
```
