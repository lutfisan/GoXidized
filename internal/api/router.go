package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"goxidized/internal/scheduler"
	"goxidized/pkg/goxidized"
)

type Scheduler interface {
	Enqueue(context.Context, scheduler.Request) error
	QueueDepth() int
}

type TokenValidator interface {
	ValidateAPIToken(ctx context.Context, token string) (actorID string, err error)
}

type Server struct {
	Metadata        goxidized.MetadataStore
	TokenValidator  TokenValidator
	Storage         goxidized.Storage
	Scheduler       Scheduler
	Drivers         func() []string
	ReloadInventory func(context.Context) error
	BootstrapToken  string
	AuthRequired    bool
	StartedAt       time.Time
}

func (s Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Get("/healthz", s.health)
	r.Get("/readyz", s.ready)
	r.Handle("/metrics", promhttp.Handler())
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.auth)
		r.Get("/devices", s.listDevices)
		r.Get("/devices/{id}", s.getDevice)
		r.Post("/devices/{id}/backup", s.backupDevice)
		r.Post("/groups/{group}/backup", s.backupGroup)
		r.Get("/devices/{id}/configs", s.configHistory)
		r.Get("/devices/{id}/configs/latest", s.latestConfig)
		r.Get("/devices/{id}/configs/diff", s.diffConfig)
		r.Get("/jobs", s.listJobs)
		r.Get("/jobs/{id}", s.getJob)
		r.Post("/inventory/reload", s.reloadInventory)
		r.Get("/drivers", s.listDrivers)
		r.Post("/drivers/{name}/test", s.driverTest)
		r.Get("/audit/events", s.auditEvents)
	})
	return r
}

func (s Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.AuthRequired {
			next.ServeHTTP(w, r)
			return
		}
		if s.BootstrapToken == "" {
			if s.TokenValidator == nil {
				writeError(w, http.StatusServiceUnavailable, "api token auth is enabled but no token validator is configured")
				return
			}
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if s.TokenValidator != nil {
			if _, err := s.TokenValidator.ValidateAPIToken(r.Context(), got); err == nil {
				next.ServeHTTP(w, r)
				return
			}
		}
		if s.BootstrapToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.BootstrapToken)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
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
	device, err := s.Metadata.GetDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	job := goxidized.Job{TargetID: device.ID, Group: device.Group, Trigger: "api", Actor: "api-token", Status: goxidized.StatusQueued}
	if err := s.Scheduler.Enqueue(r.Context(), scheduler.Request{Job: job, Target: device}); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "target_id": device.ID})
}

func (s Server) backupGroup(w http.ResponseWriter, r *http.Request) {
	group := chi.URLParam(r, "group")
	devices, err := s.Metadata.ListDevices(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	queued := 0
	var errs []string
	for _, d := range devices {
		if d.Group != group || !d.Enabled {
			continue
		}
		job := goxidized.Job{TargetID: d.ID, Group: d.Group, Trigger: "api", Actor: "api-token", Status: goxidized.StatusQueued}
		if err := s.Scheduler.Enqueue(r.Context(), scheduler.Request{Job: job, Target: d}); err != nil {
			errs = append(errs, d.ID+": "+err.Error())
			continue
		}
		queued++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "group": group, "queued": queued, "errors": errs})
}

func (s Server) configHistory(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 100)
	revs, err := s.Metadata.ListRevisions(r.Context(), chi.URLParam(r, "id"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, revs)
}

func (s Server) latestConfig(w http.ResponseWriter, r *http.Request) {
	cfg, rev, err := s.Storage.Latest(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": rev, "content": string(cfg.Content)})
}

func (s Server) diffConfig(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeError(w, http.StatusBadRequest, "from and to query parameters are required")
		return
	}
	diff, err := s.Storage.Diff(r.Context(), chi.URLParam(r, "id"), from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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
		writeError(w, http.StatusNotImplemented, "inventory reload is not configured")
		return
	}
	if err := s.ReloadInventory(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (s Server) listDrivers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"drivers": s.Drivers()})
}

func (s Server) driverTest(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "driver name required")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "fixture-test endpoint registered", "driver": name})
}

func (s Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Metadata.ListAuditEvents(r.Context(), queryLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
