// Package config loads and validates the orchestrator's environment
// configuration and the sites.yaml site definitions. All validation is
// fail-fast: a misconfigured service refuses to start.
package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// Env holds the operational configuration sourced from ORCH_* environment
// variables. Durations use Go time.ParseDuration syntax (e.g. "90s", "10m").
type Env struct {
	StaticRoot          string
	OutputRoot          string
	WorkRoot            string
	SitesConfig         string
	WebhookListen       string
	LogLevel            string
	MaxConcurrentBuilds int
	BuildTimeout        time.Duration
	GitTimeout          time.Duration
	OperationRetries    int
	RetryBackoff        time.Duration
	BuildRetentionCount int
	ShutdownGrace       time.Duration
	WebhookMaxBodyBytes int64
	WebhookReplayWindow time.Duration
	HugoManifestPath    string
	HugoBinRoot         string
}

// Getenv is the lookup function shape used by LoadEnv, matching os.Getenv.
type Getenv func(string) string

// LoadEnv reads the ORCH_* environment variables, applying defaults and
// validating values. It returns an error describing the first invalid
// variable encountered.
func LoadEnv(getenv Getenv) (*Env, error) {
	e := &Env{}

	e.StaticRoot = stringVar(getenv, "ORCH_STATIC_ROOT", "/srv/static")
	if !filepath.IsAbs(e.StaticRoot) {
		return nil, fmt.Errorf("ORCH_STATIC_ROOT must be an absolute path, got %q", e.StaticRoot)
	}
	e.OutputRoot = stringVar(getenv, "ORCH_OUTPUT_ROOT", filepath.Join(e.StaticRoot, "www"))
	e.WorkRoot = stringVar(getenv, "ORCH_WORK_ROOT", filepath.Join(e.StaticRoot, "work"))
	for name, p := range map[string]string{"ORCH_OUTPUT_ROOT": e.OutputRoot, "ORCH_WORK_ROOT": e.WorkRoot} {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("%s must be an absolute path, got %q", name, p)
		}
	}
	if e.OutputRoot == e.WorkRoot {
		return nil, fmt.Errorf("ORCH_OUTPUT_ROOT and ORCH_WORK_ROOT must differ, both are %q", e.OutputRoot)
	}

	e.SitesConfig = stringVar(getenv, "ORCH_SITES_CONFIG", "/config/sites.yaml")
	e.WebhookListen = stringVar(getenv, "ORCH_WEBHOOK_LISTEN", "0.0.0.0:8080")
	e.LogLevel = stringVar(getenv, "ORCH_LOG_LEVEL", "info")
	e.HugoManifestPath = stringVar(getenv, "ORCH_HUGO_MANIFEST_PATH", "/etc/orchestrator/hugo-manifest.txt")
	e.HugoBinRoot = stringVar(getenv, "ORCH_HUGO_BIN_ROOT", "/opt/hugo")

	var err error
	if e.MaxConcurrentBuilds, err = intVar(getenv, "ORCH_MAX_CONCURRENT_BUILDS", 2, 1); err != nil {
		return nil, err
	}
	if e.OperationRetries, err = intVar(getenv, "ORCH_OPERATION_RETRIES", 2, 0); err != nil {
		return nil, err
	}
	if e.BuildRetentionCount, err = intVar(getenv, "ORCH_BUILD_RETENTION_COUNT", 5, 0); err != nil {
		return nil, err
	}
	maxBody, err := intVar(getenv, "ORCH_WEBHOOK_MAX_BODY_BYTES", 262144, 1)
	if err != nil {
		return nil, err
	}
	e.WebhookMaxBodyBytes = int64(maxBody)

	if e.BuildTimeout, err = durationVar(getenv, "ORCH_BUILD_TIMEOUT", 10*time.Minute); err != nil {
		return nil, err
	}
	if e.GitTimeout, err = durationVar(getenv, "ORCH_GIT_TIMEOUT", 2*time.Minute); err != nil {
		return nil, err
	}
	if e.RetryBackoff, err = durationVar(getenv, "ORCH_RETRY_BACKOFF", time.Second); err != nil {
		return nil, err
	}
	if e.ShutdownGrace, err = durationVar(getenv, "ORCH_SHUTDOWN_GRACE", 30*time.Second); err != nil {
		return nil, err
	}
	if e.WebhookReplayWindow, err = durationVar(getenv, "ORCH_WEBHOOK_REPLAY_WINDOW", 10*time.Minute); err != nil {
		return nil, err
	}

	return e, nil
}

func stringVar(getenv Getenv, name, def string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return def
}

func intVar(getenv Getenv, name string, def, min int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: not an integer: %q", name, raw)
	}
	if v < min {
		return 0, fmt.Errorf("%s: must be >= %d, got %d", name, min, v)
	}
	return v, nil
}

func durationVar(getenv Getenv, name string, def time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q (use Go syntax, e.g. \"90s\", \"10m\"): %w", name, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: duration must be positive, got %q", name, raw)
	}
	return d, nil
}
