package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"goxidized/internal/scheduler"
	"goxidized/pkg/goxidized"
)

func TestAuthFailsClosedWhenTokenMissing(t *testing.T) {
	s := Server{AuthRequired: true, Drivers: func() []string { return []string{"cisco_iosxe"} }, StartedAt: time.Now()}
	rr := serve(s, http.MethodGet, "/api/v1/drivers", "", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", rr.Code)
	}
}

func TestUIRoutesArePublic(t *testing.T) {
	s := Server{AuthRequired: true, Drivers: func() []string { return []string{"cisco_iosxe"} }, StartedAt: time.Now()}

	rr := serve(s, http.MethodGet, "/", "", nil)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/ui/" {
		t.Fatalf("root status=%d location=%q, want 302 /ui/", rr.Code, rr.Header().Get("Location"))
	}

	rr = serve(s, http.MethodGet, "/ui/", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("/ui/ status=%d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "GoXidized") || !strings.Contains(rr.Body.String(), "token-form") {
		t.Fatalf("/ui/ did not serve app shell")
	}

	rr = serve(s, http.MethodGet, "/ui/app.js", "", nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "sessionStorage") {
		t.Fatalf("/ui/app.js status=%d, body did not contain app script", rr.Code)
	}

	rr = serve(s, http.MethodGet, "/ui/assets/network-backup-empty.png", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("asset status=%d, want 200", rr.Code)
	}
	if rr.Header().Get("Cache-Control") == "" {
		t.Fatalf("asset missing Cache-Control")
	}
}

func TestAuthUsesBearerToken(t *testing.T) {
	s := Server{AuthRequired: true, BootstrapToken: "secret-token", Drivers: func() []string { return []string{"cisco_iosxe"} }, StartedAt: time.Now()}
	rr := serve(s, http.MethodGet, "/api/v1/drivers", "wrong", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d, want 401", rr.Code)
	}

	rr = serve(s, http.MethodGet, "/api/v1/drivers", "secret-token", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("correct token status=%d, want 200", rr.Code)
	}
}

func TestAuthUsesStoreBearerAndSessionCookie(t *testing.T) {
	store := newFakeStore()
	store.tokens["api-token"] = principal("api-actor", "drivers:read")
	store.tokens["session-token"] = principal("user-1", "drivers:read")
	s := testServer(store)

	rr := serve(s, http.MethodGet, "/api/v1/drivers", "api-token", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("bearer status=%d, want 200", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/drivers", nil)
	req.AddCookie(&http.Cookie{Name: "goxidized_session", Value: "session-token"})
	rr = httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cookie status=%d, want 200", rr.Code)
	}
}

func TestBootstrapTokenHasAdminOverride(t *testing.T) {
	s := testServer(newFakeStore())
	s.BootstrapToken = "bootstrap"
	rr := serve(s, http.MethodGet, "/api/v1/audit/events", "bootstrap", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d, want 200", rr.Code)
	}
}

func TestRoutePermissions(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		permission string
		wantStatus int
	}{
		{name: "devices", method: http.MethodGet, path: "/api/v1/devices", permission: "devices:read", wantStatus: http.StatusOK},
		{name: "backup", method: http.MethodPost, path: "/api/v1/devices/lab-1/backup", permission: "backups:run", wantStatus: http.StatusAccepted},
		{name: "config history", method: http.MethodGet, path: "/api/v1/devices/lab-1/configs", permission: "configs:read", wantStatus: http.StatusOK},
		{name: "config diff", method: http.MethodGet, path: "/api/v1/devices/lab-1/configs/diff?from=a&to=b", permission: "configs:diff", wantStatus: http.StatusOK},
		{name: "jobs", method: http.MethodGet, path: "/api/v1/jobs", permission: "jobs:read", wantStatus: http.StatusOK},
		{name: "inventory reload", method: http.MethodPost, path: "/api/v1/inventory/reload", permission: "inventory:reload", wantStatus: http.StatusOK},
		{name: "drivers read", method: http.MethodGet, path: "/api/v1/drivers", permission: "drivers:read", wantStatus: http.StatusOK},
		{name: "drivers test", method: http.MethodPost, path: "/api/v1/drivers/cisco_iosxe/test", permission: "drivers:test", wantStatus: http.StatusAccepted},
		{name: "audit read", method: http.MethodGet, path: "/api/v1/audit/events", permission: "audit:read", wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name+" denied", func(t *testing.T) {
			store := newFakeStore()
			store.tokens["token"] = principal("actor", "unrelated:permission")
			s := testServer(store)
			rr := serve(s, tc.method, tc.path, "token", nil)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", rr.Code)
			}
			if !store.hasAudit("permission.denied") {
				t.Fatalf("permission denial was not audited")
			}
		})

		t.Run(tc.name+" allowed", func(t *testing.T) {
			store := newFakeStore()
			store.tokens["token"] = principal("actor", tc.permission)
			s := testServer(store)
			rr := serve(s, tc.method, tc.path, "token", nil)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d, want %d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestAuditsSensitiveAPIActivity(t *testing.T) {
	store := newFakeStore()
	store.tokens["token"] = principal("actor", "configs:read", "configs:diff", "backups:run", "inventory:reload", "drivers:test", "audit:read")
	s := testServer(store)

	paths := []struct {
		method string
		path   string
		action string
	}{
		{http.MethodGet, "/api/v1/devices/lab-1/configs", "config.history.read"},
		{http.MethodGet, "/api/v1/devices/lab-1/configs/latest", "config.latest.read"},
		{http.MethodGet, "/api/v1/devices/lab-1/configs/diff?from=a&to=b", "config.diff.read"},
		{http.MethodPost, "/api/v1/devices/lab-1/backup", "backup.run"},
		{http.MethodPost, "/api/v1/inventory/reload", "inventory.reload"},
		{http.MethodPost, "/api/v1/drivers/cisco_iosxe/test", "driver.test"},
		{http.MethodGet, "/api/v1/audit/events", "audit.events.read"},
	}
	for _, item := range paths {
		rr := serve(s, item.method, item.path, "token", nil)
		if rr.Code < 200 || rr.Code >= 300 {
			t.Fatalf("%s %s status=%d", item.method, item.path, rr.Code)
		}
		if !store.hasAudit(item.action) {
			t.Fatalf("%s was not audited", item.action)
		}
	}

	rr := serve(s, http.MethodGet, "/api/v1/drivers", "bad-token", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d, want 401", rr.Code)
	}
	if !store.hasAudit("auth.failed") {
		t.Fatalf("auth failure was not audited")
	}
}

func TestOIDCLoginRedirect(t *testing.T) {
	store := newFakeStore()
	s := testServer(store)
	s.OIDCEnabled = true
	s.OIDC = fakeOIDC{issuer: "https://issuer.example"}

	rr := serve(s, http.MethodGet, "/auth/oidc/login", "", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "state=") || !strings.Contains(loc, "nonce=") {
		t.Fatalf("redirect missing state or nonce: %s", loc)
	}
	if rr.Result().Cookies()[0].Name == "" {
		t.Fatalf("expected oidc cookies")
	}
}

func TestOIDCCallbackStateMismatchFailsClosed(t *testing.T) {
	store := newFakeStore()
	s := oidcServer(store, fakeOIDC{issuer: "https://issuer.example"})
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code=ok&state=bad", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "good"})
	req.AddCookie(&http.Cookie{Name: nonceCookieName, Value: "nonce"})
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
	if !store.hasAudit("auth.oidc.login") {
		t.Fatalf("oidc failure was not audited")
	}
}

func TestOIDCCallbackUnknownUserFailsClosed(t *testing.T) {
	store := newFakeStore()
	s := oidcServer(store, fakeOIDC{issuer: "https://issuer.example", identity: OIDCIdentity{
		Subject: "sub-1", Email: "user@example.com", EmailVerified: true,
	}})
	rr := oidcCallback(s, "ok", "state", "nonce")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rr.Code)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("unknown user created session")
	}
}

func TestOIDCCallbackKnownUserCreatesSession(t *testing.T) {
	store := newFakeStore()
	store.oidcPrincipals["oidc:https://issuer.example#sub-1"] = goxidized.Principal{
		ActorID: "user-1", ActorType: "user", AuthMethod: "oidc", Subject: "oidc:https://issuer.example#sub-1",
		DisplayName: "User One", Roles: []string{"operator"}, Permissions: []string{"drivers:read"},
	}
	s := oidcServer(store, fakeOIDC{issuer: "https://issuer.example", identity: OIDCIdentity{
		Subject: "sub-1", Email: "user@example.com", EmailVerified: true,
	}})
	rr := oidcCallback(s, "ok", "state", "nonce")
	if rr.Code != http.StatusFound {
		t.Fatalf("status=%d, want 302 body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Location") != "/ui/" {
		t.Fatalf("location=%q, want /ui/", rr.Header().Get("Location"))
	}
	var sessionToken string
	foundCookie := false
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == "goxidized_session" && cookie.Value != "" {
			sessionToken = cookie.Value
			foundCookie = true
		}
	}
	if !foundCookie {
		t.Fatalf("session cookie was not set")
	}
	if store.sessions[tokenHash(sessionToken)] == "" {
		t.Fatalf("session token was not persisted")
	}
	if !store.hasAudit("auth.oidc.login") {
		t.Fatalf("oidc success was not audited")
	}
}

func TestLogoutRevokesSessionToken(t *testing.T) {
	store := newFakeStore()
	store.tokens["session-token"] = principal("user-1", "drivers:read")
	s := testServer(store)
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "goxidized_session", Value: "session-token"})
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	if !store.revoked["session-token"] {
		t.Fatalf("session token was not revoked")
	}
	if !store.hasAudit("auth.logout") {
		t.Fatalf("logout was not audited")
	}
}

func serve(s Server, method, path, bearer string, body *strings.Reader) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		reader = body
	}
	req := httptest.NewRequest(method, path, reader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	return rr
}

func testServer(store *fakeStore) Server {
	return Server{
		Metadata:        store,
		AuthStore:       store,
		Storage:         fakeStorage{},
		Scheduler:       &fakeScheduler{},
		Drivers:         func() []string { return []string{"cisco_iosxe"} },
		ReloadInventory: func(context.Context) error { return nil },
		AuthRequired:    true,
		StartedAt:       time.Now(),
	}
}

func oidcServer(store *fakeStore, oidc fakeOIDC) Server {
	s := testServer(store)
	s.OIDCEnabled = true
	s.OIDC = oidc
	s.OIDCSessionTTL = time.Hour
	s.RequireEmailVerified = true
	return s
}

func oidcCallback(s Server, code, state, nonce string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	req.AddCookie(&http.Cookie{Name: nonceCookieName, Value: nonce})
	rr := httptest.NewRecorder()
	s.Router().ServeHTTP(rr, req)
	return rr
}

func principal(actor string, permissions ...string) goxidized.Principal {
	return goxidized.Principal{
		ActorID:     actor,
		ActorType:   "api_token",
		AuthMethod:  "api_token",
		Roles:       []string{"test-role"},
		Permissions: permissions,
	}
}

type fakeOIDC struct {
	issuer   string
	identity OIDCIdentity
	err      error
}

func (f fakeOIDC) Issuer() string {
	return f.issuer
}

func (f fakeOIDC) AuthCodeURL(state, nonce string) string {
	return "https://issuer.example/auth?state=" + url.QueryEscape(state) + "&nonce=" + url.QueryEscape(nonce)
}

func (f fakeOIDC) Exchange(context.Context, string, string) (OIDCIdentity, error) {
	if f.err != nil {
		return OIDCIdentity{}, f.err
	}
	return f.identity, nil
}

type fakeStore struct {
	tokens         map[string]goxidized.Principal
	oidcPrincipals map[string]goxidized.Principal
	sessions       map[string]string
	revoked        map[string]bool
	audits         []goxidized.AuditEvent
	devices        []goxidized.Target
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		tokens:         make(map[string]goxidized.Principal),
		oidcPrincipals: make(map[string]goxidized.Principal),
		sessions:       make(map[string]string),
		revoked:        make(map[string]bool),
		devices: []goxidized.Target{{
			ID: "lab-1", Hostname: "lab-1", IPAddress: "192.0.2.10", Port: 22,
			Vendor: "cisco_iosxe", Group: "lab", CredentialRef: "dotenv://LAB_1", Enabled: true,
		}},
	}
}

func (f *fakeStore) ValidateAuthToken(_ context.Context, token string) (goxidized.Principal, error) {
	if p, ok := f.tokens[token]; ok {
		return p, nil
	}
	return goxidized.Principal{}, errors.New("bad token")
}

func (f *fakeStore) ResolveOIDCPrincipal(_ context.Context, subject string) (goxidized.Principal, error) {
	if p, ok := f.oidcPrincipals[subject]; ok {
		return p, nil
	}
	return goxidized.Principal{}, errors.New("unknown user")
}

func (f *fakeStore) CreateOIDCSession(_ context.Context, tokenHash string, principal goxidized.Principal, _ time.Time) (string, error) {
	f.sessions[tokenHash] = principal.ActorID
	return "sess-1", nil
}

func (f *fakeStore) RevokeAuthToken(_ context.Context, token string) error {
	f.revoked[token] = true
	return nil
}

func (f *fakeStore) Audit(_ context.Context, ev goxidized.AuditEvent) error {
	f.audits = append(f.audits, ev)
	return nil
}

func (f *fakeStore) hasAudit(action string) bool {
	for _, ev := range f.audits {
		if ev.Action == action {
			return true
		}
	}
	return false
}

func (f *fakeStore) UpsertDevices(context.Context, []goxidized.Target) error { return nil }
func (f *fakeStore) ListDevices(context.Context) ([]goxidized.Target, error) { return f.devices, nil }
func (f *fakeStore) GetDevice(_ context.Context, id string) (goxidized.Target, error) {
	for _, d := range f.devices {
		if d.ID == id {
			return d, nil
		}
	}
	return goxidized.Target{}, errors.New("not found")
}
func (f *fakeStore) RecordJobStart(_ context.Context, job goxidized.Job) (goxidized.Job, error) {
	return job, nil
}
func (f *fakeStore) RecordJobFinish(context.Context, goxidized.JobResult) error { return nil }
func (f *fakeStore) SaveRevision(context.Context, goxidized.Revision, goxidized.CommitMeta) error {
	return nil
}
func (f *fakeStore) SaveDiff(context.Context, goxidized.DiffResult) error { return nil }
func (f *fakeStore) ListJobs(context.Context, int) ([]goxidized.Job, error) {
	return []goxidized.Job{{ID: "job-1", TargetID: "lab-1", Status: goxidized.StatusQueued}}, nil
}
func (f *fakeStore) GetJob(context.Context, string) (goxidized.Job, error) {
	return goxidized.Job{ID: "job-1", TargetID: "lab-1", Status: goxidized.StatusQueued}, nil
}
func (f *fakeStore) ListRevisions(context.Context, string, int) ([]goxidized.Revision, error) {
	return []goxidized.Revision{{ID: "rev-1", TargetID: "lab-1", CommitSHA: "abc"}}, nil
}
func (f *fakeStore) LatestRevision(context.Context, string) (goxidized.Revision, error) {
	return goxidized.Revision{ID: "rev-1", TargetID: "lab-1", CommitSHA: "abc"}, nil
}
func (f *fakeStore) ListAuditEvents(context.Context, int) ([]goxidized.AuditEvent, error) {
	return []goxidized.AuditEvent{{ID: "audit-1", Action: "test", Outcome: "success"}}, nil
}

type fakeStorage struct{}

func (fakeStorage) Save(context.Context, goxidized.Target, goxidized.RedactedConfig, goxidized.CommitMeta) (goxidized.Revision, error) {
	return goxidized.Revision{}, nil
}
func (fakeStorage) Latest(context.Context, string) (goxidized.RedactedConfig, goxidized.Revision, error) {
	return goxidized.RedactedConfig{Content: []byte("version 1")}, goxidized.Revision{ID: "rev-1", CommitSHA: "abc"}, nil
}
func (fakeStorage) History(context.Context, string, int) ([]goxidized.Revision, error) {
	return []goxidized.Revision{{ID: "rev-1"}}, nil
}
func (fakeStorage) Diff(context.Context, string, string, string) (string, error) {
	return "-old\n+new", nil
}

type fakeScheduler struct {
	enqueued []scheduler.Request
}

func (f *fakeScheduler) Enqueue(_ context.Context, req scheduler.Request) error {
	f.enqueued = append(f.enqueued, req)
	return nil
}

func (f *fakeScheduler) QueueDepth() int {
	return len(f.enqueued)
}
