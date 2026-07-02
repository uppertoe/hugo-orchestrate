// Command orchestrator builds Hugo sites from Git on GitHub webhooks and
// publishes each atomically to its own output directory for a host-side
// Caddy to serve.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
	"github.com/uppertoe/hugo-orchestrate/internal/hugo"
	"github.com/uppertoe/hugo-orchestrate/internal/observability"
	"github.com/uppertoe/hugo-orchestrate/internal/orchestrator"
	"github.com/uppertoe/hugo-orchestrate/internal/publish"
	"github.com/uppertoe/hugo-orchestrate/internal/queue"
	"github.com/uppertoe/hugo-orchestrate/internal/state"
	"github.com/uppertoe/hugo-orchestrate/internal/webhook"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false,
		"probe /healthz on the configured listen port and exit 0/1 (for container healthchecks)")
	flag.Parse()
	if *healthcheck {
		os.Exit(probeHealth(os.Getenv))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// probeHealth GETs /healthz over loopback on the ORCH_WEBHOOK_LISTEN port.
// The runtime image ships no curl/wget, so the compose healthcheck re-execs
// this binary instead.
func probeHealth(getenv func(string) string) int {
	listen := getenv("ORCH_WEBHOOK_LISTEN")
	if listen == "" {
		listen = "0.0.0.0:8080"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: invalid ORCH_WEBHOOK_LISTEN:", err)
		return 1
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.Status)
		return 1
	}
	return 0
}

func run() error {
	env, err := config.LoadEnv(os.Getenv)
	if err != nil {
		return err
	}
	log := observability.NewLogger(env.LogLevel)
	layout := config.NewLayout(env)

	sites, err := config.LoadSites(env.SitesConfig, os.Getenv)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git binary not found in PATH: %w", err)
	}

	for _, dir := range []string{layout.WorkRoot, layout.OutputRoot, layout.HomeDir(), layout.StateDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// Single-instance guard: the queue and replay cache are in-memory and
	// publishes race across processes. Exactly one instance per work volume.
	unlock, err := acquireLock(layout.LockPath())
	if err != nil {
		return err
	}
	defer unlock()

	// A crash mid-publish can leave stale staging/.__prev dirs; sweep before
	// the initial sync so they don't accumulate or shadow a live dir.
	if removed, err := publish.SweepOrphans(env.OutputRoot); err != nil {
		return fmt.Errorf("sweep orphaned publish dirs: %w", err)
	} else if len(removed) > 0 {
		log.Warn("removed orphaned publish dirs from a previous crash", "dirs", removed)
	}

	catalog, err := hugo.LoadCatalog(env.HugoManifestPath, env.HugoBinRoot)
	if err != nil {
		return err
	}
	log.Info("hugo catalog loaded", "default", catalog.Default, "versions", catalog.Versions)

	states, err := state.NewStore(layout.StateDir())
	if err != nil {
		return err
	}
	orch, err := orchestrator.New(env, sites, catalog, states, log)
	if err != nil {
		return err
	}

	slugs := make([]string, len(sites))
	for i, s := range sites {
		slugs[i] = s.Slug
	}
	qm := queue.NewManager(slugs, env.MaxConcurrentBuilds, orch.BuildSite)

	srv := webhook.NewServer(sites, qm, states, log, env.WebhookMaxBodyBytes, env.WebhookReplayWindow)
	httpServer := &http.Server{
		Addr:              env.WebhookListen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Bodies are capped at WebhookMaxBodyBytes; these bound how long a
		// client may drip-feed one so slow requests can't pin goroutines.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}
	httpErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", env.WebhookListen)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	// Initial unconditional build of every site; /readyz flips once the
	// first pass completes. Per-site failures are logged, not fatal.
	for _, s := range sites {
		if _, err := qm.Enqueue(s.Slug, "startup"); err != nil {
			return err
		}
	}
	go func() {
		if err := orch.WaitFirstPass(context.Background()); err == nil {
			srv.SetReady()
			log.Info("initial sync complete; ready")
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-stop:
		log.Info("shutting down", "signal", sig.String(), "grace", env.ShutdownGrace.String())
	case err := <-httpErr:
		return fmt.Errorf("http server: %w", err)
	}

	srv.SetDraining()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), env.ShutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "error", err)
	}
	qm.Stop(env.ShutdownGrace)
	log.Info("shutdown complete")
	return nil
}

// acquireLock takes a non-blocking exclusive flock on path, refusing to
// start if another instance holds it.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another orchestrator instance holds %s (run exactly one instance per output volume): %w", path, err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
