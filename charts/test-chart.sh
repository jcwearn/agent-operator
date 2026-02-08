#!/bin/bash
# Script to test the agent-operator Helm chart

set -e

CHART_DIR="charts/agent-operator"

echo "=== Testing agent-operator Helm chart ==="
echo

# Check if helm is installed
if ! command -v helm &> /dev/null; then
    echo "ERROR: helm is not installed"
    echo "Please install helm: https://helm.sh/docs/intro/install/"
    exit 1
fi

echo "✓ Helm version:"
helm version --short
echo

# Lint the chart
echo "=== Linting chart ==="
helm lint "$CHART_DIR"
echo

# Template the chart with default values
echo "=== Templating with default values ==="
helm template test-release "$CHART_DIR" \
    --namespace test-system > /tmp/rendered.yaml
echo "✓ Chart rendered successfully"
echo "  Output saved to: /tmp/rendered.yaml"
echo

# Template with custom values
echo "=== Templating with custom values ==="
helm template test-release "$CHART_DIR" \
    --namespace test-system \
    --set operatorImage.tag=0.1.0 \
    --set agentRunnerImage.tag=0.1.0 \
    --set secrets.create=true \
    --set networkPolicy.enabled=true > /tmp/rendered-custom.yaml
echo "✓ Chart rendered with custom values"
echo "  Output saved to: /tmp/rendered-custom.yaml"
echo

# Show what would be installed
echo "=== Resources that would be created ==="
helm template test-release "$CHART_DIR" \
    --namespace test-system \
    | grep "^kind:" | sort | uniq -c
echo

# Dry run install
echo "=== Dry run installation ==="
helm install test-release "$CHART_DIR" \
    --namespace test-system \
    --create-namespace \
    --dry-run --debug 2>&1 | tail -20
echo

echo "=== All tests passed! ==="
echo
echo "To package the chart:"
echo "  helm package $CHART_DIR -d charts/packages/"
echo
echo "To install the chart:"
echo "  helm install agent-operator $CHART_DIR --namespace agent-system --create-namespace"
