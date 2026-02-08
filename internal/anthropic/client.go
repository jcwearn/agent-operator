package anthropic

import (
	"context"
	"fmt"
	"sync"

	sdkanthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Client wraps the Anthropic SDK client with lazy initialization from a
// Kubernetes secret.
type Client struct {
	k8sClient  client.Client
	namespace  string
	secretName string
	secretKey  string

	mu        sync.Mutex
	sdkClient *sdkanthropic.Client
}

// NewClient creates a new Anthropic client wrapper. The underlying SDK client
// is lazily initialized on first use by reading the API key from the specified
// Kubernetes secret.
func NewClient(k8sClient client.Client, namespace, secretName, secretKey string) *Client {
	return &Client{
		k8sClient:  k8sClient,
		namespace:  namespace,
		secretName: secretName,
		secretKey:  secretKey,
	}
}

// getClient returns the cached SDK client, initializing it if needed.
func (c *Client) getClient(ctx context.Context) (*sdkanthropic.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sdkClient != nil {
		return c.sdkClient, nil
	}

	// Read API key from Kubernetes secret.
	var secret corev1.Secret
	key := client.ObjectKey{Name: c.secretName, Namespace: c.namespace}
	if err := c.k8sClient.Get(ctx, key, &secret); err != nil {
		return nil, fmt.Errorf("failed to read Anthropic API key secret %s/%s: %w", c.namespace, c.secretName, err)
	}

	apiKey, ok := secret.Data[c.secretKey]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s/%s", c.secretKey, c.namespace, c.secretName)
	}

	sdkClient := sdkanthropic.NewClient(
		option.WithAPIKey(string(apiKey)),
	)
	c.sdkClient = &sdkClient
	return c.sdkClient, nil
}

// Chat sends a non-streaming message request.
func (c *Client) Chat(ctx context.Context, params sdkanthropic.MessageNewParams) (*sdkanthropic.Message, error) {
	sdk, err := c.getClient(ctx)
	if err != nil {
		return nil, err
	}
	msg, err := sdk.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic chat error: %w", err)
	}
	return msg, nil
}

// ChatStream sends a streaming message request.
func (c *Client) ChatStream(ctx context.Context, params sdkanthropic.MessageNewParams) (*ssestream.Stream[sdkanthropic.MessageStreamEventUnion], error) {
	sdk, err := c.getClient(ctx)
	if err != nil {
		return nil, err
	}
	return sdk.Messages.NewStreaming(ctx, params), nil
}
