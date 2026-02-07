package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

func (s *APIServer) handleStreamLogs(w http.ResponseWriter, r *http.Request) {
	if s.clientset == nil {
		respondError(w, http.StatusServiceUnavailable, "log streaming not available")
		return
	}

	name := chi.URLParam(r, "name")

	// Look up the CodingTask.
	var task agentsv1alpha1.CodingTask
	key := client.ObjectKey{Name: name, Namespace: s.taskNamespace}
	if err := s.client.Get(r.Context(), key, &task); err != nil {
		respondError(w, http.StatusNotFound, "task not found")
		return
	}

	// Find the active AgentRun's pod.
	podName := ""
	for i := len(task.Status.AgentRuns) - 1; i >= 0; i-- {
		ref := task.Status.AgentRuns[i]
		if ref.Phase == agentsv1alpha1.AgentRunPhaseRunning || ref.Phase == agentsv1alpha1.AgentRunPhasePending {
			// Look up the AgentRun to get its pod name.
			var run agentsv1alpha1.AgentRun
			runKey := client.ObjectKey{Name: ref.Name, Namespace: s.taskNamespace}
			if err := s.client.Get(r.Context(), runKey, &run); err == nil {
				podName = run.Status.PodName
			}
			break
		}
	}

	if podName == "" {
		respondError(w, http.StatusNotFound, "no active pod found for task")
		return
	}

	// Stream logs via SSE.
	follow := r.URL.Query().Get("follow") != "false"
	logOpts := &corev1.PodLogOptions{
		Follow: follow,
	}

	req := s.clientset.CoreV1().Pods(s.taskNamespace).GetLogs(podName, logOpts)
	stream, err := req.Stream(r.Context())
	if err != nil {
		s.log.Error(err, "failed to open log stream", "pod", podName)
		respondError(w, http.StatusInternalServerError, "failed to stream logs")
		return
	}
	defer stream.Close()

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()

		// Check if the client disconnected.
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		s.log.Error(err, "error reading log stream")
	}

	// Send end-of-stream event.
	fmt.Fprintf(w, "event: done\ndata: stream ended\n\n")
	flusher.Flush()
}
