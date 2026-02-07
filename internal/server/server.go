package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ghclient "github.com/jcwearn/agent-operator/internal/github"
)

// APIServer implements manager.Runnable and serves the HTTP API.
type APIServer struct {
	client    client.Client
	clientset kubernetes.Interface
	router    chi.Router
	addr      string
	log       logr.Logger
	hub       *Hub

	// GitHub integration (optional).
	githubClient       *ghclient.Client
	githubWebhookSecret []byte

	// Default secret refs for tasks created via API.
	anthropicSecretName string
	anthropicSecretKey  string
	taskNamespace       string
}

// Option configures the APIServer.
type Option func(*APIServer)

// WithGitHubClient sets the GitHub client for webhook handling and notifications.
func WithGitHubClient(c *ghclient.Client) Option {
	return func(s *APIServer) {
		s.githubClient = c
	}
}

// WithGitHubWebhookSecret sets the webhook secret for signature validation.
func WithGitHubWebhookSecret(secret []byte) Option {
	return func(s *APIServer) {
		s.githubWebhookSecret = secret
	}
}

// WithAnthropicSecret sets the default Anthropic API key secret reference.
func WithAnthropicSecret(name, key string) Option {
	return func(s *APIServer) {
		s.anthropicSecretName = name
		s.anthropicSecretKey = key
	}
}

// WithTaskNamespace sets the namespace where CodingTasks are created.
func WithTaskNamespace(ns string) Option {
	return func(s *APIServer) {
		s.taskNamespace = ns
	}
}

// WithClientset sets the typed Kubernetes clientset for pod log streaming.
func WithClientset(cs kubernetes.Interface) Option {
	return func(s *APIServer) {
		s.clientset = cs
	}
}

// NewAPIServer creates a new API server.
func NewAPIServer(c client.Client, addr string, opts ...Option) *APIServer {
	s := &APIServer{
		client:              c,
		addr:                addr,
		log:                 ctrl.Log.WithName("api-server"),
		hub:                 NewHub(),
		anthropicSecretName: "anthropic-api-key",
		anthropicSecretKey:  "api-key",
		taskNamespace:       "agent-system",
	}

	for _, opt := range opts {
		opt(s)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/healthz", s.handleHealthz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/tasks", s.handleCreateTask)
		r.Get("/tasks", s.handleListTasks)
		r.Get("/tasks/{name}", s.handleGetTask)
		r.Delete("/tasks/{name}", s.handleDeleteTask)
		r.Post("/tasks/{name}/approve", s.handleApproveTask)
		r.Get("/tasks/{name}/logs", s.handleStreamLogs)

		r.Post("/webhooks/github", s.handleGitHubWebhook)

		r.Get("/ws", s.handleWebSocket)
	})

	s.router = r
	return s
}

// GetHub returns the WebSocket hub for broadcasting events.
func (s *APIServer) GetHub() *Hub {
	return s.hub
}

// Start implements manager.Runnable.
func (s *APIServer) Start(ctx context.Context) error {
	s.log.Info("starting API server", "addr", s.addr)

	// Start WebSocket hub.
	go s.hub.Run(ctx)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		s.log.Info("shutting down API server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.log.Error(err, "error shutting down API server")
		}
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *APIServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// respondJSON writes a JSON response.
func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// respondError writes a JSON error response.
func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}
