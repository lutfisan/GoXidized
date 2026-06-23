package telnettransport

import (
	"context"
	"errors"

	"goxidized/pkg/goxidized"
)

type Dialer struct {
	Enabled bool
}

func New(enabled bool) *Dialer {
	return &Dialer{Enabled: enabled}
}

func (d *Dialer) Dial(ctx context.Context, t goxidized.Target, _ goxidized.Credentials) (goxidized.Session, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !d.Enabled || !t.TelnetEnabled {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "telnet gate", Err: errors.New("telnet requires both global and target opt-in")}
	}
	return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "telnet dial", Err: errors.New("telnet transport is gated for v1 and has no automatic SSH fallback")}
}
