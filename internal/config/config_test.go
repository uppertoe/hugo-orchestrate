package config

import (
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestLoadEnvDefaults(t *testing.T) {
	e, err := LoadEnv(envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if e.StaticRoot != "/srv/static" || e.OutputRoot != "/srv/static/www" || e.WorkRoot != "/srv/static/work" {
		t.Errorf("unexpected roots: %+v", e)
	}
	if e.MaxConcurrentBuilds != 2 || e.BuildTimeout != 10*time.Minute || e.GitTimeout != 2*time.Minute {
		t.Errorf("unexpected defaults: %+v", e)
	}
	if e.WebhookMaxBodyBytes != 262144 || e.WebhookReplayWindow != 10*time.Minute {
		t.Errorf("unexpected webhook defaults: %+v", e)
	}
}

func TestLoadEnvOverridesAndErrors(t *testing.T) {
	e, err := LoadEnv(envMap(map[string]string{
		"ORCH_STATIC_ROOT":   "/data",
		"ORCH_BUILD_TIMEOUT": "90s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if e.OutputRoot != "/data/www" || e.BuildTimeout != 90*time.Second {
		t.Errorf("override not applied: %+v", e)
	}

	for name, vars := range map[string]map[string]string{
		"bad duration":      {"ORCH_BUILD_TIMEOUT": "10minutes"},
		"negative duration": {"ORCH_GIT_TIMEOUT": "-5s"},
		"bad int":           {"ORCH_MAX_CONCURRENT_BUILDS": "two"},
		"zero concurrency":  {"ORCH_MAX_CONCURRENT_BUILDS": "0"},
		"relative root":     {"ORCH_STATIC_ROOT": "srv/static"},
		"same roots":        {"ORCH_OUTPUT_ROOT": "/x", "ORCH_WORK_ROOT": "/x"},
	} {
		if _, err := LoadEnv(envMap(vars)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

const validSite = `
sites:
  - slug: docs
    repo: https://github.com/org/docs-site.git
    webhook:
      secret_env: DOCS_WEBHOOK_SECRET
`

func TestParseSitesDefaults(t *testing.T) {
	sites, err := ParseSites([]byte(validSite), envMap(map[string]string{"DOCS_WEBHOOK_SECRET": "s3cret"}))
	if err != nil {
		t.Fatal(err)
	}
	s := sites[0]
	if s.Branch != "main" || s.PublishDir != "docs" || s.HugoEnv != "production" {
		t.Errorf("defaults not applied: %+v", s)
	}
	if !s.WebhookEnabled() || s.WebhookSecret != "s3cret" {
		t.Errorf("webhook secret not resolved")
	}
	if s.Token != "" {
		t.Errorf("expected anonymous clone for absent auth")
	}
}

func TestParseSitesErrors(t *testing.T) {
	cases := map[string]struct {
		yaml string
		env  map[string]string
		want string
	}{
		"ssh repo rejected": {
			yaml: "sites:\n  - slug: a\n    repo: git@github.com:org/a.git\n",
			want: "https",
		},
		"credentials in url": {
			yaml: "sites:\n  - slug: a\n    repo: https://user:pass@github.com/org/a.git\n",
			want: "credentials",
		},
		"empty token env is hard error": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    auth:\n      token_env: MISSING_TOKEN\n",
			want: "anonymous fallback",
		},
		"auth without token_env": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    auth: {}\n",
			want: "token_env",
		},
		"empty webhook secret": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    webhook:\n      secret_env: NOPE\n",
			want: "empty value",
		},
		"bad provider": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    webhook:\n      provider: gitlab\n      secret_env: S\n",
			env:  map[string]string{"S": "x"},
			want: "provider",
		},
		"duplicate slug": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n  - slug: a\n    repo: https://github.com/org/b.git\n    publish_dir: b\n",
			want: "duplicate slug",
		},
		"duplicate publish_dir": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    publish_dir: shared\n  - slug: b\n    repo: https://github.com/org/b.git\n    publish_dir: shared\n",
			want: "publish_dir",
		},
		"bad slug": {
			yaml: "sites:\n  - slug: 'Bad Slug'\n    repo: https://github.com/org/a.git\n",
			want: "slug",
		},
		"bad hugo version": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    build:\n      hugo_version: latest\n",
			want: "hugo_version",
		},
		"unknown field": {
			yaml: "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    poll_interval: 60s\n",
			want: "field",
		},
		"no sites": {
			yaml: "sites: []\n",
			want: "no sites",
		},
	}
	for name, tc := range cases {
		_, err := ParseSites([]byte(tc.yaml), envMap(tc.env))
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, err, tc.want)
		}
	}
}

func TestParseSitesTokenResolution(t *testing.T) {
	yaml := "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    auth:\n      token_env: MY_TOKEN\n"
	sites, err := ParseSites([]byte(yaml), envMap(map[string]string{"MY_TOKEN": "tok123"}))
	if err != nil {
		t.Fatal(err)
	}
	if sites[0].Token != "tok123" {
		t.Errorf("token not resolved")
	}
}

func TestSiteBuildTimeout(t *testing.T) {
	yaml := "sites:\n  - slug: a\n    repo: https://github.com/org/a.git\n    build:\n      timeout: 8m\n"
	sites, err := ParseSites([]byte(yaml), envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(sites[0].Build.Timeout) != 8*time.Minute {
		t.Errorf("timeout = %v, want 8m", sites[0].Build.Timeout)
	}
}
