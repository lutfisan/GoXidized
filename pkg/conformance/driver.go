package conformance

import (
	"context"
	"errors"
	"fmt"

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
	Target          goxidized.Target
	Responses       map[string][]byte
	ExpectedCommand string
}

func RunDriverFixture(ctx context.Context, driver goxidized.Driver, fixture DriverFixture) (*goxidized.RedactedConfig, goxidized.RedactionReport, error) {
	if fixture.Target.ID == "" {
		return nil, goxidized.RedactionReport{}, errors.New("fixture target id is required")
	}
	sess := &ReplaySession{Responses: fixture.Responses, Errors: map[string]error{}}
	if err := driver.Prepare(ctx, sess); err != nil {
		return nil, goxidized.RedactionReport{}, err
	}
	cfg, err := driver.FetchConfig(ctx, fixture.Target, sess)
	if err != nil {
		return nil, goxidized.RedactionReport{}, err
	}
	norm, err := driver.Normalize(ctx, cfg)
	if err != nil {
		return nil, goxidized.RedactionReport{}, err
	}
	return driver.Redact(ctx, norm)
}
