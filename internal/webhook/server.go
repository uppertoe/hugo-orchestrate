// Package webhook serves the HTTP surface: GitHub webhook intake with HMAC,
// body-size, replay and branch gating, plus health/readiness/status.
package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
	"github.com/uppertoe/hugo-orchestrate/internal/state"
)

// Enqueuer schedules a site build; implemented by queue.Manager.
type Enqueuer interface {
	Enqueue(slug, reason string) (coalesced bool, err error)
}

// Server holds the HTTP handlers.
type Server struct {
	sites    map[string]*config.Site
	enq      Enqueuer
	states   *state.Store
	log      *slog.Logger
	maxBody  int64
	replay   *replayCache
	ready    atomic.Bool
	draining atomic.Bool
}

// NewServer wires the handlers. sites is keyed by slug.
func NewServer(sites []*config.Site, enq Enqueuer, states *state.Store, log *slog.Logger, maxBody int64, replayWindow time.Duration) *Server {
	m := make(map[string]*config.Site, len(sites))
	for _, s := range sites {
		m[s.Slug] = s
	}
	return &Server{
		sites:   m,
		enq:     enq,
		states:  states,
		log:     log,
		maxBody: maxBody,
		replay:  newReplayCache(replayWindow, nil),
	}
}

// SetReady marks initial sync complete; /readyz turns 200.
func (s *Server) SetReady() { s.ready.Store(true) }

// SetDraining makes webhook intake refuse new triggers during shutdown.
func (s *Server) SetDraining() { s.draining.Store(true) }

// Handler returns the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/{slug}", s.handleWebhook)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "initial sync in progress", http.StatusServiceUnavailable)
	})
	mux.HandleFunc("GET /status", s.handleStatus)
	return mux
}

type pushPayload struct {
	Ref string `json:"ref"`
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	log := s.log.With("slug", slug, "remote", r.RemoteAddr)

	site, ok := s.sites[slug]
	if !ok || !site.WebhookEnabled() {
		http.Error(w, "unknown webhook", http.StatusNotFound)
		return
	}
	if s.draining.Load() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
	body, err := readAll(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			log.Warn("webhook rejected", "reason", "body too large")
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	if !VerifySignature(site.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		log.Warn("webhook rejected", "reason", "bad signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if event != "push" {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "event is not push"})
		return
	}

	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		http.Error(w, "missing X-GitHub-Delivery", http.StatusBadRequest)
		return
	}
	if s.replay.CheckAndRecord(delivery) {
		log.Warn("webhook rejected", "reason", "replayed delivery", "delivery", delivery)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "replayed delivery"})
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	want := "refs/heads/" + site.Branch
	if payload.Ref != want {
		writeJSON(w, http.StatusAccepted, map[string]string{
			"status": "ignored", "reason": "ref " + payload.Ref + " does not match " + want,
		})
		return
	}

	coalesced, err := s.enq.Enqueue(slug, "webhook")
	if err != nil {
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	status := "queued"
	if coalesced {
		status = "coalesced"
	}
	log.Info("webhook accepted", "delivery", delivery, "status", status)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": status})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	out := make(map[string]any, len(s.sites))
	for slug := range s.sites {
		st, err := s.states.Read(slug)
		if err != nil {
			out[slug] = map[string]string{"error": "unreadable state"}
			continue
		}
		if st == nil {
			out[slug] = map[string]string{"status": "never built"}
			continue
		}
		out[slug] = st
	}
	writeJSON(w, http.StatusOK, out)
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
