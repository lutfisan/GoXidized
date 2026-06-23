package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"goxidized/internal/app"
	"goxidized/internal/config"
	"goxidized/internal/drivers"
	invcsv "goxidized/internal/inventory/csv"
	"goxidized/internal/storage/gitstore"
	"goxidized/pkg/conformance"
	"goxidized/pkg/goxidized"
)

type options struct {
	configPath string
}

func NewRootCommand() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:   "goxidized",
		Short: "Go-native network configuration backup and change governance",
	}
	root.PersistentFlags().StringVar(&opts.configPath, "config", "", "path to config YAML")
	root.AddCommand(serverCmd(opts), inventoryCmd(opts), backupCmd(opts), deviceCmd(opts), configCmd(opts), diffCmd(opts), driverCmd(), storageCmd(opts), adminCmd(), versionCmd())
	return root
}

func Execute(ctx context.Context) error {
	return NewRootCommand().ExecuteContext(ctx)
}

func serverCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "server", Short: "Run server commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the GoXidized API and scheduler",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.configPath)
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
			a, err := app.New(cmd.Context(), cfg, logger)
			if err != nil {
				return err
			}
			defer a.Close()
			return a.Start(cmd.Context())
		},
	})
	return cmd
}

func inventoryCmd(opts *options) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "inventory", Short: "Inventory commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate CSV/router.db inventory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				cfg, err := config.Load(opts.configPath)
				if err != nil {
					return err
				}
				file = cfg.Inventory.Sources[0].Path
			}
			targets, err := invcsv.New(file).Load(cmd.Context())
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"valid_targets": len(targets)})
			return err
		},
	})
	cmd.Commands()[0].Flags().StringVar(&file, "file", "", "inventory file")
	cmd.AddCommand(&cobra.Command{
		Use:   "reload",
		Short: "Reload inventory into metadata store",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), opts, func(a *app.App) error {
				return a.ReloadInventory(cmd.Context())
			})
		},
	})
	return cmd
}

func backupCmd(opts *options) *cobra.Command {
	var device, group string
	cmd := &cobra.Command{Use: "backup", Short: "Backup commands"}
	run := &cobra.Command{
		Use:   "run",
		Short: "Run an immediate backup for a device or group",
		RunE: func(cmd *cobra.Command, args []string) error {
			if device == "" && group == "" {
				return fmt.Errorf("--device or --group is required")
			}
			return withApp(cmd.Context(), opts, func(a *app.App) error {
				if err := a.ReloadInventory(cmd.Context()); err != nil {
					return err
				}
				var results []goxidized.JobResult
				if device != "" {
					t, err := a.Metadata.GetDevice(cmd.Context(), device)
					if err != nil {
						return err
					}
					results = append(results, a.RunBackup(cmd.Context(), t, "cli", "cli"))
				}
				if group != "" {
					devices, err := a.Metadata.ListDevices(cmd.Context())
					if err != nil {
						return err
					}
					for _, t := range devices {
						if t.Group == group && t.Enabled {
							results = append(results, a.RunBackup(cmd.Context(), t, "cli", "cli"))
						}
					}
				}
				return writePretty(cmd, results)
			})
		},
	}
	run.Flags().StringVar(&device, "device", "", "device id")
	run.Flags().StringVar(&group, "group", "", "group name")
	cmd.AddCommand(run)
	return cmd
}

func deviceCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "device", Short: "Device commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "status DEVICE_ID",
		Short: "Show device status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), opts, func(a *app.App) error {
				t, err := a.Metadata.GetDevice(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return writePretty(cmd, t)
			})
		},
	})
	return cmd
}

func configCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Configuration history commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "show DEVICE_ID",
		Short: "Show latest sanitized config",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), opts, func(a *app.App) error {
				cfg, rev, err := a.Storage.Latest(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if cmd.Flags().Changed("latest") {
					_, _ = fmt.Fprint(cmd.OutOrStdout(), string(cfg.Content))
					return nil
				}
				return writePretty(cmd, map[string]any{"revision": rev, "content": string(cfg.Content)})
			})
		},
	})
	cmd.Commands()[0].Flags().Bool("latest", true, "show latest config")
	return cmd
}

func diffCmd(opts *options) *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "diff DEVICE_ID",
		Short: "Show unified diff between revisions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" || to == "" {
				return fmt.Errorf("--from and --to are required")
			}
			return withApp(cmd.Context(), opts, func(a *app.App) error {
				diff, err := a.Storage.Diff(cmd.Context(), args[0], from, to)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprint(cmd.OutOrStdout(), diff)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "from revision")
	cmd.Flags().StringVar(&to, "to", "", "to revision")
	return cmd
}

func driverCmd() *cobra.Command {
	var vendor, fixture string
	cmd := &cobra.Command{Use: "driver", Short: "Driver commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List registered drivers",
		RunE: func(cmd *cobra.Command, args []string) error {
			drivers.RegisterDefaults()
			return writePretty(cmd, map[string]any{"drivers": drivers.List()})
		},
	})
	test := &cobra.Command{
		Use:   "test",
		Short: "Replay a fixture through a driver",
		RunE: func(cmd *cobra.Command, args []string) error {
			if vendor == "" || fixture == "" {
				return fmt.Errorf("--vendor and --fixture are required")
			}
			drivers.RegisterDefaults()
			driver, err := drivers.Get(vendor)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(fixture)
			if err != nil {
				return err
			}
			cfg, report, err := conformance.RunDriverFixture(cmd.Context(), driver, conformance.DriverFixture{
				Target:    goxidized.Target{ID: "fixture", Hostname: "fixture", IPAddress: "127.0.0.1", Vendor: vendor, Group: "fixture", CredentialRef: "dotenv://fixture", Enabled: true},
				Responses: fixtureResponses(vendor, data),
			})
			if err != nil {
				return err
			}
			return writePretty(cmd, map[string]any{"config": string(cfg.Content), "redaction_report": report})
		},
	}
	test.Flags().StringVar(&vendor, "vendor", "", "driver key")
	test.Flags().StringVar(&fixture, "fixture", "", "raw transcript fixture")
	cmd.AddCommand(test)
	return cmd
}

func storageCmd(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "storage", Short: "Storage commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Verify configured Git storage path is writable",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(opts.configPath)
			if err != nil {
				return err
			}
			store := gitstore.New(cfg.Storage.Config.BasePath, cfg.Storage.Config.ShardStrategy, cfg.Storage.Config.ShardCount, cfg.Storage.Config.AuthorName, cfg.Storage.Config.AuthorEmail)
			_, err = store.Save(cmd.Context(), goxidized.Target{ID: "_storage_verify", Hostname: "_storage_verify", IPAddress: "127.0.0.1", Vendor: "verify", Group: "verify", Role: "verify", CredentialRef: "none", Enabled: true}, goxidized.RedactedConfig{TargetID: "_storage_verify", Content: []byte("storage verify\n")}, goxidized.CommitMeta{Trigger: "cli", Actor: "cli"})
			if err != nil {
				return err
			}
			return writePretty(cmd, map[string]string{"status": "ok"})
		},
	})
	return cmd
}

func adminCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "admin", Short: "Admin commands"}
	cmd.AddCommand(&cobra.Command{
		Use:   "create-token",
		Short: "Create a random bootstrap token for api_tokens insertion",
		RunE: func(cmd *cobra.Command, args []string) error {
			var b [32]byte
			if _, err := rand.Read(b[:]); err != nil {
				return err
			}
			token := hex.EncodeToString(b[:])
			sum := sha256.Sum256([]byte(token))
			return writePretty(cmd, map[string]string{"token": token, "token_sha256": hex.EncodeToString(sum[:]), "note": "insert token_sha256 into api_tokens.token_hash"})
		},
	})
	return cmd
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "goxidized dev")
			return nil
		},
	}
}

func withApp(ctx context.Context, opts *options, fn func(*app.App) error) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	a, err := app.New(ctx, cfg, slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	defer a.Close()
	return fn(a)
}

func writePretty(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func fixtureResponses(vendor string, data []byte) map[string][]byte {
	responses := map[string][]byte{
		"terminal length 0":                []byte(""),
		"screen-length 0 temporary":        []byte(""),
		"show running-config":              data,
		"display current-configuration":    data,
		"show configuration | display set": data,
		"show configuration":               data,
	}
	if strings.Contains(vendor, "huawei") {
		responses["display current-configuration"] = data
	}
	return responses
}
