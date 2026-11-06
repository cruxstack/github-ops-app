package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cruxstack/github-app-setup-go/configstore"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/fakes"
)

var discard = slog.New(slog.DiscardHandler)

func keyPEM(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

// mapEnv is a config.Env backed by a map that tests can change.
type mapEnv struct {
	mu sync.Mutex
	m  map[string]string
}

func (e *mapEnv) set(k, v string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.m[k] = v
}

func (e *mapEnv) env() config.Env {
	return config.Env{Lookup: func(k string) (string, bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		v, ok := e.m[k]
		return v, ok
	}}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, path, nil))
	return rec
}

func newService(t *testing.T, env config.Env, mode Mode, store configstore.Store) *Service {
	t.Helper()
	boot, err := config.LoadBootstrap(env)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(boot, Options{
		Mode:          mode,
		Logger:        discard,
		Env:           env,
		Store:         store,
		AppOptions:    []app.Option{app.WithGitHubClient(&fakes.GitHub{})},
		RetryInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestServerMode_FailsFastWithoutInstaller(t *testing.T) {
	e := &mapEnv{m: map[string]string{"APP_GITHUB_ORG": "acme"}}
	svc := newService(t, e.env(), ModeServer, nil)
	if err := svc.Start(t.Context()); err == nil {
		t.Fatal("Start succeeded without credentials")
	}
}

func TestServerMode_Ready(t *testing.T) {
	e := &mapEnv{m: map[string]string{
		"APP_GITHUB_ORG":             "acme",
		"APP_GITHUB_APP_ID":          "1",
		"APP_GITHUB_APP_PRIVATE_KEY": keyPEM(t),
		"APP_GITHUB_WEBHOOK_SECRET":  "s",
		"APP_BASE_PATH":              "/prod",
	}}
	svc := newService(t, e.env(), ModeServer, nil)
	h := svc.Handler()

	if rec := get(t, h, http.MethodGet, "/prod/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before start: %d", rec.Code)
	}
	if rec := get(t, h, http.MethodPost, "/prod/webhooks"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("webhook before start: %d", rec.Code)
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, http.MethodGet, "/prod/readyz"); rec.Code != http.StatusOK {
		t.Errorf("readyz after start: %d", rec.Code)
	}
	if rec := get(t, h, http.MethodGet, "/prod/healthz"); rec.Code != http.StatusOK {
		t.Errorf("healthz: %d", rec.Code)
	}
	// reaches the application, which requires a signature
	if rec := get(t, h, http.MethodPost, "/prod/webhooks"); rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook: %d, want 401 from the app", rec.Code)
	}
	if rec := get(t, h, http.MethodGet, "/prod/setup"); rec.Code != http.StatusNotFound {
		t.Errorf("setup without installer: %d, want 404", rec.Code)
	}
}

// installerEnv sets up the process environment for the installer with an
// env file store. The installer's store writes the process environment, so
// every variable it writes is restored after the test.
func installerEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{
		"APP_GITHUB_APP_ID", "APP_GITHUB_APP_PRIVATE_KEY", "APP_GITHUB_WEBHOOK_SECRET",
		configstore.EnvGitHubAppID, configstore.EnvGitHubAppPrivateKey, configstore.EnvGitHubWebhookSecret,
		configstore.EnvGitHubClientID, configstore.EnvGitHubClientSecret,
		configstore.EnvGitHubAppSlug, configstore.EnvGitHubAppHTMLURL,
	} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	envFile := filepath.Join(t.TempDir(), ".env")
	t.Setenv("APP_GITHUB_ORG", "acme")
	t.Setenv("APP_BASE_PATH", "/prod")
	t.Setenv("APP_PUBLIC_URL", "https://ops.example.com/prod")
	t.Setenv(configstore.EnvGitHubAppInstallerEnabled, "true")
	t.Setenv(configstore.EnvStorageMode, configstore.StorageModeEnvFile)
	t.Setenv(configstore.EnvStorageDir, envFile)
	return envFile
}

func waitReady(t *testing.T, svc *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !svc.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("service did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInstaller_SetupSaveReloadAndRestart(t *testing.T) {
	envFile := installerEnv(t)
	svc := newService(t, config.ProcessEnv(), ModeServer, nil)
	h := svc.Handler()

	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("Start with the installer enabled must not fail: %v", err)
	}
	if svc.Ready() {
		t.Fatal("ready without credentials")
	}

	rec := get(t, h, http.MethodGet, "/prod/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("setup page: %d", rec.Code)
	}
	page := rec.Body.String()
	for _, want := range []string{
		`https://ops.example.com/prod/callback`,
		`https://ops.example.com/prod/webhooks`,
		`"public":false`,
		`"vulnerability_alerts":"read"`,
		`"administration":"read"`,
		`/organizations/acme/settings/apps/new`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("setup page lacks %s", want)
		}
	}

	// what the installer's callback does after github creates the app
	key := keyPEM(t)
	creds := &configstore.AppCredentials{AppID: 42, AppSlug: "ops", PrivateKey: key, WebhookSecret: "whsec", ClientID: "cid", ClientSecret: "csec"}
	if err := svc.runtime.Store().Save(t.Context(), creds); err != nil {
		t.Fatal(err)
	}
	svc.runtime.ReloadCallback()()
	waitReady(t, svc)

	if got := svc.current.Load().Config().GitHub.AppID; got != 42 {
		t.Errorf("app id after reload = %d", got)
	}
	if rec := get(t, h, http.MethodGet, "/prod/callback?code=abcdefghij0123"); rec.Code != http.StatusConflict {
		t.Errorf("callback after setup: %d, want 409", rec.Code)
	}

	file, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "APP_GITHUB_APP_ID") || !strings.Contains(string(file), "APP_GITHUB_WEBHOOK_SECRET") {
		t.Errorf("env file lacks the app's credential names:\n%s", file)
	}

	// a reload picks up a secret rotated in the env file
	rotated := regexp.MustCompile(`(?m)^APP_GITHUB_WEBHOOK_SECRET=.*$`).ReplaceAll(file, []byte(`APP_GITHUB_WEBHOOK_SECRET="rotated"`))
	if err := os.WriteFile(envFile, rotated, 0o600); err != nil {
		t.Fatal(err)
	}
	svc.runtime.ReloadCallback()()
	waitFor(t, func() bool { return svc.current.Load().Config().GitHub.WebhookSecret == "rotated" })

	// a restarted service reads the credentials from the env file
	for _, k := range []string{"APP_GITHUB_APP_ID", "APP_GITHUB_APP_PRIVATE_KEY", "APP_GITHUB_WEBHOOK_SECRET"} {
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	restarted := newService(t, config.ProcessEnv(), ModeServer, nil)
	if err := restarted.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !restarted.Ready() || restarted.current.Load().Config().GitHub.WebhookSecret != "rotated" {
		t.Error("restarted service did not load the saved credentials")
	}

	// variables set in the environment take precedence over the file
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "from-env")
	explicit := newService(t, config.ProcessEnv(), ModeServer, nil)
	if err := explicit.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := explicit.current.Load().Config().GitHub.WebhookSecret; got != "from-env" {
		t.Errorf("webhook secret = %q, want the environment's value", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWait_BackgroundJobs(t *testing.T) {
	svc := &Service{jobs: &jobs{logger: discard}}
	release := make(chan struct{})
	svc.jobs.Go(func(context.Context) { <-release })
	svc.jobs.Go(func(context.Context) { panic("boom") })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := svc.Wait(ctx); err == nil {
		t.Fatal("Wait returned before the job finished")
	}
	close(release)
	if err := svc.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (&Service{}).Wait(context.Background()); err != nil {
		t.Errorf("Wait without jobs: %v", err)
	}
}

// registeredStore reports an app as already registered.
type registeredStore struct{ configstore.Store }

func (registeredStore) Status(context.Context) (*configstore.InstallerStatus, error) {
	return &configstore.InstallerStatus{Registered: true, AppID: 1}, nil
}

func TestInstaller_CallbackRefusedOnceRegistered(t *testing.T) {
	installerEnv(t)
	svc := newService(t, config.ProcessEnv(), ModeServer, registeredStore{})
	if rec := get(t, svc.Handler(), http.MethodGet, "/prod/callback?code=abcdefghij0123"); rec.Code != http.StatusConflict {
		t.Errorf("callback with a registered app: %d, want 409", rec.Code)
	}
}

func TestLambdaMode_RetriesLoadAfterFailure(t *testing.T) {
	e := &mapEnv{m: map[string]string{"APP_GITHUB_ORG": "acme"}}
	svc := newService(t, e.env(), ModeLambda, nil)
	h := svc.Handler()

	if rec := get(t, h, http.MethodPost, "/webhooks"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("webhook without config: %d, want 503", rec.Code)
	}
	if err := svc.RunScheduled(t.Context(), app.ActionOktaSync); err == nil {
		t.Fatal("scheduled action ran without config")
	}

	// the credentials appear, as when the installer saves them to ssm
	e.set("APP_GITHUB_APP_ID", "1")
	e.set("APP_GITHUB_APP_PRIVATE_KEY", keyPEM(t))
	e.set("APP_GITHUB_WEBHOOK_SECRET", "s")

	if rec := get(t, h, http.MethodPost, "/webhooks"); rec.Code != http.StatusUnauthorized {
		t.Errorf("webhook after config: %d, want 401 from the app", rec.Code)
	}
	if !svc.Ready() {
		t.Error("not ready after a successful load")
	}
}

func TestRecoverer(t *testing.T) {
	svc := &Service{logger: discard}
	h := svc.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	if rec := get(t, h, http.MethodGet, "/"); rec.Code != http.StatusInternalServerError {
		t.Errorf("panic: %d, want 500", rec.Code)
	}
}

func TestDetach_SurvivesClientCancel(t *testing.T) {
	svc := &Service{logger: discard}
	var jobErr error
	h := svc.detach(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		jobErr = r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/webhooks", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if jobErr != nil {
		t.Errorf("job context canceled with the request: %v", jobErr)
	}
}
