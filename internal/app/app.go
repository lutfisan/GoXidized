package app

import (
	"context"
	"errors"
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
		ConnectTimeout:  cfg.Transport.SSH.ConnectTimeout,
		AuthTimeout:     cfg.Transport.SSH.AuthTimeout,
		CommandTimeout:  cfg.Transport.SSH.CommandTimeout,
		IdleTimeout:     cfg.Transport.SSH.IdleTimeout,
		SessionDeadline: cfg.Transport.SSH.SessionDeadline,
		HostKeyMode:     cfg.Transport.SSH.HostKeyMode, KnownHostsPath: cfg.Transport.SSH.KnownHostsPath, TOFUPath: cfg.Transport.SSH.TOFUPath,
		InsecureWarning: func(msg string) { logger.Warn(msg) },
	})
	telnetDialer := telnettransport.New(cfg.Transport.Telnet.Enabled)
	runner := &worker.BackupRunner{
		Metadata: meta, Storage: storage, Credentials: creds, SSHDialer: sshDialer, TelnetDialer: telnetDialer, Drivers: drivers.Get,
	}
	mgr := scheduler.New(scheduler.Config{
		QueueSize: cfg.Scheduler.QueueSize, MaxGlobalConcurrency: cfg.Scheduler.MaxGlobalConcurrency,
		MaxPerVendorConcurrency: cfg.Scheduler.MaxPerVendorConcurrency, MaxPerGroupConcurrency: cfg.Scheduler.MaxPerGroupConcurrency,
		MaxPerSiteConcurrency: cfg.Scheduler.MaxPerSiteConcurrency, MaxNewConnectionsPerSecond: cfg.Scheduler.MaxNewConnectionsPerSecond,
		MaxAttempts: cfg.Scheduler.Retry.MaxAttempts, BackoffInitial: cfg.Scheduler.Retry.BackoffInitial, BackoffMax: cfg.Scheduler.Retry.BackoffMax,
	}, runner.Handle)
	a := &App{Config: cfg, Metadata: meta, Storage: storage, Scheduler: mgr, Runner: runner, logger: logger}
	a.API = api.Server{
		Metadata: meta, TokenValidator: meta, Storage: storage, Scheduler: mgr, Drivers: drivers.List, ReloadInventory: a.ReloadInventory,
		BootstrapToken: os.Getenv(cfg.Server.Auth.BootstrapTokenEnv), AuthRequired: cfg.Server.Auth.APITokensEnabled, StartedAt: time.Now().UTC(),
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
	interval := a.Config.Scheduler.DefaultInterval
	if interval <= 0 {
		return
	}
	go func() {
		a.enqueueSweep(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.enqueueSweep(ctx)
			}
		}
	}()
}

func (a *App) enqueueSweep(ctx context.Context) {
	devices, err := a.Metadata.ListDevices(ctx)
	if err != nil {
		a.logger.Warn("sweep inventory read failed", "error", err)
		return
	}
	for _, t := range devices {
		if !t.Enabled {
			continue
		}
		delay := deterministicJitter(t.ID, a.Config.Scheduler.DefaultInterval, a.Config.Scheduler.JitterPercent)
		job := goxidized.Job{TargetID: t.ID, Group: t.Group, Trigger: "schedule", Actor: "scheduler", Status: goxidized.StatusQueued, QueuedAt: time.Now().UTC().Add(delay)}
		if err := a.Scheduler.Enqueue(ctx, scheduler.Request{Job: job, Target: t}); err != nil {
			a.logger.Debug("sweep enqueue skipped", "target_id", t.ID, "error", err)
		}
	}
}

func deterministicJitter(key string, interval time.Duration, percent int) time.Duration {
	if percent <= 0 || interval <= 0 {
		return 0
	}
	max := interval * time.Duration(percent) / 100
	if max <= 0 {
		return 0
	}
	var n uint64
	for _, b := range []byte(key) {
		n = n*131 + uint64(b)
	}
	return time.Duration(n % uint64(max))
}
