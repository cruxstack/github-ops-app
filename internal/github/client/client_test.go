package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

const (
	testOrg   = "acme"
	testAppID = 1234
	testToken = "ghs_installation_token"
)

var (
	testKeyOnce sync.Once
	testKey     *rsa.PrivateKey
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func pkcs1PEM(t *testing.T) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey(t))})
}

// fakeGitHub is a GitHub API server. It serves the installation token
// endpoint itself and checks that every other request carries that token.
type fakeGitHub struct {
	t   *testing.T
	mux *http.ServeMux
	srv *httptest.Server

	tokenRequests atomic.Int32
	mu            sync.Mutex
	requests      []string
	handlers      map[string]http.HandlerFunc
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, mux: http.NewServeMux(), handlers: map[string]http.HandlerFunc{}}
	f.handle("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		f.requireJWT(r)
		f.tokenRequests.Add(1)
		writeJSON(w, http.StatusCreated, map[string]any{
			"token":      testToken,
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()

	usesJWT := strings.HasPrefix(r.URL.Path, "/app") || strings.HasSuffix(r.URL.Path, "/installation")
	if !usesJWT && r.Header.Get("Authorization") != "Bearer "+testToken {
		f.t.Errorf("%s %s: authorization = %q, want the installation token", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	}
	f.mux.ServeHTTP(w, r)
}

// requireJWT fails the test unless r carries a JWT signed by the app key.
func (f *fakeGitHub) requireJWT(r *http.Request) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		f.t.Errorf("%s: missing bearer jwt", r.URL.Path)
		return
	}
	claims := &jwt.RegisteredClaims{}
	if _, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return &testKey.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"})); err != nil {
		f.t.Errorf("%s: invalid jwt: %v", r.URL.Path, err)
		return
	}
	if claims.Issuer != fmt.Sprint(testAppID) {
		f.t.Errorf("jwt issuer = %q, want %d", claims.Issuer, testAppID)
	}
}

// handle serves pattern with h, replacing any earlier handler for it.
func (f *fakeGitHub) handle(pattern string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.handlers[pattern]; !ok {
		f.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			h := f.handlers[pattern]
			f.mu.Unlock()
			h(w, r)
		})
	}
	f.handlers[pattern] = h
}

func (f *fakeGitHub) json(pattern string, status int, body any) {
	f.handle(pattern, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, status, body) })
}

func (f *fakeGitHub) called(methodPath string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == methodPath {
			n++
		}
	}
	return n
}

func (f *fakeGitHub) client(t *testing.T, installationID int64) *Client {
	t.Helper()
	c, err := New(Options{
		AppID:          testAppID,
		InstallationID: installationID,
		PrivateKey:     pkcs1PEM(t),
		Org:            testOrg,
		BaseURL:        f.srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// nextLink sets a Link header pointing at the next page.
func nextLink(w http.ResponseWriter, r *http.Request, query string) {
	w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, query))
}

func TestNew_Validation(t *testing.T) {
	key := pkcs1PEM(t)
	tests := []struct {
		name string
		opts Options
	}{
		{"missing app id", Options{PrivateKey: key, Org: testOrg}},
		{"missing org", Options{AppID: 1, PrivateKey: key}},
		{"missing key", Options{AppID: 1, Org: testOrg}},
		{"bad key", Options{AppID: 1, Org: testOrg, PrivateKey: []byte("not a key")}},
		{"bad base url", Options{AppID: 1, Org: testOrg, PrivateKey: key, BaseURL: "ftp://example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.opts); err == nil {
				t.Error("New succeeded, want error")
			}
		})
	}
}

func TestParsePrivateKey(t *testing.T) {
	key := rsaKey(t)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPKCS8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	escaped := strings.ReplaceAll(string(pkcs1PEM(t)), "\n", `\n`)

	tests := []struct {
		name    string
		pem     []byte
		wantErr bool
	}{
		{"pkcs1", pkcs1PEM(t), false},
		{"pkcs8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), false},
		{"escaped newlines", []byte(escaped), false},
		{"ec key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecPKCS8}), true},
		{"not pem", []byte("garbage"), true},
		{"empty", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePrivateKey(tt.pem)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePrivateKey() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_DiscoversInstallationAndCachesToken(t *testing.T) {
	f := newFakeGitHub(t)
	f.handle("GET /orgs/acme/installation", func(w http.ResponseWriter, r *http.Request) {
		f.requireJWT(r)
		writeJSON(w, http.StatusOK, map[string]any{"id": 77})
	})
	f.json("GET /orgs/acme/members", http.StatusOK, []map[string]any{{"login": "alice"}})
	c := f.client(t, 0)

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := c.ListOrgMembers(context.Background()); err != nil {
				t.Errorf("ListOrgMembers: %v", err)
			}
		})
	}
	wg.Wait()

	if got := f.called("GET /orgs/acme/installation"); got != 1 {
		t.Errorf("installation lookups = %d, want 1", got)
	}
	if got := f.called("POST /app/installations/77/access_tokens"); got != 1 {
		t.Errorf("token requests = %d, want 1", got)
	}
}

func TestClient_InstallationMissing(t *testing.T) {
	f := newFakeGitHub(t)
	f.json("GET /orgs/acme/installation", http.StatusNotFound, map[string]any{"message": "Not Found"})
	c := f.client(t, 0)

	_, err := c.ListOrgMembers(context.Background())
	if !errors.Is(err, domain.ErrInstallationMissing) {
		t.Fatalf("error = %v, want ErrInstallationMissing", err)
	}
	if !errors.Is(err, domain.ErrConfig) {
		t.Errorf("error kind is not ErrConfig")
	}
}

func TestAuthTransport_OnlyAuthenticatesAPIHost(t *testing.T) {
	var gotAuth string
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer other.Close()

	tr := &authTransport{
		base:  http.DefaultTransport,
		host:  "api.github.com",
		token: func(context.Context) (string, error) { return "secret", nil },
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, other.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("token sent to another host: %q", gotAuth)
	}
}

func TestGetAppSlug_Cached(t *testing.T) {
	f := newFakeGitHub(t)
	f.handle("GET /app", func(w http.ResponseWriter, r *http.Request) {
		f.requireJWT(r)
		writeJSON(w, http.StatusOK, map[string]any{"slug": "ops-bot"})
	})
	c := f.client(t, 1)

	for range 3 {
		slug, err := c.GetAppSlug(context.Background())
		if err != nil || slug != "ops-bot" {
			t.Fatalf("GetAppSlug = %q, %v", slug, err)
		}
	}
	if got := f.called("GET /app"); got != 1 {
		t.Errorf("app fetches = %d, want 1", got)
	}
}

// prFixture serves a merged PR and lets each test add its rules, reviews,
// statuses, and check runs.
func prFixture(t *testing.T) *fakeGitHub {
	f := newFakeGitHub(t)
	f.json("GET /repos/acme/web/pulls/7", http.StatusOK, map[string]any{
		"number":    7,
		"merged":    true,
		"base":      map[string]any{"ref": "main"},
		"head":      map[string]any{"sha": "abc123"},
		"merged_by": map[string]any{"login": "mallory"},
	})
	f.json("GET /repos/acme/web/rules/branches/main", http.StatusOK, []any{})
	f.json("GET /repos/acme/web/collaborators/mallory/permission", http.StatusOK, map[string]any{
		"permission": "write",
		"role_name":  "maintain",
	})
	return f
}

func notProtected(f *fakeGitHub) {
	f.json("GET /repos/acme/web/branches/main/protection", http.StatusNotFound, map[string]any{"message": "Branch not protected"})
}

func TestCheckPRCompliance_NoProtection(t *testing.T) {
	f := prFixture(t)
	notProtected(f)
	c := f.client(t, 1)

	result, err := c.CheckPRCompliance(context.Background(), "acme", "web", 7)
	if err != nil {
		t.Fatal(err)
	}
	if result.HasViolations() {
		t.Errorf("violations = %v, want none", result.Violations)
	}
	if f.called("GET /repos/acme/web/pulls/7/reviews") != 0 {
		t.Error("reviews fetched although none are required")
	}
}

func TestCheckPRCompliance_ProtectionErrorIsReturned(t *testing.T) {
	f := prFixture(t)
	f.json("GET /repos/acme/web/branches/main/protection", http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"})
	c := f.client(t, 1)

	if _, err := c.CheckPRCompliance(context.Background(), "acme", "web", 7); err == nil {
		t.Fatal("CheckPRCompliance succeeded, want the permission error")
	}
}

func TestCheckPRCompliance_Reviews(t *testing.T) {
	review := func(login, state string) map[string]any {
		return map[string]any{"user": map[string]any{"login": login}, "state": state}
	}
	tests := []struct {
		name    string
		pages   [][]map[string]any
		wantErr bool
	}{
		{
			name: "comment after approval keeps the approval",
			pages: [][]map[string]any{
				{review("alice", "APPROVED"), review("bob", "APPROVED")},
				{review("alice", "COMMENTED")},
			},
		},
		{
			name: "changes requested after approval revokes it",
			pages: [][]map[string]any{
				{review("alice", "APPROVED"), review("bob", "APPROVED")},
				{review("Alice", "CHANGES_REQUESTED")},
			},
			wantErr: true,
		},
		{
			name: "dismissed approval does not count",
			pages: [][]map[string]any{
				{review("alice", "APPROVED"), review("bob", "DISMISSED")},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := prFixture(t)
			f.json("GET /repos/acme/web/branches/main/protection", http.StatusOK, map[string]any{
				"required_pull_request_reviews": map[string]any{"required_approving_review_count": 2},
			})
			f.handle("GET /repos/acme/web/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
				page := 0
				if r.URL.Query().Get("page") == "2" {
					page = 1
				}
				if page+1 < len(tt.pages) {
					nextLink(w, r, "page=2&per_page=100")
				}
				writeJSON(w, http.StatusOK, tt.pages[page])
			})
			c := f.client(t, 1)

			result, err := c.CheckPRCompliance(context.Background(), "acme", "web", 7)
			if err != nil {
				t.Fatal(err)
			}
			if result.HasViolations() != tt.wantErr {
				t.Errorf("violations = %v, want violation: %v", result.Violations, tt.wantErr)
			}
		})
	}
}

func TestCheckPRCompliance_StatusChecks(t *testing.T) {
	f := prFixture(t)
	f.json("GET /repos/acme/web/branches/main/protection", http.StatusOK, map[string]any{
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []string{"legacy/ci"},
			"checks":   []map[string]any{{"context": "lint"}},
		},
	})
	f.json("GET /repos/acme/web/rules/branches/main", http.StatusOK, []map[string]any{{
		"type": "required_status_checks",
		"parameters": map[string]any{
			"required_status_checks": []map[string]any{{"context": "docs"}, {"context": "e2e"}, {"context": "build"}},
		},
	}})
	f.json("GET /repos/acme/web/commits/abc123/status", http.StatusOK, map[string]any{
		"statuses": []map[string]any{{"context": "legacy/ci", "state": "success"}},
	})
	f.json("GET /repos/acme/web/commits/abc123/check-runs", http.StatusOK, map[string]any{
		"total_count": 4,
		"check_runs": []map[string]any{
			{"name": "lint", "conclusion": "success"},
			{"name": "docs", "conclusion": "skipped"},
			{"name": "e2e", "conclusion": "neutral"},
			{"name": "build", "conclusion": "failure"},
		},
	})
	c := f.client(t, 1)

	result, err := c.CheckPRCompliance(context.Background(), "acme", "web", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Violations) != 1 || !strings.Contains(result.Violations[0].Description, "'build'") {
		t.Fatalf("violations = %v, want only the failed build check", result.Violations)
	}
	if !result.WasBypassed() || !result.UserHasBypass || result.UserBypassReason != "repository maintainer" {
		t.Errorf("bypass = %v %v %q, want a maintainer bypass", result.WasBypassed(), result.UserHasBypass, result.UserBypassReason)
	}
}

func TestListSecurityAlerts_PaginatesAndFilters(t *testing.T) {
	f := newFakeGitHub(t)
	old := time.Now().AddDate(0, 0, -90).UTC().Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	repo := map[string]any{"full_name": "acme/web"}

	// dependabot pages with an "after" cursor
	f.handle("GET /orgs/acme/dependabot/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			nextLink(w, r, "after=cursor1&per_page=100")
			writeJSON(w, http.StatusOK, []map[string]any{
				{"number": 1, "created_at": old, "repository": repo, "security_advisory": map[string]any{"severity": "critical", "summary": "rce"}},
				{"number": 2, "created_at": old, "repository": repo, "security_advisory": map[string]any{"severity": "low"}},
			})
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 3, "created_at": old, "repository": repo, "security_vulnerability": map[string]any{"severity": "high"}},
			{"number": 4, "created_at": recent, "repository": repo, "security_advisory": map[string]any{"severity": "critical"}},
		})
	})
	// code scanning pages by number
	f.handle("GET /orgs/acme/code-scanning/alerts", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			nextLink(w, r, "page=2&per_page=100")
			writeJSON(w, http.StatusOK, []map[string]any{
				{"number": 5, "created_at": old, "repository": repo, "rule": map[string]any{"severity": "error"}},
			})
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 6, "created_at": old, "repository": repo, "rule": map[string]any{"security_severity_level": "medium"}},
		})
	})
	f.json("GET /orgs/acme/secret-scanning/alerts", http.StatusForbidden, map[string]any{"message": "Advanced Security is not enabled"})
	c := f.client(t, 1)

	report, err := c.ListSecurityAlerts(context.Background(), 30, domain.SeverityHigh)
	if err != nil {
		t.Fatal(err)
	}

	var numbers []int
	for _, a := range report.AlertsByRepo["acme/web"] {
		numbers = append(numbers, a.Number)
	}
	if fmt.Sprint(numbers) != "[1 3 5]" || report.TotalAlerts != 3 {
		t.Errorf("alerts = %v (total %d), want [1 3 5]", numbers, report.TotalAlerts)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "secret scanning") {
		t.Errorf("errors = %v, want the secret scanning failure", report.Errors)
	}
}

func TestListSecurityAlerts_SkipsSecretScanningBelowSeverity(t *testing.T) {
	f := newFakeGitHub(t)
	f.json("GET /orgs/acme/dependabot/alerts", http.StatusOK, []any{})
	f.json("GET /orgs/acme/code-scanning/alerts", http.StatusOK, []any{})
	c := f.client(t, 1)

	if _, err := c.ListSecurityAlerts(context.Background(), 30, domain.SeverityCritical); err != nil {
		t.Fatal(err)
	}
	if f.called("GET /orgs/acme/secret-scanning/alerts") != 0 {
		t.Error("secret scanning alerts fetched although their severity is below critical")
	}
}

func TestResolveTeam(t *testing.T) {
	t.Run("existing team", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /orgs/acme/teams/platform-team", http.StatusOK, map[string]any{"slug": "platform-team"})
		slug, err := f.client(t, 1).ResolveTeam(context.Background(), "Platform Team", "closed", false)
		if err != nil || slug != "platform-team" {
			t.Fatalf("ResolveTeam = %q, %v", slug, err)
		}
	})

	t.Run("finds a team whose slug differs from its name", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /orgs/acme/teams/{slug}", http.StatusNotFound, map[string]any{"message": "Not Found"})
		f.json("GET /orgs/acme/teams", http.StatusOK, []map[string]any{{"name": "Ops & Infra", "slug": "ops-infra"}})
		slug, err := f.client(t, 1).ResolveTeam(context.Background(), "ops & infra", "closed", false)
		if err != nil || slug != "ops-infra" {
			t.Fatalf("ResolveTeam = %q, %v", slug, err)
		}
	})

	t.Run("missing team without create", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /orgs/acme/teams/{slug}", http.StatusNotFound, map[string]any{"message": "Not Found"})
		f.json("GET /orgs/acme/teams", http.StatusOK, []any{})
		_, err := f.client(t, 1).ResolveTeam(context.Background(), "eng", "closed", false)
		if !errors.Is(err, domain.ErrTeamNotFound) {
			t.Fatalf("error = %v, want ErrTeamNotFound", err)
		}
		if f.called("POST /orgs/acme/teams") != 0 {
			t.Error("team created although create is false")
		}
	})

	t.Run("missing team with create", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /orgs/acme/teams/{slug}", http.StatusNotFound, map[string]any{"message": "Not Found"})
		f.json("GET /orgs/acme/teams", http.StatusOK, []any{})
		f.handle("POST /orgs/acme/teams", func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"privacy":"secret"`) {
				t.Errorf("create body = %s, want secret privacy", body)
			}
			writeJSON(w, http.StatusCreated, map[string]any{"slug": "eng"})
		})
		slug, err := f.client(t, 1).ResolveTeam(context.Background(), "eng", "secret", true)
		if err != nil || slug != "eng" {
			t.Fatalf("ResolveTeam = %q, %v", slug, err)
		}
	})
}

func TestSyncTeamMembers(t *testing.T) {
	f := newFakeGitHub(t)
	// github lists child team members as members of the parent team
	f.json("GET /orgs/acme/teams/eng/members", http.StatusOK, []map[string]any{
		{"login": "alice"}, {"login": "carol"}, {"login": "dave"}, {"login": "erin"}, {"login": "gone"}, {"login": "subby"},
	})
	f.json("GET /orgs/acme/teams/eng/invitations", http.StatusOK, []map[string]any{{"login": "frank"}})
	f.json("GET /orgs/acme/teams/eng/teams", http.StatusOK, []map[string]any{{"slug": "eng-sub"}})
	f.json("GET /orgs/acme/teams/eng-sub/members", http.StatusOK, []map[string]any{{"login": "subby"}})
	f.json("PUT /orgs/acme/teams/eng/memberships/{user}", http.StatusOK, map[string]any{"state": "active"})
	f.json("GET /orgs/acme/memberships/carol", http.StatusOK, map[string]any{"state": "active"})
	f.json("GET /orgs/acme/memberships/gone", http.StatusOK, map[string]any{"state": "active"})
	f.json("GET /orgs/acme/memberships/dave", http.StatusNotFound, map[string]any{"message": "Not Found"})
	f.handle("DELETE /orgs/acme/teams/eng/memberships/{user}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("user") == "gone" {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := f.client(t, 1)

	desired := []string{"Alice", "bob", "BOB", "frank", "erin", "bad/../name"}
	result, err := c.SyncTeamMembers(context.Background(), "eng", desired, 0.75)
	if err != nil {
		t.Fatal(err)
	}

	if fmt.Sprint(result.MembersAdded) != "[bob]" {
		t.Errorf("added = %v, want [bob]", result.MembersAdded)
	}
	if fmt.Sprint(result.MembersRemoved) != "[carol]" {
		t.Errorf("removed = %v, want [carol]; a 404 is not a removal", result.MembersRemoved)
	}
	if fmt.Sprint(result.MembersSkippedExternal) != "[dave]" {
		t.Errorf("skipped external = %v, want [dave]", result.MembersSkippedExternal)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "bad/../name") {
		t.Errorf("errors = %v, want the invalid username", result.Errors)
	}
	if fmt.Sprint(result.Members) != "[alice dave erin gone subby bob]" {
		t.Errorf("members = %v", result.Members)
	}
	if f.called("PUT /orgs/acme/teams/eng/memberships/frank") != 0 {
		t.Error("pending invitee re-invited")
	}
	if f.called("DELETE /orgs/acme/teams/eng/memberships/subby") != 0 {
		t.Error("child team member removed from the parent team")
	}
}

func TestSyncTeamMembers_SafetyThreshold(t *testing.T) {
	f := newFakeGitHub(t)
	f.json("GET /orgs/acme/teams/eng/members", http.StatusOK, []map[string]any{
		{"login": "alice"}, {"login": "bob"}, {"login": "carol"},
	})
	f.json("GET /orgs/acme/teams/eng/invitations", http.StatusOK, []any{})
	f.json("GET /orgs/acme/teams/eng/teams", http.StatusOK, []any{})
	c := f.client(t, 1)

	result, err := c.SyncTeamMembers(context.Background(), "eng", nil, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.MembersRemoved) != 0 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "safety threshold") {
		t.Fatalf("result = %+v, want removal refused", result)
	}
	if len(result.Members) != 3 {
		t.Errorf("members = %v, want the unchanged team", result.Members)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is too long", 10, "this is..."},
		{"héllo wörld", 8, "héllo..."},
		{"abcdef", 3, "abc"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.max); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
