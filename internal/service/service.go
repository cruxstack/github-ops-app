// Package service runs the application for both entry points, the HTTP
// server and the Lambda function. It loads the configuration through a
// github-app-setup-go runtime, so the configuration can be loaded lazily,
// retried, and reloaded after the installer saves new credentials, and it
// serves the health, installer, and application routes.
package service

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cruxstack/github-app-setup-go/configstore"
	"github.com/cruxstack/github-app-setup-go/ghappsetup"
	"github.com/cruxstack/github-app-setup-go/installer"
	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/setup"
)

// JobTimeout bounds a webhook, a scheduled action, or a background job.
// Requests run detached from the client, so a scheduled sync is not cut
// short when its caller disconnects.
const JobTimeout = app.JobTimeout

// Mode is how the service runs.
type Mode int

const (
	// ModeServer loads the configuration at startup and on reload.
	ModeServer Mode = iota
	// ModeLambda loads the configuration on first use, retrying in later
	// invocations until it succeeds.
	ModeLambda
)

// Options configures a Service.
type Options struct {
	Mode   Mode
	Logger *slog.Logger
	// Env is where configuration is read from. Defaults to the process
	// environment.
	Env config.Env
	// Store replaces the installer's credential store, for tests.
	Store configstore.Store
	// AppOptions are passed to app.New.
	AppOptions []app.Option
	// RetryInterval is the wait between load attempts in ModeLambda.
	// Defaults to one second.
	RetryInterval time.Duration
}

// Service serves the application and keeps its configuration loaded.
type Service struct {
	boot    *config.Bootstrap
	opts    Options
	appOpts []app.Option
	logger  *slog.Logger
	runtime *ghappsetup.Runtime
	current atomic.Pointer[app.App]
	handler http.Handler
	jobs    *jobs // nil in ModeLambda

	// envFile is the installer's env file, read on every load, and
	// explicit are the variables set in the process environment at startup,
	// which take precedence over it
	envFile  string
	explicit map[string]bool
}

// New creates a Service. It does not load the application configuration;
// see Start and EnsureLoaded.
func New(boot *config.Bootstrap, opts Options) (*Service, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Env.Lookup == nil {
		opts.Env = config.ProcessEnv()
	}
	s := &Service{boot: boot, opts: opts, logger: opts.Logger}
	s.appOpts = slices.Clone(opts.AppOptions)
	if opts.Mode == ModeServer {
		// answer webhooks at once and sync afterwards
		s.jobs = &jobs{logger: opts.Logger}
		s.appOpts = append(s.appOpts, app.WithBackground(s.jobs))

		if boot.Installer.Enabled && boot.Installer.StorageMode == configstore.StorageModeEnvFile {
			s.envFile = boot.Installer.StorageDir
			s.explicit = make(map[string]bool)
			for _, kv := range os.Environ() {
				key, _, _ := strings.Cut(kv, "=")
				s.explicit[key] = true
			}
		}
	}

	store := opts.Store
	if store == nil {
		store = setup.NopStore{}
		if boot.Installer.Enabled {
			var err error
			if store, err = configstore.NewFromEnv(); err != nil {
				return nil, fmt.Errorf("failed to create installer credential store: %w", err)
			}
		}
	}
	store = setup.Store{Store: store}

	runtimeCfg := ghappsetup.Config{Store: store, LoadFunc: s.load}
	if opts.Mode == ModeLambda {
		runtimeCfg.MaxRetries = 3
		runtimeCfg.RetryInterval = cmp.Or(opts.RetryInterval, time.Second)
	}
	runtime, err := ghappsetup.NewRuntime(runtimeCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create runtime: %w", err)
	}
	s.runtime = runtime

	var installerHandler http.Handler
	if boot.Installer.Enabled {
		if installerHandler, err = s.newInstaller(); err != nil {
			return nil, err
		}
	}
	s.handler = s.newRouter(installerHandler)
	return s, nil
}

// load reads the configuration and replaces the running application.
func (s *Service) load(ctx context.Context) error {
	env := s.opts.Env
	if s.envFile != "" {
		// credentials the installer saved, read again on every load so a
		// reload picks up changes to the file
		values, err := godotenv.Read(s.envFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to read installer env file '%s': %w", s.envFile, err)
		}
		base := env.Lookup
		env.Lookup = func(key string) (string, bool) {
			if !s.explicit[key] {
				if v, ok := values[key]; ok {
					return v, true
				}
			}
			return base(key)
		}
	}

	cfg, err := config.Load(ctx, env)
	if err != nil {
		return err
	}
	a, err := app.New(cfg, s.logger, s.appOpts...)
	if err != nil {
		return err
	}
	s.current.Store(a)
	s.logger.InfoContext(ctx, "configuration loaded",
		"github_org", cfg.GitHub.Org,
		"okta_sync", cfg.Okta.Enabled,
		"pr_compliance", cfg.PRCompliance.Enabled,
		"security_alerts", cfg.SecurityAlerts.Enabled,
		"slack", cfg.Slack.Enabled,
		"admin_api", cfg.AdminToken != "")
	return nil
}

// Start loads the configuration and then reloads it on SIGHUP or when the
// installer saves credentials, until ctx is done. It is for ModeServer. If
// the first load fails, Start returns the error, unless the installer is
// enabled: then the service keeps running so the app can be set up.
func (s *Service) Start(ctx context.Context) error {
	if err := s.runtime.Reload(ctx); err != nil {
		if !s.boot.Installer.Enabled {
			return err
		}
		s.logger.WarnContext(ctx, "configuration not loaded; waiting for the github app to be set up",
			"setup_url", s.boot.Server.BasePath+"/setup",
			"error", err.Error())
	}
	s.runtime.ListenForReloads(ctx)
	return nil
}

// EnsureLoaded loads the configuration if it is not loaded yet. It is for
// ModeLambda: a failed load is retried on the next call.
func (s *Service) EnsureLoaded(ctx context.Context) (*app.App, error) {
	if a := s.current.Load(); a != nil {
		return a, nil
	}
	if s.opts.Mode != ModeLambda {
		return nil, errors.New("configuration is not loaded")
	}
	if err := s.runtime.EnsureLoaded(ctx); err != nil {
		return nil, err
	}
	return s.current.Load(), nil
}

// Wait waits until background jobs, such as webhook-triggered syncs, finish
// or ctx is done.
func (s *Service) Wait(ctx context.Context) error {
	if s.jobs == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		s.jobs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jobs runs background jobs, each with its own JobTimeout, and tracks them
// so shutdown can wait for them.
type jobs struct {
	wg     sync.WaitGroup
	logger *slog.Logger
}

func (j *jobs) Go(f func(ctx context.Context)) {
	j.wg.Go(func() {
		defer func() {
			if v := recover(); v != nil {
				j.logger.Error("panic in background job", "panic", v)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), JobTimeout)
		defer cancel()
		f(ctx)
	})
}

// Ready returns true when the configuration is loaded.
func (s *Service) Ready() bool {
	return s.current.Load() != nil
}

// Handler returns the service's HTTP handler.
func (s *Service) Handler() http.Handler {
	return s.handler
}

// RunScheduled runs a scheduled action with a detached, bounded context.
func (s *Service) RunScheduled(ctx context.Context, action string) error {
	a, err := s.EnsureLoaded(ctx)
	if err != nil {
		return fmt.Errorf("service not ready: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), JobTimeout)
	defer cancel()
	return a.RunScheduled(ctx, action)
}

func (s *Service) newInstaller() (http.Handler, error) {
	cfg := installer.Config{
		Store:          s.runtime.Store(),
		Manifest:       setup.Manifest(),
		AppDisplayName: "GitHub Ops App",
		GitHubURL:      s.boot.Installer.GitHubURL,
		GitHubOrg:      s.boot.Installer.GitHubOrg,
		OnReloadNeeded: s.runtime.ReloadCallback(),
	}
	if public := s.boot.Server.PublicURL; public != "" {
		cfg.RedirectURL = public
		cfg.WebhookURL = public + "/webhooks"
	}
	h, err := installer.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create installer: %w", err)
	}
	return h, nil
}

// newRouter serves, under the base path:
//
//	GET /healthz                       liveness, always 200
//	GET /readyz                        200 once the configuration is loaded
//	GET /setup, /callback, ...         the installer, when enabled
//	everything else                    the application, once loaded
func (s *Service) newRouter(installerHandler http.Handler) http.Handler {
	base := s.boot.Server.BasePath
	r := chi.NewRouter()
	r.Use(s.recoverer, s.detach)

	r.Get(base+"/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	r.Get(base+"/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Ready() {
			writeStatus(w, http.StatusServiceUnavailable, "not ready")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	})

	if installerHandler != nil {
		h := http.StripPrefix(base, s.guardCallback(installerHandler))
		r.Get(base+"/setup", h.ServeHTTP)
		r.Get(base+"/setup/", h.ServeHTTP)
		r.Post(base+"/setup/disable", h.ServeHTTP)
		r.Get(base+"/callback", h.ServeHTTP)
	}

	r.NotFound(s.serveApp)
	r.MethodNotAllowed(s.serveApp)
	return r
}

// serveApp passes a request to the loaded application.
func (s *Service) serveApp(w http.ResponseWriter, r *http.Request) {
	a, err := s.EnsureLoaded(r.Context())
	if err != nil {
		s.logger.WarnContext(r.Context(), "request received before the configuration loaded",
			"path", r.URL.Path, "error", err.Error())
		w.Header().Set("Retry-After", "30")
		writeStatus(w, http.StatusServiceUnavailable, "service not configured")
		return
	}
	a.Handler().ServeHTTP(w, r)
}

// guardCallback refuses to exchange installer codes once an app is
// registered or the configuration is loaded, so whoever can reach the
// service cannot replace the app's credentials with their own after setup,
// even before the installer is disabled.
func (s *Service) guardCallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSuffix(r.URL.Path, "/") != "/callback" {
			next.ServeHTTP(w, r)
			return
		}
		if s.Ready() {
			writeStatus(w, http.StatusConflict, "the github app is already configured")
			return
		}
		status, err := s.runtime.Store().Status(r.Context())
		if err != nil {
			s.logger.ErrorContext(r.Context(), "failed to read installer status", "error", err.Error())
			writeStatus(w, http.StatusInternalServerError, "failed to read installer status")
			return
		}
		if status != nil && status.Registered {
			writeStatus(w, http.StatusConflict, "the github app is already configured")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// detach gives requests that run jobs a context that is not canceled when
// the client disconnects.
func (s *Service) detach(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), JobTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recoverer turns a panic into a 500 response and an error log.
func (s *Service) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				s.logger.ErrorContext(r.Context(), "panic while serving request",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", v)
				writeStatus(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeStatus(w http.ResponseWriter, status int, message string) {
	key := "status"
	if status >= 400 {
		key = "error"
	}
	body, _ := json.Marshal(map[string]string{key: message})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
