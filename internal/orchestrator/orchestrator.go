// Package orchestrator runs the per-site pipeline:
// git sync → hugo build → validate → atomic publish → state + retention.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uppertoe/hugo-orchestrate/internal/build"
	"github.com/uppertoe/hugo-orchestrate/internal/config"
	"github.com/uppertoe/hugo-orchestrate/internal/gitsource"
	"github.com/uppertoe/hugo-orchestrate/internal/hugo"
	"github.com/uppertoe/hugo-orchestrate/internal/publish"
	"github.com/uppertoe/hugo-orchestrate/internal/state"
)

// Orchestrator executes builds; it is the queue.BuildFunc target.
type Orchestrator struct {
	env     *config.Env
	layout  config.Layout
	sites   map[string]*config.Site
	catalog *hugo.Catalog
	git     *gitsource.Client
	runner  *build.Runner
	pub     *publish.Publisher
	states  *state.Store
	log     *slog.Logger

	buildSeq  atomic.Uint64
	firstOnce map[string]*sync.Once
	firstWG   sync.WaitGroup
}

// New wires an Orchestrator and fail-fast validates that every site's
// requested Hugo version is installed. Sites without a usable webhook are
// surfaced as warnings: they will only ever get the startup build.
func New(env *config.Env, sites []*config.Site, catalog *hugo.Catalog, states *state.Store, log *slog.Logger) (*Orchestrator, error) {
	layout := config.NewLayout(env)
	o := &Orchestrator{
		env:       env,
		layout:    layout,
		sites:     make(map[string]*config.Site, len(sites)),
		catalog:   catalog,
		git:       gitsource.New(env.GitTimeout, layout.HomeDir()),
		runner:    &build.Runner{HomeDir: layout.HomeDir()},
		pub:       publish.New(),
		states:    states,
		log:       log,
		firstOnce: make(map[string]*sync.Once, len(sites)),
	}
	for _, s := range sites {
		if _, _, err := catalog.Resolve(s.Build.HugoVersion); err != nil {
			return nil, fmt.Errorf("site %q: %w", s.Slug, err)
		}
		if !s.WebhookEnabled() {
			log.Warn("site has no webhook configured; it will only build at startup", "slug", s.Slug)
		}
		o.sites[s.Slug] = s
		o.firstOnce[s.Slug] = &sync.Once{}
		o.firstWG.Add(1)
	}
	return o, nil
}

// WaitFirstPass blocks until every site has completed (or failed) one build,
// or ctx is done. Used to flip /readyz after the initial sync.
func (o *Orchestrator) WaitFirstPass(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		o.firstWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// BuildSite runs one build attempt for slug. Errors are logged and recorded
// in the site's state file; the queue does not see them.
func (o *Orchestrator) BuildSite(ctx context.Context, slug, reason string) {
	defer o.firstOnce[slug].Do(o.firstWG.Done)

	site := o.sites[slug]
	log := o.log.With("slug", slug, "reason", reason)
	start := time.Now()

	commit, err := o.runPipeline(ctx, site, log)
	duration := time.Since(start)
	st := state.SiteState{
		Slug:       slug,
		Reason:     reason,
		Commit:     commit,
		DurationMS: duration.Milliseconds(),
		Status:     "success",
		FinishedAt: time.Now().UTC(),
	}
	if err != nil {
		st.Status = "failed"
		st.Error = err.Error()
		log.Error("build failed", "error", err, "duration_ms", st.DurationMS, "commit", commit)
	} else {
		log.Info("build succeeded", "commit", commit, "duration_ms", st.DurationMS, "status", st.Status)
	}
	if werr := o.states.Write(st); werr != nil {
		log.Error("failed to write state", "error", werr)
	}
}

func (o *Orchestrator) runPipeline(ctx context.Context, site *config.Site, log *slog.Logger) (commit string, err error) {
	version, binary, err := o.catalog.Resolve(site.Build.HugoVersion)
	if err != nil {
		return "", err
	}

	err = o.retry(ctx, log, "git sync", func() error {
		var gerr error
		commit, gerr = o.git.Sync(ctx, site, o.layout.RepoDir(site.Slug))
		return gerr
	})
	if err != nil {
		return commit, err
	}

	buildID := fmt.Sprintf("%s-%04d", time.Now().UTC().Format("20060102-150405"), o.buildSeq.Add(1)%10000)
	buildDir := o.layout.BuildDir(site.Slug, buildID)
	timeout := o.env.BuildTimeout
	if site.Build.Timeout > 0 {
		timeout = time.Duration(site.Build.Timeout)
	}
	log.Info("building", "commit", commit, "hugo_version", version, "build_id", buildID)
	if err := o.runner.Run(ctx, build.Input{
		Binary:      binary,
		SourceDir:   o.layout.RepoDir(site.Slug),
		DestDir:     buildDir,
		CacheDir:    o.layout.CacheDir(site.Slug),
		Environment: site.HugoEnv,
		BaseURL:     site.BaseURL,
		Timeout:     timeout,
	}); err != nil {
		return commit, err
	}
	if err := build.ValidateOutput(buildDir); err != nil {
		return commit, err
	}

	err = o.retry(ctx, log, "publish", func() error {
		return o.pub.Publish(buildDir, o.layout.LiveDir(site.PublishDir), buildID)
	})
	if err != nil {
		return commit, err
	}

	if err := publish.PruneBuilds(o.layout.BuildsDir(site.Slug), o.env.BuildRetentionCount); err != nil {
		log.Warn("build retention pruning failed", "error", err)
	}
	return commit, nil
}

// retry runs op up to 1+OperationRetries times with exponential backoff.
func (o *Orchestrator) retry(ctx context.Context, log *slog.Logger, name string, op func() error) error {
	var err error
	backoff := o.env.RetryBackoff
	for attempt := 0; attempt <= o.env.OperationRetries; attempt++ {
		if attempt > 0 {
			log.Warn("retrying", "op", name, "attempt", attempt, "backoff", backoff.String(), "error", err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff *= 2
		}
		if err = op(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
	}
	return err
}
