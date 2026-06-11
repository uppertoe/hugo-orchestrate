package gitsource

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
)

func TestRedactorScrubsTokenAndHeader(t *testing.T) {
	token := "ghp_supersecret123"
	r := newRedactor(token)
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	in := "fatal: auth failed for " + token + " header " + b64
	out := r(in)
	if strings.Contains(out, token) || strings.Contains(out, b64) {
		t.Errorf("token leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("redaction marker missing: %q", out)
	}
}

func TestGitEnvAuthHeaderScopedToHost(t *testing.T) {
	c := New(time.Minute, "/work/home")
	site := &config.Site{
		Slug:  "docs",
		Repo:  "https://github.com/org/docs.git",
		Token: "tok123",
	}
	env, err := c.gitEnv(site)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://github.com/.extraHeader") {
		t.Errorf("auth header not scoped to origin host:\n%s", joined)
	}
	if strings.Contains(joined, "tok123") {
		t.Error("raw token must not appear in env values directly")
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:tok123"))
	if !strings.Contains(joined, "AUTHORIZATION: Basic "+wantB64) {
		t.Error("expected basic auth header value")
	}
	if !strings.Contains(joined, "HOME=/work/home") || !strings.Contains(joined, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Error("read-only rootfs git env missing HOME/GIT_CONFIG_GLOBAL")
	}
	if !strings.Contains(joined, "GIT_TERMINAL_PROMPT=0") {
		t.Error("terminal prompts must be disabled")
	}
}

func TestGitEnvAnonymous(t *testing.T) {
	c := New(time.Minute, "/work/home")
	env, err := c.gitEnv(&config.Site{Slug: "pub", Repo: "https://github.com/org/pub.git"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(env, "\n"), "GIT_CONFIG_COUNT") {
		t.Error("anonymous site must not get auth config")
	}
}
