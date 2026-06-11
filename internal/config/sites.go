package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	nameRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	hugoVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// Duration wraps time.Duration for YAML decoding using Go duration syntax.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("invalid duration %q (use Go syntax, e.g. \"8m\"): %w", raw, err)
	}
	if v <= 0 {
		return fmt.Errorf("duration must be positive, got %q", raw)
	}
	*d = Duration(v)
	return nil
}

// Site is one validated entry from sites.yaml. Token and WebhookSecret are
// resolved from the environment at load time and must never be logged.
type Site struct {
	Slug       string       `yaml:"slug"`
	Repo       string       `yaml:"repo"`
	Branch     string       `yaml:"branch"`
	PublishDir string       `yaml:"publish_dir"`
	HugoEnv    string       `yaml:"hugo_env"`
	BaseURL    string       `yaml:"base_url"`
	Auth       *AuthSpec    `yaml:"auth"`
	Build      BuildSpec    `yaml:"build"`
	Webhook    *WebhookSpec `yaml:"webhook"`

	// Resolved secrets (not part of the YAML schema).
	Token         string `yaml:"-"`
	WebhookSecret string `yaml:"-"`
}

// AuthSpec selects the env var holding an HTTPS access token for the repo.
type AuthSpec struct {
	TokenEnv string `yaml:"token_env"`
}

// BuildSpec carries per-site build overrides.
type BuildSpec struct {
	HugoVersion string   `yaml:"hugo_version"`
	Timeout     Duration `yaml:"timeout"`
}

// WebhookSpec configures the GitHub webhook for a site.
type WebhookSpec struct {
	Provider  string `yaml:"provider"`
	SecretEnv string `yaml:"secret_env"`
}

// WebhookEnabled reports whether the site can receive webhook triggers.
func (s *Site) WebhookEnabled() bool { return s.WebhookSecret != "" }

type sitesFile struct {
	Sites []*Site `yaml:"sites"`
}

// LoadSites parses and validates the sites.yaml at path, resolving per-site
// secrets via getenv. A token_env or secret_env that is set in YAML but
// resolves empty is a hard error — never a silent anonymous fallback.
func LoadSites(path string, getenv Getenv) ([]*Site, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sites config: %w", err)
	}
	return ParseSites(raw, getenv)
}

// ParseSites validates raw sites.yaml content. Split from LoadSites for tests.
func ParseSites(raw []byte, getenv Getenv) ([]*Site, error) {
	var f sitesFile
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse sites config: %w", err)
	}
	if len(f.Sites) == 0 {
		return nil, fmt.Errorf("sites config defines no sites")
	}

	slugs := map[string]bool{}
	publishDirs := map[string]string{} // publish_dir -> slug
	for i, s := range f.Sites {
		if err := validateSite(s, getenv); err != nil {
			return nil, fmt.Errorf("site %d (%q): %w", i, s.Slug, err)
		}
		if slugs[s.Slug] {
			return nil, fmt.Errorf("duplicate slug %q", s.Slug)
		}
		slugs[s.Slug] = true
		if other, dup := publishDirs[s.PublishDir]; dup {
			return nil, fmt.Errorf("sites %q and %q share publish_dir %q; live output would clobber",
				other, s.Slug, s.PublishDir)
		}
		publishDirs[s.PublishDir] = s.Slug
	}
	return f.Sites, nil
}

func validateSite(s *Site, getenv Getenv) error {
	if s.Slug == "" {
		return fmt.Errorf("slug is required")
	}
	if !nameRe.MatchString(s.Slug) {
		return fmt.Errorf("slug %q must match %s", s.Slug, nameRe)
	}

	if s.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	u, err := url.Parse(s.Repo)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("repo %q must be an https:// URL (SSH/git@ is not supported)", s.Repo)
	}
	if u.User != nil {
		return fmt.Errorf("repo URL must not embed credentials; use auth.token_env")
	}

	if s.Branch == "" {
		s.Branch = "main"
	}
	if s.PublishDir == "" {
		s.PublishDir = s.Slug
	}
	if !nameRe.MatchString(s.PublishDir) {
		return fmt.Errorf("publish_dir %q must match %s", s.PublishDir, nameRe)
	}
	if s.HugoEnv == "" {
		s.HugoEnv = "production"
	}
	if s.BaseURL != "" {
		bu, err := url.Parse(s.BaseURL)
		if err != nil || (bu.Scheme != "http" && bu.Scheme != "https") || bu.Host == "" {
			return fmt.Errorf("base_url %q must be an absolute http(s) URL", s.BaseURL)
		}
	}
	if s.Build.HugoVersion != "" && !hugoVersionRe.MatchString(s.Build.HugoVersion) {
		return fmt.Errorf("build.hugo_version %q must be X.Y.Z", s.Build.HugoVersion)
	}

	if s.Auth != nil {
		if s.Auth.TokenEnv == "" {
			return fmt.Errorf("auth block present but auth.token_env is empty; remove auth for public repos")
		}
		s.Token = getenv(s.Auth.TokenEnv)
		if s.Token == "" {
			return fmt.Errorf("auth.token_env %q resolves to an empty value; refusing silent anonymous fallback", s.Auth.TokenEnv)
		}
	}

	if s.Webhook != nil {
		if s.Webhook.Provider != "" && s.Webhook.Provider != "github" {
			return fmt.Errorf("webhook.provider %q unsupported (only \"github\")", s.Webhook.Provider)
		}
		if s.Webhook.SecretEnv == "" {
			return fmt.Errorf("webhook block present but webhook.secret_env is empty")
		}
		s.WebhookSecret = getenv(s.Webhook.SecretEnv)
		if s.WebhookSecret == "" {
			return fmt.Errorf("webhook.secret_env %q resolves to an empty value", s.Webhook.SecretEnv)
		}
	}

	return nil
}
