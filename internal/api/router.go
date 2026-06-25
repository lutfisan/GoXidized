package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"goxidized/internal/scheduler"
	"goxidized/internal/web"
	"goxidized/pkg/goxidized"
)

const (
	stateCookieName = "goxidized_oidc_state"
	nonceCookieName = "goxidized_oidc_nonce"
)

type Scheduler interface {
	Enqueue(context.Context, scheduler.Request) error
	QueueDepth() int
}

type AuthStore interface {
	ValidateAuthToken(ctx context.Context, token string) (goxidized.Principal, error)
	ResolveOIDCPrincipal(ctx context.Context, subject string) (goxidized.Principal, error)
	CreateOIDCSession(ctx context.Context, tokenHash string, principal goxidized.Principal, expiresAt time.Time) (string, error)
	RevokeAuthToken(ctx context.Context, token string) error
	Audit(ctx context.Context, ev goxidized.AuditEvent) error
}

type auditor interface {
	Audit(ctx context.Context, ev goxidized.AuditEvent) error
}

type contextKey string

const (
	principalContextKey contextKey = "principal"
	authTokenContextKey contextKey = "auth_token"
)

type Server struct {
	Metadata             goxidized.MetadataStore
	AuthStore            AuthStore
	Storage              goxidized.Storage
	Scheduler            Scheduler
	Drivers              func() []string
	ReloadInventory      func(context.Context) error
	BootstrapToken       string
	AuthRequired         bool
	OIDC                 OIDCAuthenticator
	OIDCEnabled          bool
	OIDCSessionTTL       time.Duration
	OIDCCookieName       string
	RequireEmailVerified bool
	StartedAt            time.Time
}

func (s Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	r.Get("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
	r.Handle("/ui/assets/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.StripPrefix("/ui/", web.Handler()).ServeHTTP(w, r)
	}))
	r.Handle("/ui/*", http.StripPrefix("/ui/", web.Handler()))
	r.Get("/healthz", s.health)
	r.Get("/readyz", s.ready)
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/auth/oidc/login", s.oidcLogin)
	r.Get("/auth/oidc/callback", s.oidcCallback)
	r.With(s.auth).Post("/auth/logout", s.logout)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/auth/me", s.me)
		r.With(s.require("devices:read")).Get("/devices", s.listDevices)
		r.With(s.require("devices:read")).Get("/devices/{id}", s.getDevice)
		r.With(s.require("backups:run")).Post("/devices/{id}/backup", s.backupDevice)
		r.With(s.require("backups:run")).Post("/groups/{group}/backup", s.backupGroup)
		r.With(s.require("configs:read")).Get("/devices/{id}/configs", s.configHistory)
		r.With(s.require("configs:read")).Get("/devices/{id}/configs/latest", s.latestConfig)
		r.With(s.require("configs:diff")).Get("/devices/{id}/configs/diff", s.diffConfig)
		r.With(s.require("jobs:read")).Get("/jobs", s.listJobs)
		r.With(s.require("jobs:read")).Get("/jobs/{id}", s.getJob)
		r.With(s.require("inventory:reload")).Post("/inventory/reload", s.reloadInventory)
		r.With(s.require("drivers:read")).Get("/drivers", s.listDrivers)
		r.With(s.require("drivers:test")).Post("/drivers/{name}/test", s.driverTest)
		r.With(s.require("audit:read")).Get("/audit/events", s.auditEvents)
	})
	return r
}

func (s Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AuthRequired {
			next.ServeHTTP(w, r.WithContext(contextWithPrincipal(r.Context(), goxidized.Principal{
				ActorID:     "anonymous",
				ActorType:   "anonymous",
				AuthMethod:  "disabled",
				Permissions: []string{"*"},
			})))
			return
		}
		if s.BootstrapToken == "" && s.AuthStore == nil {
			writeError(w, http.StatusServiceUnavailable, "auth is enabled but no token validator is configured")
			return
		}
		token, source := s.authToken(r)
		if token == "" {
			s.audit(r, "auth.failed", "missing_token", "", map[string]string{"route": r.URL.Path})
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if s.AuthStore != nil {
			principal, err := s.AuthStore.ValidateAuthToken(r.Context(), token)
			if err == nil && !principal.IsZero() {
				if principal.AuthMethod == "" {
					principal.AuthMethod = source
				}
				ctx := contextWithPrincipal(r.Context(), principal)
				ctx = context.WithValue(ctx, authTokenContextKey, token)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		if source == "bearer" && s.BootstrapToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.BootstrapToken)) == 1 {
			principal := goxidized.Principal{
				ActorID:     "bootstrap",
				ActorType:   "bootstrap",
				AuthMethod:  "bootstrap",
				Roles:       []string{"admin"},
				Permissions: []string{"*"},
			}
			ctx := contextWithPrincipal(r.Context(), principal)
			ctx = context.WithValue(ctx, authTokenContextKey, token)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		s.audit(r, "auth.failed", "invalid_token", "", map[string]string{"route": r.URL.Path, "auth_source": source})
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

func (s Server) require(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := PrincipalFromContext(r.Context())
			if ok && principal.HasPermission(permission) {
				next.ServeHTTP(w, r)
				return
			}
			s.audit(r, "permission.denied", "denied", chi.URLParam(r, "id"), map[string]string{
				"permission": permission,
				"route":      r.URL.Path,
			})
			writeError(w, http.StatusForbidden, "forbidden")
		})
	}
}

func PrincipalFromContext(ctx context.Context) (goxidized.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey).(goxidized.Principal)
	return principal, ok
}

func contextWithPrincipal(ctx context.Context, principal goxidized.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func (s Server) authToken(r *http.Request) (string, string) {
	if value := r.Header.Get("Authorization"); strings.HasPrefix(value, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(value, "Bearer ")), "bearer"
	}
	if cookieName := s.sessionCookieName(); cookieName != "" {
		if cookie, err := r.Cookie(cookieName); err == nil {
			return cookie.Value, "cookie"
		}
	}
	return "", ""
}

func (s Server) sessionCookieName() string {
	if s.OIDCCookieName != "" {
		return s.OIDCCookieName
	}
	return "goxidized_session"
}

func (s Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "uptime_seconds": int(time.Since(s.StartedAt).Seconds())})
}

func (s Server) ready(w http.ResponseWriter, r *http.Request) {
	if s.Metadata == nil || s.Storage == nil || s.Scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "dependencies not ready")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "queue_depth": s.Scheduler.QueueDepth()})
}

func (s Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if !s.OIDCEnabled || s.OIDC == nil {
		writeError(w, http.StatusNotFound, "oidc login is not enabled")
		return
	}
	state, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create oidc state")
		return
	}
	nonce, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create oidc nonce")
		return
	}
	setTransientCookie(w, r, stateCookieName, state)
	setTransientCookie(w, r, nonceCookieName, nonce)
	http.Redirect(w, r, s.OIDC.AuthCodeURL(state, nonce), http.StatusFound)
}

func (s Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if !s.OIDCEnabled || s.OIDC == nil || s.AuthStore == nil {
		writeError(w, http.StatusNotFound, "oidc login is not enabled")
		return
	}
	clearCookie(w, r, stateCookieName, "/auth/oidc")
	clearCookie(w, r, nonceCookieName, "/auth/oidc")
	if msg := r.URL.Query().Get("error"); msg != "" {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"error": msg})
		writeError(w, http.StatusUnauthorized, "oidc login failed")
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	stateCookie, stateErr := r.Cookie(stateCookieName)
	nonceCookie, nonceErr := r.Cookie(nonceCookieName)
	if code == "" || state == "" || stateErr != nil || nonceErr != nil || stateCookie.Value != state {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"error": "state_mismatch"})
		writeError(w, http.StatusBadRequest, "invalid oidc state")
		return
	}
	identity, err := s.OIDC.Exchange(r.Context(), code, nonceCookie.Value)
	if err != nil {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"error": "token_exchange_or_verify_failed"})
		writeError(w, http.StatusUnauthorized, "oidc token verification failed")
		return
	}
	if s.RequireEmailVerified && !identity.EmailVerified {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"subject": s.oidcSubject(identity.Subject), "error": "email_not_verified"})
		writeError(w, http.StatusForbidden, "oidc email is not verified")
		return
	}
	principal, err := s.AuthStore.ResolveOIDCPrincipal(r.Context(), s.oidcSubject(identity.Subject))
	if err != nil {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"subject": s.oidcSubject(identity.Subject), "error": "unknown_or_unassigned_user"})
		writeError(w, http.StatusForbidden, "oidc user is not provisioned")
		return
	}
	if len(principal.Roles) == 0 {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"subject": principal.Subject, "error": "no_roles"})
		writeError(w, http.StatusForbidden, "oidc user has no roles")
		return
	}
	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session token")
		return
	}
	ttl := s.OIDCSessionTTL
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	expiresAt := time.Now().UTC().Add(ttl)
	principal.AuthMethod = "oidc_session"
	if principal.DisplayName == "" {
		principal.DisplayName = identity.DisplayName
	}
	sessionID, err := s.AuthStore.CreateOIDCSession(r.Context(), tokenHash(token), principal, expiresAt)
	if err != nil {
		s.audit(r, "auth.oidc.login", "failed", "", map[string]string{"subject": principal.Subject, "error": "session_create_failed"})
		writeError(w, http.StatusInternalServerError, "failed to create oidc session")
		return
	}
	principal.TokenID = sessionID
	principal.ExpiresAt = expiresAt
	setSessionCookie(w, r, s.sessionCookieName(), token, expiresAt)
	s.audit(r.WithContext(contextWithPrincipal(r.Context(), principal)), "auth.oidc.login", "success", "", map[string]string{"subject": principal.Subject})
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

func (s Server) oidcSubject(sub string) string {
	if s.OIDC == nil {
		return "oidc:#" + sub
	}
	return "oidc:" + s.OIDC.Issuer() + "#" + sub
}

func (s Server) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := r.Context().Value(authTokenContextKey).(string)
	outcome := "success"
	if token != "" && s.AuthStore != nil {
		if err := s.AuthStore.RevokeAuthToken(r.Context(), token); err != nil {
			outcome = "failed"
		}
	}
	clearCookie(w, r, s.sessionCookieName(), "/")
	s.audit(r, "auth.logout", outcome, "", nil)
	if outcome == "failed" {
		writeError(w, http.StatusInternalServerError, "failed to revoke session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s Server) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := PrincipalFromContext(r.Context())
	writeJSON(w, http.StatusOK, principal)
}

func (s Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.Metadata.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (s Server) getDevice(w http.ResponseWriter, r *http.Request) {
	device, err := s.Metadata.GetDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, device)
}

func (s Server) backupDevice(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")
	device, err := s.Metadata.GetDevice(r.Context(), targetID)
	if err != nil {
		s.audit(r, "backup.run", "not_found", targetID, map[string]string{"scope": "device"})
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	job := goxidized.Job{TargetID: device.ID, Group: device.Group, Trigger: "api", Actor: principal.ActorID, Status: goxidized.StatusQueued}
	if err := s.Scheduler.Enqueue(r.Context(), scheduler.Request{Job: job, Target: device}); err != nil {
		s.audit(r, "backup.run", "failed", device.ID, map[string]string{"scope": "device", "error": err.Error()})
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "backup.run", "queued", device.ID, map[string]string{"scope": "device", "queued_count": "1"})
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "target_id": device.ID})
}

func (s Server) backupGroup(w http.ResponseWriter, r *http.Request) {
	group := chi.URLParam(r, "group")
	devices, err := s.Metadata.ListDevices(r.Context())
	if err != nil {
		s.audit(r, "backup.run", "failed", "", map[string]string{"scope": "group", "group": group, "error": err.Error()})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	principal, _ := PrincipalFromContext(r.Context())
	queued := 0
	var errs []string
	for _, d := range devices {
		if d.Group != group || !d.Enabled {
			continue
		}
		job := goxidized.Job{TargetID: d.ID, Group: d.Group, Trigger: "api", Actor: principal.ActorID, Status: goxidized.StatusQueued}
		if err := s.Scheduler.Enqueue(r.Context(), scheduler.Request{Job: job, Target: d}); err != nil {
			errs = append(errs, d.ID+": "+err.Error())
			continue
		}
		queued++
	}
	outcome := "queued"
	if len(errs) > 0 {
		outcome = "partial"
	}
	s.audit(r, "backup.run", outcome, "", map[string]string{
		"scope":        "group",
		"group":        group,
		"queued_count": strconv.Itoa(queued),
		"error_count":  strconv.Itoa(len(errs)),
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "group": group, "queued": queued, "errors": errs})
}

func (s Server) configHistory(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")
	limit := queryLimit(r, 100)
	revs, err := s.Metadata.ListRevisions(r.Context(), targetID, limit)
	if err != nil {
		s.audit(r, "config.history.read", "failed", targetID, map[string]string{"limit": strconv.Itoa(limit)})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "config.history.read", "success", targetID, map[string]string{"limit": strconv.Itoa(limit), "count": strconv.Itoa(len(revs))})
	writeJSON(w, http.StatusOK, revs)
}

func (s Server) latestConfig(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")
	cfg, rev, err := s.Storage.Latest(r.Context(), targetID)
	if err != nil {
		s.audit(r, "config.latest.read", "failed", targetID, nil)
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.audit(r, "config.latest.read", "success", targetID, map[string]string{"revision": rev.ID, "commit_sha": rev.CommitSHA})
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev, "content": string(cfg.Content)})
}

func (s Server) diffConfig(w http.ResponseWriter, r *http.Request) {
	targetID := chi.URLParam(r, "id")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		s.audit(r, "config.diff.read", "bad_request", targetID, map[string]string{"from": from, "to": to})
		writeError(w, http.StatusBadRequest, "from and to query parameters are required")
		return
	}
	diff, err := s.Storage.Diff(r.Context(), targetID, from, to)
	if err != nil {
		s.audit(r, "config.diff.read", "failed", targetID, map[string]string{"from": from, "to": to})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "config.diff.read", "success", targetID, map[string]string{"from": from, "to": to})
	writeJSON(w, http.StatusOK, map[string]string{"diff": diff})
}

func (s Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Metadata.ListJobs(r.Context(), queryLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Metadata.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s Server) reloadInventory(w http.ResponseWriter, r *http.Request) {
	if s.ReloadInventory == nil {
		s.audit(r, "inventory.reload", "not_configured", "", nil)
		writeError(w, http.StatusNotImplemented, "inventory reload is not configured")
		return
	}
	if err := s.ReloadInventory(r.Context()); err != nil {
		s.audit(r, "inventory.reload", "failed", "", map[string]string{"error": err.Error()})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "inventory.reload", "success", "", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (s Server) listDrivers(w http.ResponseWriter, r *http.Request) {
	drivers := []string(nil)
	if s.Drivers != nil {
		drivers = s.Drivers()
	}
	writeJSON(w, http.StatusOK, map[string]any{"drivers": drivers})
}

func (s Server) driverTest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		s.audit(r, "driver.test", "bad_request", "", nil)
		writeError(w, http.StatusBadRequest, "driver name required")
		return
	}
	s.audit(r, "driver.test", "accepted", name, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "fixture-test endpoint registered", "driver": name})
}

func (s Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 100)
	events, err := s.Metadata.ListAuditEvents(r.Context(), limit)
	if err != nil {
		s.audit(r, "audit.events.read", "failed", "", map[string]string{"limit": strconv.Itoa(limit)})
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "audit.events.read", "success", "", map[string]string{"limit": strconv.Itoa(limit), "count": strconv.Itoa(len(events))})
	writeJSON(w, http.StatusOK, events)
}

func queryLimit(r *http.Request, def int) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		return def
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

func (s Server) audit(r *http.Request, action, outcome, targetID string, metadata map[string]string) {
	store := s.auditStore()
	if store == nil {
		return
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok || principal.IsZero() {
		principal = goxidized.Principal{ActorType: "anonymous", ActorID: "anonymous"}
	}
	meta := map[string]string{
		"method": r.Method,
		"route":  r.URL.Path,
	}
	for k, v := range metadata {
		meta[k] = v
	}
	_ = store.Audit(r.Context(), goxidized.AuditEvent{
		ActorType: principal.ActorType,
		ActorID:   principal.ActorID,
		Action:    action,
		TargetID:  targetID,
		RequestID: middleware.GetReqID(r.Context()),
		SourceIP:  sourceIP(r),
		Outcome:   outcome,
		Metadata:  meta,
	})
}

func (s Server) auditStore() auditor {
	if s.AuthStore != nil {
		return s.AuthStore
	}
	if s.Metadata != nil {
		return s.Metadata
	}
	return nil
}

func sourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func setTransientCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/auth/oidc",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, name, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})
}

func clearCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func randomToken(size int) (string, error) {
	if size <= 0 {
		return "", errors.New("token size must be positive")
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
