package drivers

import (
	"context"
	"time"

	"goxidized/internal/pipeline"
	"goxidized/pkg/goxidized"
)

type CLIDriver struct {
	vendor       string
	prepare      []string
	fetchCommand string
	processor    pipeline.Processor
}

func NewCLI(vendor string, prepare []string, fetchCommand string) *CLIDriver {
	return &CLIDriver{
		vendor: vendor, prepare: append([]string(nil), prepare...), fetchCommand: fetchCommand,
		processor: pipeline.NewProcessor(true, nil),
	}
}

func (d *CLIDriver) Vendor() string {
	return d.vendor
}

func (d *CLIDriver) Prepare(ctx context.Context, sess goxidized.Session) error {
	for _, cmd := range d.prepare {
		if _, err := sess.Run(ctx, cmd); err != nil {
			return &goxidized.BackupError{Category: goxidized.FailurePrivilege, Op: "prepare " + d.vendor, Err: err}
		}
	}
	return nil
}

func (d *CLIDriver) FetchConfig(ctx context.Context, t goxidized.Target, sess goxidized.Session) (*goxidized.ConfigResult, error) {
	start := time.Now()
	out, err := sess.Run(ctx, d.fetchCommand)
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureCommand, Op: d.fetchCommand, Err: err}
	}
	return &goxidized.ConfigResult{
		TargetID:   t.ID,
		FetchedAt:  time.Now(),
		RawConfig:  out,
		DurationMS: time.Since(start).Milliseconds(),
		Metadata: map[string]string{
			"vendor":        d.vendor,
			"fetch_command": d.fetchCommand,
		},
	}, nil
}

func (d *CLIDriver) Normalize(ctx context.Context, r *goxidized.ConfigResult) (*goxidized.ConfigResult, error) {
	out, err := d.processor.Normalize(ctx, r.RawConfig)
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureNormalization, Op: d.vendor, Err: err}
	}
	cp := *r
	cp.RawConfig = out
	return &cp, nil
}

func (d *CLIDriver) Redact(ctx context.Context, r *goxidized.ConfigResult) (*goxidized.RedactedConfig, goxidized.RedactionReport, error) {
	out, report, err := d.processor.Redact(ctx, r.RawConfig)
	if err != nil {
		return nil, report, err
	}
	return &goxidized.RedactedConfig{TargetID: r.TargetID, Content: out}, report, nil
}
