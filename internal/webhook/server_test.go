package webhook

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/config"
	"github.com/uppertoe/hugo-orchestrate/internal/state"
)

const secret = "s3cret"

type fakeEnqueuer struct {
	calls []string
}

func (f *fakeEnqueuer) Enqueue(slug, reason string) (bool, error) {
	f.calls = append(f.calls, slug+":"+reason)
	return false, nil
}

func newTestServer(t *testing.T) (*Server, *fakeEnqueuer) {
	t.Helper()
	states, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sites := []*config.Site{
		{Slug: "docs", Branch: "main", WebhookSecret: secret},
		{Slug: "nohook", Branch: "main"},
	}
	enq := &fakeEnqueuer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(sites, enq, states, log, 1024, 10*time.Minute), enq
}

func sign(body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func post(t *testing.T, h http.Handler, path string, body []byte, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sign(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-GitHub-Delivery", "delivery-"+t.Name())
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var pushMain = []byte(`{"ref":"refs/heads/main"}`)

func TestWebhookHappyPath(t *testing.T) {
	srv, enq := newTestServer(t)
	rec := post(t, srv.Handler(), "/webhook/docs", pushMain)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}
	if len(enq.calls) != 1 || enq.calls[0] != "docs:webhook" {
		t.Errorf("enqueue calls = %v", enq.calls)
	}
}

func TestWebhookRejections(t *testing.T) {
	srv, enq := newTestServer(t)
	h := srv.Handler()

	cases := []struct {
		name string
		do   func() *httptest.ResponseRecorder
		code int
	}{
		{"unknown slug", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/ghost", pushMain)
		}, http.StatusNotFound},
		{"site without webhook", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/nohook", pushMain)
		}, http.StatusNotFound},
		{"bad signature", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/docs", pushMain,
				withHeader("X-Hub-Signature-256", "sha256=deadbeef"))
		}, http.StatusUnauthorized},
		{"missing signature", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/docs", pushMain, withHeader("X-Hub-Signature-256", ""))
		}, http.StatusUnauthorized},
		{"oversize body", func() *httptest.ResponseRecorder {
			big := append([]byte(`{"ref":"`), bytes.Repeat([]byte("x"), 2048)...)
			big = append(big, []byte(`"}`)...)
			return post(t, h, "/webhook/docs", big)
		}, http.StatusRequestEntityTooLarge},
		{"missing delivery id", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/docs", pushMain, withHeader("X-GitHub-Delivery", ""))
		}, http.StatusBadRequest},
		{"invalid json", func() *httptest.ResponseRecorder {
			return post(t, h, "/webhook/docs", []byte("not json"))
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		if rec := tc.do(); rec.Code != tc.code {
			t.Errorf("%s: code = %d, want %d (body %s)", tc.name, rec.Code, tc.code, rec.Body)
		}
	}
	if len(enq.calls) != 0 {
		t.Errorf("rejected requests must not enqueue: %v", enq.calls)
	}
}

func TestWebhookIgnoredEvents(t *testing.T) {
	srv, enq := newTestServer(t)
	h := srv.Handler()

	if rec := post(t, h, "/webhook/docs", []byte(`{"zen":"hi"}`), withHeader("X-GitHub-Event", "ping")); rec.Code != http.StatusOK {
		t.Errorf("ping: code = %d", rec.Code)
	}
	if rec := post(t, h, "/webhook/docs", pushMain, withHeader("X-GitHub-Event", "issues")); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("non-push: code = %d body = %s", rec.Code, rec.Body)
	}
	other := []byte(`{"ref":"refs/heads/feature"}`)
	if rec := post(t, h, "/webhook/docs", other); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "does not match") {
		t.Errorf("wrong branch: code = %d body = %s", rec.Code, rec.Body)
	}
	if len(enq.calls) != 0 {
		t.Errorf("ignored events must not enqueue: %v", enq.calls)
	}
}

func TestWebhookReplay(t *testing.T) {
	srv, enq := newTestServer(t)
	h := srv.Handler()
	opt := withHeader("X-GitHub-Delivery", "dup-1")

	if rec := post(t, h, "/webhook/docs", pushMain, opt); rec.Code != http.StatusAccepted {
		t.Fatalf("first delivery: %d", rec.Code)
	}
	rec := post(t, h, "/webhook/docs", pushMain, opt)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "replayed") {
		t.Errorf("replay: code = %d body = %s", rec.Code, rec.Body)
	}
	if len(enq.calls) != 1 {
		t.Errorf("replayed delivery must not enqueue twice: %v", enq.calls)
	}
}

func TestReadyz(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()

	get := func(path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if get("/healthz") != http.StatusOK {
		t.Error("healthz should always be 200")
	}
	if get("/readyz") != http.StatusServiceUnavailable {
		t.Error("readyz should be 503 before initial sync")
	}
	srv.SetReady()
	if get("/readyz") != http.StatusOK {
		t.Error("readyz should be 200 after initial sync")
	}
	if get("/status") != http.StatusOK {
		t.Error("status should be 200")
	}
}

func TestDraining(t *testing.T) {
	srv, enq := newTestServer(t)
	srv.SetDraining()
	rec := post(t, srv.Handler(), "/webhook/docs", pushMain)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("draining: code = %d", rec.Code)
	}
	if len(enq.calls) != 0 {
		t.Errorf("draining must not enqueue: %v", enq.calls)
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte("payload")
	if !VerifySignature(secret, body, sign(body)) {
		t.Error("valid signature rejected")
	}
	if VerifySignature(secret, []byte("tampered"), sign(body)) {
		t.Error("tampered body accepted")
	}
	if VerifySignature(secret, body, "") || VerifySignature(secret, body, "sha1=abc") {
		t.Error("malformed header accepted")
	}
	if VerifySignature("", body, sign(body)) {
		t.Error("empty secret accepted")
	}
}
