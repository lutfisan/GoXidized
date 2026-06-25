package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"goxidized/internal/api"
	"goxidized/internal/config"
	"goxidized/internal/credentials"
	"goxidized/internal/credentials/dotenv"
	"goxidized/internal/credentials/encryptedfile"
	"goxidized/internal/drivers"
	invcsv "goxidized/internal/inventory/csv"
	"goxidized/internal/metadata/postgres"
	"goxidized/internal/scheduler"
	"goxidized/internal/storage/gitstore"
	"goxidized/internal/transport/sshtransport"
	"goxidized/internal/transport/telnettransport"
	"goxidized/internal/worker"
	"goxidized/pkg/goxidized"
)

type App struct {
	Config    config.Config
	Metadata  *postgres.Store
	Storage   goxidized.Storage
	Scheduler *scheduler.Manager
	Runner    *worker.BackupRunner
	API       http.Handler
	schedule  *scheduler.Planner
	logger    *slog.Logger
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	drivers.RegisterDefaults()
	dsn := cfg.MetadataDSN()
	if dsn == "" {
		return nil, errors.New("GOXIDIZED_POSTGRES_DSN or storage.metadata.dsn is required")
	}
	meta, err := postgres.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := meta.Migrate(ctx); err != nil {
		meta.Close()
		return nil, err
	}
	storage := gitstore.New(cfg.Storage.Config.BasePath, cfg.Storage.Config.ShardStrategy, cfg.Storage.Config.ShardCount, cfg.Storage.Config.AuthorName, cfg.Storage.Config.AuthorEmail)
	providers := map[string]goxidized.CredentialProvider{
		"dotenv":         dotenv.New(cfg.Credentials.Dotenv.FilePath, cfg.Credentials.Dotenv.RequireChmod600),
		"env":            dotenv.New(cfg.Credentials.Dotenv.FilePath, cfg.Credentials.Dotenv.RequireChmod600),
		"encfile":        encryptedfile.New(cfg.Credentials.EncryptedFile.Path, cfg.Credentials.EncryptedFile.KeyEnv),
		"encrypted-file": encryptedfile.New(cfg.Credentials.EncryptedFile.Path, cfg.Credentials.EncryptedFile.KeyEnv),
	}
	defaultProvider, ok := providers[cfg.Credentials.DefaultProvider]
	if !ok {
		meta.Close()
		return nil, errors.New("unknown credentials.default_provider " + cfg.Credentials.DefaultProvider)
	}
	creds := credentials.Router{Default: defaultProvider, Providers: providers}
	sshDialer := sshtransport.New(sshtransport.Config{
		ConnectTimeout:   cfg.Transport.SSH.ConnectTimeout,
		AuthTimeout:      cfg.Transport.SSH.AuthTimeout,
		CommandTimeout:   cfg.Transport.SSH.CommandTimeout,
		IdleTimeout:      cfg.Transport.SSH.IdleTimeout,
		SessionDeadline:  cfg.Transport.SSH.SessionDeadline,
		InteractiveShell: cfg.Transport.SSH.InteractiveShell,
		PromptPattern:    cfg.Transport.SSH.PromptPattern,
		MaxOutputBytes:   cfg.Transport.SSH.MaxOutputBytes,
		HostKeyMode:      cfg.Transport.SSH.HostKeyMode, KnownHostsPath: cfg.Transport.SSH.KnownHostsPath, TOFUPath: cfg.Transport.SSH.TOFUPath,
		InsecureWarning: func(msg string) { logger.Warn(msg) },
	})
	telnetDialer := telnettransport.NewConfig(telnettransport.Config{
		Enabled:        cfg.Transport.Telnet.Enabled,
		ConnectTimeout: cfg.Transport.Telnet.ConnectTimeout,
		LoginTimeout:   cfg.Transport.Telnet.LoginTimeout,
		CommandTimeout: cfg.Transport.Telnet.CommandTimeout,
		IdleTimeout:    cfg.Transport.Telnet.IdleTimeout,
		PromptPattern:  cfg.Transport.Telnet.PromptPattern,
		MaxOutputBytes: cfg.Transport.Telnet.MaxOutputBytes,
	})
	runner := &worker.BackupRunner{
		Metadata: meta, Storage: storage, Credentials: creds, SSHDialer: sshDialer, TelnetDialer: telnetDialer, Drivers: drivers.Get,
	}
	schedule, err := buildSchedulePlanner(cfg.Scheduler)
	if err != nil {
		meta.Close()
		return nil, err
	}
	var leaseStore scheduler.LeaseStore
	if cfg.Scheduler.Lease.Enabled {
		leaseStore = meta
	}
	mgr := scheduler.New(scheduler.Config{
		QueueSize: cfg.Scheduler.QueueSize, MaxGlobalConcurrency: cfg.Scheduler.MaxGlobalConcurrency,
		MaxPerVendorConcurrency: cfg.Scheduler.MaxPerVendorConcurrency, MaxPerGroupConcurrency: cfg.Scheduler.MaxPerGroupConcurrency,
		MaxPerSiteConcurrency: cfg.Scheduler.MaxPerSiteConcurrency, MaxNewConnectionsPerSecond: cfg.Scheduler.MaxNewConnectionsPerSecond,
		MaxAttempts: cfg.Scheduler.Retry.MaxAttempts, BackoffInitial: cfg.Scheduler.Retry.BackoffInitial, BackoffMax: cfg.Scheduler.Retry.BackoffMax,
		WorkerID: cfg.Scheduler.Lease.WorkerID, LeaseStore: leaseStore, LeaseTTL: cfg.Scheduler.Lease.TTL, LeaseRenewInterval: cfg.Scheduler.Lease.RenewInterval,
	}, runner.Handle)
	a := &App{Config: cfg, Metadata: meta, Storage: storage, Scheduler: mgr, Runner: runner, schedule: schedule, logger: logger}
	var oidcAuth api.OIDCAuthenticator
	if cfg.Server.Auth.OIDCActive() {
		secret := os.Getenv(cfg.Server.Auth.OIDC.ClientSecretEnv)
		if secret == "" {
			meta.Close()
			return nil, fmt.Errorf("%s is required when OIDC auth is enabled", cfg.Server.Auth.OIDC.ClientSecretEnv)
		}
		oidcAuth, err = api.NewOIDCAuthenticator(ctx, api.OIDCSettings{
			IssuerURL:    cfg.Server.Auth.OIDC.IssuerURL,
			ClientID:     cfg.Server.Auth.OIDC.ClientID,
			ClientSecret: secret,
			RedirectURL:  cfg.Server.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Server.Auth.OIDC.Scopes,
		})
		if err != nil {
			meta.Close()
			return nil, fmt.Errorf("configure oidc auth: %w", err)
		}
	}
	a.API = api.Server{
		Metadata: meta, AuthStore: meta, Storage: storage, Scheduler: mgr, Drivers: drivers.List, ReloadInventory: a.ReloadInventory,
		BootstrapToken: os.Getenv(cfg.Server.Auth.BootstrapTokenEnv), AuthRequired: cfg.Server.Auth.APITokensEnabled || cfg.Server.Auth.OIDCActive(),
		OIDC: oidcAuth, OIDCEnabled: cfg.Server.Auth.OIDCActive(), OIDCSessionTTL: cfg.Server.Auth.OIDC.SessionTTL,
		OIDCCookieName: cfg.Server.Auth.OIDC.CookieName, RequireEmailVerified: cfg.Server.Auth.OIDC.RequireEmailVerified, StartedAt: time.Now().UTC(),
	}.Router()
	return a, nil
}

func (a *App) RunBackup(ctx context.Context, target goxidized.Target, trigger, actor string) goxidized.JobResult {
	return a.Runner.Handle(ctx, scheduler.Request{
		Target: target,
		Job:    goxidized.Job{TargetID: target.ID, Group: target.Group, Trigger: trigger, Actor: actor, Status: goxidized.StatusQueued, QueuedAt: time.Now().UTC()},
	})
}

func (a *App) Close() {
	if a.Metadata != nil {
		a.Metadata.Close()
	}
}

func (a *App) ReloadInventory(ctx context.Context) error {
	var all []goxidized.Target
	var firstErr error
	for _, src := range a.Config.Inventory.Sources {
		if src.Type != "csv" {
			if firstErr == nil {
				firstErr = errors.New("unsupported inventory source type " + src.Type)
			}
			continue
		}
		targets, err := invcsv.New(src.Path).Load(ctx)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		all = append(all, targets...)
	}
	if len(all) > 0 {
		if err := a.Metadata.UpsertDevices(ctx, all); err != nil {
			return err
		}
	}
	return firstErr
}

func (a *App) Start(ctx context.Context) error {
	if err := a.ReloadInventory(ctx); err != nil {
		a.logger.Warn("inventory loaded with validation issues", "error", err)
	}
	a.startSweeper(ctx)
	a.Scheduler.Start(ctx, a.Config.Scheduler.MaxGlobalConcurrency)
	server := &http.Server{Addr: a.Config.Server.ListenAddress, Handler: a.API}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	a.logger.Info("starting API server", "listen_address", a.Config.Server.ListenAddress)
	var err error
	if a.Config.Server.TLSEnabled {
		err = server.ListenAndServeTLS(a.Config.Server.TLSCertFile, a.Config.Server.TLSKeyFile)
	} else {
		err = server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) startSweeper(ctx context.Context) {
	if a.schedule == nil {
		return
	}
	go func() {
		base, err := a.schedule.InitialBase(time.Now().UTC())
		if err != nil {
			a.logger.Warn("scheduler disabled", "error", err)
			return
		}
		for {
			if wait := time.Until(base); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			a.enqueueSweep(ctx, base)
			next, err := a.schedule.NextBase(base)
			if err != nil {
				a.logger.Warn("scheduler stopped", "error", err)
				return
			}
			base = next
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
}

func (a *App) enqueueSweep(ctx context.Context, base time.Time) {
	devices, err := a.Metadata.ListDevices(ctx)
	if err != nil {
		a.logger.Warn("sweep inventory read failed", "error", err)
		return
	}
	for _, t := range devices {
		if !t.Enabled {
			continue
		}
		decision, err := a.schedule.QueueTime(base, t)
		if err != nil {
			a.logger.Warn("sweep schedule skipped", "target_id", t.ID, "error", err)
			continue
		}
		job := goxidized.Job{TargetID: t.ID, Group: t.Group, Trigger: "schedule", Actor: "scheduler", Status: goxidized.StatusQueued, QueuedAt: decision.QueueAt}
		if err := a.Scheduler.Enqueue(ctx, scheduler.Request{Job: job, Target: t}); err != nil {
			a.logger.Debug("sweep enqueue skipped", "target_id", t.ID, "error", err)
		}
	}
}

func buildSchedulePlanner(cfg config.SchedulerConfig) (*scheduler.Planner, error) {
	if cfg.DefaultInterval <= 0 && cfg.Cron == "" {
		return nil, nil
	}
	loc := time.UTC
	if cfg.Timezone != "" {
		loaded, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("scheduler.timezone: %w", err)
		}
		loc = loaded
	}
	windows, err := buildScheduleWindows(cfg.Windows, loc)
	if err != nil {
		return nil, err
	}
	blackouts, err := buildScheduleWindows(cfg.Blackouts, loc)
	if err != nil {
		return nil, err
	}
	return scheduler.NewPlanner(scheduler.SchedulePolicy{
		Interval: cfg.DefaultInterval, Cron: cfg.Cron, Location: loc, JitterPercent: cfg.JitterPercent, JitterMax: cfg.JitterMax,
		Windows: windows, Blackouts: blackouts,
	})
}

func buildScheduleWindows(in []config.WindowConfig, fallback *time.Location) ([]scheduler.ScheduleWindow, error) {
	out := make([]scheduler.ScheduleWindow, 0, len(in))
	for _, window := range in {
		loc := fallback
		if window.Timezone != "" {
			loaded, err := time.LoadLocation(window.Timezone)
			if err != nil {
				return nil, fmt.Errorf("scheduler window %q timezone: %w", window.Name, err)
			}
			loc = loaded
		}
		start, err := scheduler.ParseClock(window.Start)
		if err != nil {
			return nil, fmt.Errorf("scheduler window %q start: %w", window.Name, err)
		}
		end, err := scheduler.ParseClock(window.End)
		if err != nil {
			return nil, fmt.Errorf("scheduler window %q end: %w", window.Name, err)
		}
		days, err := scheduler.ParseWeekdays(window.Days)
		if err != nil {
			return nil, fmt.Errorf("scheduler window %q days: %w", window.Name, err)
		}
		out = append(out, scheduler.ScheduleWindow{
			Name: window.Name, Days: days, Start: start, End: end, Location: loc,
			Groups: window.Groups, Sites: window.Sites, Vendors: window.Vendors, Roles: window.Roles,
		})
	}
	return out, nil
}
