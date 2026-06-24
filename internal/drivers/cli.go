package drivers

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"goxidized/internal/pipeline"
	"goxidized/pkg/goxidized"
)

type CLIDriver struct {
	profile   commandProfile
	processor pipeline.Processor
}

func NewCLI(vendor string, prepare []string, fetchCommand string) *CLIDriver {
	commands := make([]profileCommand, 0, len(prepare))
	for _, cmd := range prepare {
		commands = append(commands, profileCommand{Command: cmd})
	}
	return NewCLIProfile(commandProfile{
		Vendor:       vendor,
		Prepare:      commands,
		FetchCommand: fetchCommand,
		MaxBytes:     defaultMaxConfigBytes,
	})
}

func NewCLIProfile(profile commandProfile) *CLIDriver {
	processor := pipeline.NewProcessor(true, nil)
	processor.NormalizeRules = append(processor.NormalizeRules, profile.NormalizeRules...)
	processor.RedactionRules = append(processor.RedactionRules, profile.RedactionRules...)
	return &CLIDriver{
		profile:   profile.clone(),
		processor: processor,
	}
}

func (d *CLIDriver) Vendor() string {
	return d.profile.Vendor
}

func (d *CLIDriver) Prepare(ctx context.Context, sess goxidized.Session) error {
	for _, cmd := range d.profile.Prepare {
		out, err := sess.Run(ctx, cmd.Command)
		if markerErr := d.classifyOutput(cmd.Command, out); markerErr != nil {
			if cmd.AllowFailure {
				continue
			}
			return markerErr
		}
		if err != nil {
			if cmd.AllowFailure {
				continue
			}
			return d.wrapCommandError("prepare "+d.profile.Vendor, err)
		}
	}
	return nil
}

func (d *CLIDriver) FetchConfig(ctx context.Context, t goxidized.Target, sess goxidized.Session) (*goxidized.ConfigResult, error) {
	start := time.Now()
	out, err := sess.Run(ctx, d.profile.FetchCommand)
	if markerErr := d.classifyOutput(d.profile.FetchCommand, out); markerErr != nil {
		return nil, markerErr
	}
	if err != nil {
		return nil, d.wrapCommandError(d.profile.FetchCommand, err)
	}
	if d.profile.MaxBytes > 0 && len(out) > d.profile.MaxBytes {
		return nil, &goxidized.BackupError{
			Category: goxidized.FailureCommand,
			Op:       d.profile.FetchCommand,
			Err:      fmt.Errorf("config output exceeded profile limit of %d bytes", d.profile.MaxBytes),
		}
	}
	return &goxidized.ConfigResult{
		TargetID:   t.ID,
		FetchedAt:  time.Now(),
		RawConfig:  out,
		DurationMS: time.Since(start).Milliseconds(),
		Metadata: map[string]string{
			"vendor":        d.profile.Vendor,
			"fetch_command": d.profile.FetchCommand,
		},
	}, nil
}

func (d *CLIDriver) Normalize(ctx context.Context, r *goxidized.ConfigResult) (*goxidized.ConfigResult, error) {
	out, err := d.processor.Normalize(ctx, d.stripTranscriptArtifacts(r.RawConfig))
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureNormalization, Op: d.profile.Vendor, Err: err}
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

func (d *CLIDriver) classifyOutput(command string, out []byte) error {
	if len(out) == 0 {
		return nil
	}
	text := string(out)
	for _, marker := range d.profile.Markers {
		if match := marker.Pattern.FindString(text); match != "" {
			return &goxidized.BackupError{
				Category: marker.Category,
				Op:       command,
				Err:      fmt.Errorf("%s marker matched: %s", marker.Name, compactMarker(match)),
			}
		}
	}
	return nil
}

func (d *CLIDriver) wrapCommandError(op string, err error) error {
	var backupErr *goxidized.BackupError
	if errors.As(err, &backupErr) {
		return err
	}
	category := goxidized.FailureCommand
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		category = goxidized.FailureTimeout
	}
	return &goxidized.BackupError{Category: category, Op: op, Err: err}
}

func (d *CLIDriver) stripTranscriptArtifacts(in []byte) []byte {
	text := strings.ReplaceAll(string(in), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = ansiSequence.ReplaceAllString(text, "")
	for _, pattern := range d.profile.PagingPatterns {
		text = pattern.ReplaceAllString(text, "")
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(strings.ReplaceAll(line, "\b", ""), " \t")
		if d.isPromptOrCommandEcho(line) {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n"))
}

func (d *CLIDriver) isPromptOrCommandEcho(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if sameCommand(trimmed, d.profile.FetchCommand) || d.isPrepareCommand(trimmed) {
		return true
	}
	for _, prompt := range d.profile.PromptPatterns {
		loc := prompt.FindStringIndex(trimmed)
		if loc == nil || loc[0] != 0 {
			continue
		}
		rest := strings.TrimSpace(trimmed[loc[1]:])
		if rest == "" {
			return true
		}
		if sameCommand(rest, d.profile.FetchCommand) || d.isPrepareCommand(rest) {
			return true
		}
	}
	return false
}

func (d *CLIDriver) isPrepareCommand(line string) bool {
	for _, cmd := range d.profile.Prepare {
		if sameCommand(line, cmd.Command) {
			return true
		}
	}
	return false
}

func sameCommand(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func compactMarker(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	compact := strings.Join(fields, " ")
	if len(compact) > 96 {
		return compact[:96] + "..."
	}
	return compact
}

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
