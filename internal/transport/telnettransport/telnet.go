package telnettransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"goxidized/pkg/goxidized"
)

const (
	iac  = 255
	dont = 254
	do   = 253
	wont = 252
	will = 251

	defaultPromptPattern = `(?m)(^|\n)[^\n]{1,96}[>#]\s*$`
	defaultMaxOutput     = int64(16 << 20)
)

type Config struct {
	Enabled        bool
	ConnectTimeout time.Duration
	LoginTimeout   time.Duration
	CommandTimeout time.Duration
	IdleTimeout    time.Duration
	PromptPattern  string
	MaxOutputBytes int64
}

type Dialer struct {
	Config Config
}

func New(enabled bool) *Dialer {
	return NewConfig(Config{Enabled: enabled})
}

func NewConfig(cfg Config) *Dialer {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 20 * time.Second
	}
	if cfg.LoginTimeout <= 0 {
		cfg.LoginTimeout = 20 * time.Second
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 60 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Second
	}
	if cfg.PromptPattern == "" {
		cfg.PromptPattern = defaultPromptPattern
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutput
	}
	return &Dialer{Config: cfg}
}

func (d *Dialer) Dial(ctx context.Context, t goxidized.Target, creds goxidized.Credentials) (goxidized.Session, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !d.Config.Enabled || !t.TelnetEnabled {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "telnet gate", Err: errors.New("telnet requires both global and target opt-in")}
	}
	addr := net.JoinHostPort(t.IPAddress, portString(t.Port, 23))
	dialer := net.Dialer{Timeout: d.Config.ConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "telnet dial", Err: err}
	}
	prompt, err := regexp.Compile(d.Config.PromptPattern)
	if err != nil {
		_ = conn.Close()
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "telnet prompt pattern", Err: err}
	}
	s := &Session{
		conn: conn, commandTimeout: d.Config.CommandTimeout, idleTimeout: d.Config.IdleTimeout,
		prompt: prompt, maxOutputBytes: d.Config.MaxOutputBytes,
	}
	loginCtx, cancel := context.WithTimeout(ctx, d.Config.LoginTimeout)
	defer cancel()
	if err := s.login(loginCtx, creds); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return s, nil
}

type Session struct {
	conn           net.Conn
	commandTimeout time.Duration
	idleTimeout    time.Duration
	prompt         *regexp.Regexp
	maxOutputBytes int64
}

func (s *Session) Run(ctx context.Context, command string) ([]byte, error) {
	if s.commandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.commandTimeout)
		defer cancel()
	}
	if _, err := fmt.Fprintf(s.conn, "%s\r\n", command); err != nil {
		return nil, err
	}
	out, err := s.readUntilPrompt(ctx, command)
	return out, err
}

func (s *Session) Close() error {
	if s == nil || s.conn == nil {
		return nil
	}
	return s.conn.Close()
}

func (s *Session) login(ctx context.Context, creds goxidized.Credentials) error {
	for {
		out, err := s.readUntilPrompt(ctx, "")
		text := strings.ToLower(string(out))
		switch {
		case strings.Contains(text, "username") || strings.Contains(text, "login"):
			if creds.Username == "" {
				return &goxidized.BackupError{Category: goxidized.FailureAuth, Op: "telnet username", Err: errors.New("username is required")}
			}
			if _, writeErr := fmt.Fprintf(s.conn, "%s\r\n", creds.Username); writeErr != nil {
				return writeErr
			}
		case strings.Contains(text, "password"):
			if creds.Password.IsZero() {
				return &goxidized.BackupError{Category: goxidized.FailureAuth, Op: "telnet password", Err: errors.New("password is required")}
			}
			if _, writeErr := fmt.Fprintf(s.conn, "%s\r\n", creds.Password.Reveal()); writeErr != nil {
				return writeErr
			}
		case s.prompt != nil && s.prompt.Match(out):
			return nil
		default:
			if err != nil {
				return err
			}
			if len(out) > 0 {
				return nil
			}
		}
		if err != nil {
			return err
		}
	}
}

func (s *Session) readUntilPrompt(ctx context.Context, command string) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		if s.maxOutputBytes > 0 && int64(out.Len()) > s.maxOutputBytes {
			return out.Bytes()[:s.maxOutputBytes], &goxidized.BackupError{Category: goxidized.FailureCommand, Op: command, Err: fmt.Errorf("output exceeded %d bytes", s.maxOutputBytes)}
		}
		if out.Len() > 0 && s.prompt != nil && s.prompt.Match(out.Bytes()) {
			return cleanOutput(out.Bytes(), command, s.prompt), nil
		}
		if err := ctx.Err(); err != nil {
			return cleanOutput(out.Bytes(), command, s.prompt), &goxidized.BackupError{Category: goxidized.FailureTimeout, Op: command, Err: err}
		}
		deadline := time.Now().Add(readIdle(s.idleTimeout))
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		_ = s.conn.SetReadDeadline(deadline)
		n, err := s.read(buf)
		if n > 0 {
			out.Write(buf[:n])
			continue
		}
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			if out.Len() > 0 {
				return cleanOutput(out.Bytes(), command, s.prompt), nil
			}
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if out.Len() > 0 {
				return cleanOutput(out.Bytes(), command, s.prompt), nil
			}
			continue
		}
		if out.Len() > 0 {
			return cleanOutput(out.Bytes(), command, s.prompt), nil
		}
		return nil, err
	}
}

func (s *Session) read(buf []byte) (int, error) {
	tmp := make([]byte, len(buf))
	n, err := s.conn.Read(tmp)
	if n <= 0 {
		return n, err
	}
	w := 0
	for i := 0; i < n; i++ {
		b := tmp[i]
		if b != iac {
			buf[w] = b
			w++
			continue
		}
		if i+2 >= n {
			continue
		}
		cmd := tmp[i+1]
		opt := tmp[i+2]
		i += 2
		switch cmd {
		case do, dont:
			_, _ = s.conn.Write([]byte{iac, wont, opt})
		case will, wont:
			_, _ = s.conn.Write([]byte{iac, dont, opt})
		}
	}
	return w, err
}

func cleanOutput(raw []byte, command string, prompt *regexp.Regexp) []byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "--More--", "")
	lines := strings.Split(text, "\n")
	clean := make([]string, 0, len(lines))
	command = strings.TrimSpace(command)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if command != "" && trimmed == command {
			continue
		}
		if prompt != nil && prompt.MatchString("\n"+trimmed) {
			continue
		}
		clean = append(clean, strings.TrimRight(line, " \t"))
	}
	return []byte(strings.TrimSpace(strings.Join(clean, "\n")) + "\n")
}

func readIdle(idle time.Duration) time.Duration {
	if idle <= 0 {
		return 500 * time.Millisecond
	}
	if idle > 2*time.Second {
		return 2 * time.Second
	}
	return idle
}

func portString(port, fallback int) string {
	if port == 0 {
		port = fallback
	}
	return fmt.Sprint(port)
}
