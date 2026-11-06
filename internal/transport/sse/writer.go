// Package sse writes Server-Sent Events frames and flushes them to the client.
// It is transport plumbing only: it knows nothing about interview state.
package sse

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type Writer struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func NewWriter(w http.ResponseWriter) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Flush headers now so clients see the stream open immediately instead of
	// blocking on the first event or heartbeat.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &Writer{w: w, flusher: flusher}, nil
}

// WriteEvent writes one SSE frame. id is the event_id cursor used by
// Last-Event-ID reconnects; event is the SSE event name.
func (w *Writer) WriteEvent(id, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sse payload: %w", err)
	}

	if _, err := fmt.Fprintf(w.w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, data); err != nil {
		return fmt.Errorf("write sse frame: %w", err)
	}
	w.flusher.Flush()
	return nil
}

func (w *Writer) Heartbeat() {
	_, _ = fmt.Fprint(w.w, ": ping\n\n")
	w.flusher.Flush()
}
