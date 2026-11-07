package okta

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func testKeyPEM(t *testing.T, pkcs8 bool) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if !pkcs8 {
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestToPKCS1(t *testing.T) {
	pkcs1 := testKeyPEM(t, false)
	pkcs8 := testKeyPEM(t, true)
	escaped := []byte(strings.ReplaceAll(string(pkcs8), "\n", `\n`))

	for name, in := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8, "escaped": escaped} {
		out, err := toPKCS1(in)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		block, _ := pem.Decode(out)
		if block == nil || block.Type != "RSA PRIVATE KEY" {
			t.Fatalf("%s: output is not a pkcs1 pem block", name)
		}
	}

	for name, in := range map[string][]byte{
		"empty":   nil,
		"garbage": []byte("nope"),
		"cert":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1}}),
	} {
		if _, err := toPKCS1(in); err == nil {
			t.Errorf("%s: toPKCS1 succeeded, want error", name)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	tests := []struct{ in, want string }{
		{"octocat", "octocat"},
		{"\t@octocat\n", "octocat"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeUsername(tt.in); got != tt.want {
			t.Errorf("normalizeUsername(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// fakeOkta serves the token endpoint, two pages of groups, and group members.
func fakeOkta(t *testing.T, members map[string][]map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	group := func(id, name string) map[string]any {
		return map[string]any{"id": id, "type": "OKTA_GROUP", "objectClass": []string{"okta:user_group"}, "profile": map[string]any{"name": name}}
	}

	mux.HandleFunc("POST /oauth2/v1/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"token_type": "Bearer", "expires_in": 3600, "access_token": "okta-token", "scope": "okta.groups.read okta.users.read"})
	})
	mux.HandleFunc("GET /api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer okta-token" {
			t.Errorf("groups request authorization = %q", r.Header.Get("Authorization"))
		}
		if q := r.URL.Query().Get("q"); q != "" {
			writeJSON(w, []any{group("g-eng", "eng-platform"), group("g-eng2", q)})
			return
		}
		if r.URL.Query().Get("after") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<https://%s/api/v1/groups?after=g2&limit=200>; rel="next"`, r.Host))
			writeJSON(w, []any{group("g-eng", "eng-platform"), group("g-sales", "sales")})
			return
		}
		writeJSON(w, []any{group("g-broken", "eng-broken"), group("g-data", "eng-data")})
	})
	mux.HandleFunc("GET /api/v1/groups/{id}/users", func(w http.ResponseWriter, r *http.Request) {
		users, ok := members[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"errorCode": "E0000009", "errorSummary": "internal error"})
			return
		}
		writeJSON(w, users)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func user(status, email string, profile map[string]any) map[string]any {
	p := map[string]any{"email": email, "login": email}
	for k, v := range profile {
		p[k] = v
	}
	return map[string]any{"id": "u-" + email, "status": status, "profile": p}
}

func newTestClient(t *testing.T, srv *httptest.Server, field string) *Client {
	t.Helper()
	c, err := New(Options{
		Domain:          "example.okta.com",
		ClientID:        "client",
		PrivateKey:      testKeyPEM(t, true),
		GitHubUserField: field,
		BaseURL:         srv.URL,
		HTTPClient:      srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGetGroupsByPattern(t *testing.T) {
	srv := fakeOkta(t, map[string][]map[string]any{
		"g-eng": {
			user("ACTIVE", "a@x.com", map[string]any{"githubUsername": "alice"}),
			user("PASSWORD_EXPIRED", "b@x.com", map[string]any{"githubUsername": " @bob "}),
			user("LOCKED_OUT", "c@x.com", map[string]any{"githubUsername": "carol"}),
			user("SUSPENDED", "d@x.com", map[string]any{"githubUsername": "dave"}),
			user("DEPROVISIONED", "e@x.com", map[string]any{"githubUsername": "erin"}),
			user("STAGED", "f@x.com", map[string]any{"githubUsername": "frank"}),
			user("ACTIVE", "g@x.com", nil),
			user("ACTIVE", "h@x.com", map[string]any{"githubUsername": "../../admin"}),
		},
		"g-data": {user("ACTIVE", "i@x.com", map[string]any{"githubUsername": "ivan"})},
	})
	c := newTestClient(t, srv, "githubUsername")

	groups, err := c.GetGroupsByPattern(context.Background(), "^eng-")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 3 {
		t.Fatalf("matched %d groups, want 3 across both pages", len(groups))
	}

	byName := map[string]int{}
	for i, g := range groups {
		byName[g.Name] = i
	}
	eng := groups[byName["eng-platform"]]
	if got := strings.Join(eng.Members, ","); got != "alice,bob,carol" {
		t.Errorf("members = %s, want alice,bob,carol", got)
	}
	if len(eng.SkippedNoGitHubUsername) != 2 || !slices.Contains(eng.SkippedNoGitHubUsername, "g@x.com") ||
		!strings.Contains(eng.SkippedNoGitHubUsername[1], "invalid github username") {
		t.Errorf("skipped = %v, want the user without a username and the invalid one", eng.SkippedNoGitHubUsername)
	}
	if groups[byName["eng-broken"]].Err == nil {
		t.Error("group whose members failed to load has no error")
	}
	if groups[byName["eng-data"]].Err != nil {
		t.Errorf("eng-data error = %v", groups[byName["eng-data"]].Err)
	}
}

func TestGetGroupInfo_StandardProfileField(t *testing.T) {
	srv := fakeOkta(t, map[string][]map[string]any{
		"g-eng2": {user("ACTIVE", "a@x.com", map[string]any{"nickName": "alice"})},
	})
	c := newTestClient(t, srv, "nickName")

	group, err := c.GetGroupInfo(context.Background(), "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	if group.ID != "g-eng2" || strings.Join(group.Members, ",") != "alice" {
		t.Errorf("group = %+v, want g-eng2 with alice", group)
	}
}
