package conformance

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"goxidized/pkg/goxidized"
)

type ReplaySession struct {
	Responses map[string][]byte
	Errors    map[string]error
	Commands  []string
}

func (s *ReplaySession) Run(ctx context.Context, command string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.Commands = append(s.Commands, command)
	if err := s.Errors[command]; err != nil {
		return nil, err
	}
	out, ok := s.Responses[command]
	if !ok {
		return nil, fmt.Errorf("no replay response for command %q", command)
	}
	return append([]byte(nil), out...), nil
}

func (s *ReplaySession) Close() error {
	return nil
}

type DriverFixture struct {
	Target           goxidized.Target
	Responses        map[string][]byte
	Errors           map[string]error
	ExpectedCommand  string
	ExpectedCommands []string
}

func RunDriverFixture(ctx context.Context, driver goxidized.Driver, fixture DriverFixture) (*goxidized.RedactedConfig, goxidized.RedactionReport, error) {
	run, err := RunDriverFixtureDetailed(ctx, driver, fixture)
	if err != nil {
		return nil, goxidized.RedactionReport{}, err
	}
	return run.Redacted, run.Report, nil
}

type DriverRun struct {
	Config     *goxidized.ConfigResult
	Normalized *goxidized.ConfigResult
	Redacted   *goxidized.RedactedConfig
	Report     goxidized.RedactionReport
	Commands   []string
}

func RunDriverFixtureDetailed(ctx context.Context, driver goxidized.Driver, fixture DriverFixture) (DriverRun, error) {
	if fixture.Target.ID == "" {
		return DriverRun{}, errors.New("fixture target id is required")
	}
	errorsByCommand := fixture.Errors
	if errorsByCommand == nil {
		errorsByCommand = map[string]error{}
	}
	sess := &ReplaySession{Responses: fixture.Responses, Errors: errorsByCommand}
	if err := driver.Prepare(ctx, sess); err != nil {
		return DriverRun{Commands: append([]string(nil), sess.Commands...)}, err
	}
	cfg, err := driver.FetchConfig(ctx, fixture.Target, sess)
	if err != nil {
		return DriverRun{Commands: append([]string(nil), sess.Commands...)}, err
	}
	norm, err := driver.Normalize(ctx, cfg)
	if err != nil {
		return DriverRun{Config: cfg, Commands: append([]string(nil), sess.Commands...)}, err
	}
	redacted, report, err := driver.Redact(ctx, norm)
	commands := append([]string(nil), sess.Commands...)
	if err != nil {
		return DriverRun{Config: cfg, Normalized: norm, Commands: commands, Report: report}, err
	}
	if fixture.ExpectedCommand != "" && len(commands) == 0 {
		return DriverRun{Config: cfg, Normalized: norm, Redacted: redacted, Report: report, Commands: commands}, fmt.Errorf("last command = <none>, want %q", fixture.ExpectedCommand)
	}
	if fixture.ExpectedCommand != "" && commands[len(commands)-1] != fixture.ExpectedCommand {
		return DriverRun{Config: cfg, Normalized: norm, Redacted: redacted, Report: report, Commands: commands}, fmt.Errorf("last command = %q, want %q", commands[len(commands)-1], fixture.ExpectedCommand)
	}
	if len(fixture.ExpectedCommands) > 0 && !reflect.DeepEqual(commands, fixture.ExpectedCommands) {
		return DriverRun{Config: cfg, Normalized: norm, Redacted: redacted, Report: report, Commands: commands}, fmt.Errorf("commands = %q, want %q", commands, fixture.ExpectedCommands)
	}
	return DriverRun{Config: cfg, Normalized: norm, Redacted: redacted, Report: report, Commands: commands}, nil
}
