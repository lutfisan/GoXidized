package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"goxidized/pkg/goxidized"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Scheduler     SchedulerConfig     `yaml:"scheduler"`
	Inventory     InventoryConfig     `yaml:"inventory"`
	Credentials   CredentialsConfig   `yaml:"credentials"`
	Storage       StorageConfig       `yaml:"storage"`
	Redaction     RedactionConfig     `yaml:"redaction"`
	Transport     TransportConfig     `yaml:"transport"`
	Retention     RetentionConfig     `yaml:"retention"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type ServerConfig struct {
	ListenAddress string     `yaml:"listen_address"`
	TLSEnabled    bool       `yaml:"tls_enabled"`
	TLSCertFile   string     `yaml:"tls_cert_file"`
	TLSKeyFile    string     `yaml:"tls_key_file"`
	Auth          AuthConfig `yaml:"auth"`
}

type AuthConfig struct {
	APITokensEnabled  bool       `yaml:"api_tokens_enabled"`
	BootstrapTokenEnv string     `yaml:"bootstrap_token_env"`
	OIDCEnabled       bool       `yaml:"oidc_enabled,omitempty"`
	OIDC              OIDCConfig `yaml:"oidc"`
}

type OIDCConfig struct {
	Enabled              bool          `yaml:"enabled"`
	IssuerURL            string        `yaml:"issuer_url"`
	ClientID             string        `yaml:"client_id"`
	ClientSecretEnv      string        `yaml:"client_secret_env"`
	RedirectURL          string        `yaml:"redirect_url"`
	Scopes               []string      `yaml:"scopes"`
	SessionTTL           time.Duration `yaml:"session_ttl"`
	CookieName           string        `yaml:"cookie_name"`
	RequireEmailVerified bool          `yaml:"require_email_verified"`
}

func (a AuthConfig) OIDCActive() bool {
	return a.OIDC.Enabled || a.OIDCEnabled
}

type SchedulerConfig struct {
	DefaultInterval            time.Duration  `yaml:"default_interval"`
	Cron                       string         `yaml:"cron"`
	Timezone                   string         `yaml:"timezone"`
	JitterPercent              int            `yaml:"jitter_percent"`
	JitterMax                  time.Duration  `yaml:"jitter_max"`
	MaxGlobalConcurrency       int            `yaml:"max_global_concurrency"`
	MaxNewConnectionsPerSecond float64        `yaml:"max_new_connections_per_second"`
	MaxPerSiteConcurrency      int            `yaml:"max_per_site_concurrency"`
	MaxPerVendorConcurrency    int            `yaml:"max_per_vendor_concurrency"`
	MaxPerGroupConcurrency     int            `yaml:"max_per_group_concurrency"`
	QueueSize                  int            `yaml:"queue_size"`
	Retry                      RetryConfig    `yaml:"retry"`
	Lease                      LeaseConfig    `yaml:"lease"`
	Windows                    []WindowConfig `yaml:"windows"`
	Blackouts                  []WindowConfig `yaml:"blackouts"`
}

type RetryConfig struct {
	MaxAttempts    int           `yaml:"max_attempts"`
	BackoffInitial time.Duration `yaml:"backoff_initial"`
	BackoffMax     time.Duration `yaml:"backoff_max"`
}

type LeaseConfig struct {
	Enabled       bool          `yaml:"enabled"`
	WorkerID      string        `yaml:"worker_id"`
	TTL           time.Duration `yaml:"ttl"`
	RenewInterval time.Duration `yaml:"renew_interval"`
}

type WindowConfig struct {
	Name     string   `yaml:"name"`
	Days     []string `yaml:"days"`
	Start    string   `yaml:"start"`
	End      string   `yaml:"end"`
	Timezone string   `yaml:"timezone"`
	Groups   []string `yaml:"groups"`
	Sites    []string `yaml:"sites"`
	Vendors  []string `yaml:"vendors"`
	Roles    []string `yaml:"roles"`
}

type InventoryConfig struct {
	RefreshInterval time.Duration     `yaml:"refresh_interval"`
	Sources         []InventorySource `yaml:"sources"`
}

type InventorySource struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type CredentialsConfig struct {
	DefaultProvider string                    `yaml:"default_provider"`
	Dotenv          DotenvConfig              `yaml:"dotenv"`
	EncryptedFile   EncryptedFileConfig       `yaml:"encrypted_file"`
	Providers       map[string]ProviderConfig `yaml:"providers"`
}

type DotenvConfig struct {
	FilePath        string `yaml:"file_path"`
	RequireChmod600 bool   `yaml:"require_chmod_600"`
}

type EncryptedFileConfig struct {
	Path      string `yaml:"path"`
	KeyEnv    string `yaml:"kms_key_env"`
	NonceSize int    `yaml:"nonce_size"`
}

type ProviderConfig struct {
	Type string `yaml:"type"`
}

type StorageConfig struct {
	Metadata MetadataStorageConfig `yaml:"metadata"`
	Config   ConfigStorageConfig   `yaml:"config"`
}

type MetadataStorageConfig struct {
	Type   string `yaml:"type"`
	DSNEnv string `yaml:"dsn_env"`
	DSN    string `yaml:"dsn"`
}

type ConfigStorageConfig struct {
	Type          string                  `yaml:"type"`
	ShardStrategy goxidized.ShardStrategy `yaml:"shard_strategy"`
	ShardCount    int                     `yaml:"shard_count"`
	BasePath      string                  `yaml:"base_path"`
	AuthorName    string                  `yaml:"author_name"`
	AuthorEmail   string                  `yaml:"author_email"`
}

type RedactionConfig struct {
	Enabled           bool   `yaml:"enabled"`
	StrictMode        bool   `yaml:"strict_mode"`
	RawStorageEnabled bool   `yaml:"raw_storage_enabled"`
	HMACKeyEnv        string `yaml:"hmac_key_env"`
}

type TransportConfig struct {
	SSH    SSHConfig    `yaml:"ssh"`
	Telnet TelnetConfig `yaml:"telnet"`
}

type SSHConfig struct {
	ConnectTimeout   time.Duration `yaml:"connect_timeout"`
	AuthTimeout      time.Duration `yaml:"auth_timeout"`
	CommandTimeout   time.Duration `yaml:"command_timeout"`
	IdleTimeout      time.Duration `yaml:"idle_timeout"`
	SessionDeadline  time.Duration `yaml:"session_deadline"`
	InteractiveShell bool          `yaml:"interactive_shell"`
	PromptPattern    string        `yaml:"prompt_pattern"`
	MaxOutputBytes   int64         `yaml:"max_output_bytes"`
	HostKeyMode      string        `yaml:"host_key_mode"`
	KnownHostsPath   string        `yaml:"known_hosts_path"`
	TOFUPath         string        `yaml:"tofu_path"`
}

type TelnetConfig struct {
	Enabled        bool          `yaml:"enabled"`
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	LoginTimeout   time.Duration `yaml:"login_timeout"`
	CommandTimeout time.Duration `yaml:"command_timeout"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	PromptPattern  string        `yaml:"prompt_pattern"`
	MaxOutputBytes int64         `yaml:"max_output_bytes"`
}

type RetentionConfig struct {
	Days int `yaml:"days"`
}

type ObservabilityConfig struct {
	MetricsEnabled bool   `yaml:"metrics_enabled"`
	TracingEnabled bool   `yaml:"tracing_enabled"`
	LogFormat      string `yaml:"log_format"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{
			ListenAddress: "127.0.0.1:8080",
			Auth: AuthConfig{
				APITokensEnabled:  true,
				BootstrapTokenEnv: "GOXIDIZED_BOOTSTRAP_TOKEN",
				OIDC: OIDCConfig{
					ClientSecretEnv:      "GOXIDIZED_OIDC_CLIENT_SECRET",
					Scopes:               []string{"openid", "profile", "email"},
					SessionTTL:           8 * time.Hour,
					CookieName:           "goxidized_session",
					RequireEmailVerified: true,
				},
			},
		},
		Scheduler: SchedulerConfig{
			DefaultInterval:            24 * time.Hour,
			Timezone:                   "UTC",
			JitterPercent:              20,
			MaxGlobalConcurrency:       250,
			MaxNewConnectionsPerSecond: 10,
			MaxPerSiteConcurrency:      30,
			MaxPerVendorConcurrency:    100,
			MaxPerGroupConcurrency:     100,
			QueueSize:                  10000,
			Retry:                      RetryConfig{MaxAttempts: 3, BackoffInitial: 30 * time.Second, BackoffMax: 30 * time.Minute},
			Lease:                      LeaseConfig{Enabled: true, TTL: 15 * time.Minute, RenewInterval: 5 * time.Minute},
		},
		Inventory: InventoryConfig{
			RefreshInterval: 5 * time.Minute,
			Sources:         []InventorySource{{Name: "primary-csv", Type: "csv", Path: "devices.csv"}},
		},
		Credentials: CredentialsConfig{
			DefaultProvider: "dotenv",
			Dotenv:          DotenvConfig{FilePath: ".env", RequireChmod600: true},
			EncryptedFile:   EncryptedFileConfig{Path: "secrets.enc", KeyEnv: "GOXIDIZED_KMS_KEY", NonceSize: 12},
		},
		Storage: StorageConfig{
			Metadata: MetadataStorageConfig{Type: "postgres", DSNEnv: "GOXIDIZED_POSTGRES_DSN"},
			Config: ConfigStorageConfig{
				Type: "git", ShardStrategy: goxidized.ShardByRole, BasePath: "data/repos",
				AuthorName: "GoXidized", AuthorEmail: "goxidized@example.invalid",
			},
		},
		Redaction: RedactionConfig{Enabled: true, StrictMode: true, HMACKeyEnv: "GOXIDIZED_REDACTION_HMAC_KEY"},
		Transport: TransportConfig{
			SSH: SSHConfig{
				ConnectTimeout: 20 * time.Second, AuthTimeout: 20 * time.Second, CommandTimeout: 60 * time.Second,
				IdleTimeout: 30 * time.Second, SessionDeadline: 3 * time.Minute, InteractiveShell: true,
				PromptPattern: `(?m)(^|\n)[^\n]{1,96}[>#]\s*$`, MaxOutputBytes: 16 << 20, HostKeyMode: "strict",
				KnownHostsPath: "known_hosts", TOFUPath: "known_hosts.tofu",
			},
			Telnet: TelnetConfig{
				Enabled: false, ConnectTimeout: 20 * time.Second, LoginTimeout: 20 * time.Second,
				CommandTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second,
				PromptPattern: `(?m)(^|\n)[^\n]{1,96}[>#]\s*$`, MaxOutputBytes: 16 << 20,
			},
		},
		Retention:     RetentionConfig{Days: 365},
		Observability: ObservabilityConfig{MetricsEnabled: true, LogFormat: "json"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, cfg.Validate()
}

func (c Config) MetadataDSN() string {
	if c.Storage.Metadata.DSN != "" {
		return c.Storage.Metadata.DSN
	}
	if c.Storage.Metadata.DSNEnv == "" {
		return ""
	}
	return os.Getenv(c.Storage.Metadata.DSNEnv)
}

func (c Config) Validate() error {
	if c.Server.ListenAddress == "" {
		return errors.New("server.listen_address is required")
	}
	if c.Server.TLSEnabled && (c.Server.TLSCertFile == "" || c.Server.TLSKeyFile == "") {
		return errors.New("server.tls_cert_file and server.tls_key_file are required when tls_enabled is true")
	}
	if c.Server.Auth.OIDCActive() {
		oidc := c.Server.Auth.OIDC
		if oidc.IssuerURL == "" {
			return errors.New("server.auth.oidc.issuer_url is required when OIDC is enabled")
		}
		if oidc.ClientID == "" {
			return errors.New("server.auth.oidc.client_id is required when OIDC is enabled")
		}
		if oidc.ClientSecretEnv == "" {
			return errors.New("server.auth.oidc.client_secret_env is required when OIDC is enabled")
		}
		if oidc.RedirectURL == "" {
			return errors.New("server.auth.oidc.redirect_url is required when OIDC is enabled")
		}
		if oidc.SessionTTL <= 0 {
			return errors.New("server.auth.oidc.session_ttl must be > 0 when OIDC is enabled")
		}
		if oidc.CookieName == "" || strings.ContainsAny(oidc.CookieName, " \t\r\n;") {
			return errors.New("server.auth.oidc.cookie_name must be a valid cookie name")
		}
		if !contains(oidc.Scopes, "openid") {
			return errors.New("server.auth.oidc.scopes must include openid")
		}
	}
	if c.Scheduler.MaxGlobalConcurrency <= 0 {
		return errors.New("scheduler.max_global_concurrency must be > 0")
	}
	if c.Scheduler.MaxNewConnectionsPerSecond <= 0 {
		return errors.New("scheduler.max_new_connections_per_second must be > 0")
	}
	if c.Scheduler.JitterPercent < 0 || c.Scheduler.JitterPercent > 100 {
		return errors.New("scheduler.jitter_percent must be between 0 and 100")
	}
	if c.Scheduler.Cron == "" && c.Scheduler.DefaultInterval <= 0 {
		return errors.New("scheduler.default_interval must be > 0 when scheduler.cron is empty")
	}
	if c.Scheduler.Lease.Enabled && c.Scheduler.Lease.TTL <= 0 {
		return errors.New("scheduler.lease.ttl must be > 0 when leases are enabled")
	}
	if c.Scheduler.Lease.Enabled && c.Scheduler.Lease.RenewInterval <= 0 {
		return errors.New("scheduler.lease.renew_interval must be > 0 when leases are enabled")
	}
	for _, window := range append(append([]WindowConfig{}, c.Scheduler.Windows...), c.Scheduler.Blackouts...) {
		if window.Start == "" || window.End == "" {
			return fmt.Errorf("scheduler window %q must include start and end", window.Name)
		}
	}
	if len(c.Inventory.Sources) == 0 {
		return errors.New("at least one inventory source is required")
	}
	for _, src := range c.Inventory.Sources {
		if src.Type == "" || src.Path == "" {
			return fmt.Errorf("inventory source %q must include type and path", src.Name)
		}
	}
	if c.Storage.Config.Type == "" || c.Storage.Config.BasePath == "" {
		return errors.New("storage.config.type and storage.config.base_path are required")
	}
	if c.Storage.Config.ShardStrategy == goxidized.ShardByHash && c.Storage.Config.ShardCount <= 0 {
		return errors.New("storage.config.shard_count must be > 0 for hash sharding")
	}
	if !c.Redaction.Enabled && c.Redaction.StrictMode {
		return errors.New("redaction.strict_mode requires redaction.enabled")
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
