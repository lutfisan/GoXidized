package sshtransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"goxidized/pkg/goxidized"
)

type Config struct {
	ConnectTimeout  time.Duration
	AuthTimeout     time.Duration
	CommandTimeout  time.Duration
	IdleTimeout     time.Duration
	SessionDeadline time.Duration
	HostKeyMode     string
	KnownHostsPath  string
	TOFUPath        string
	InsecureWarning func(string)
}

type Dialer struct {
	Config Config
	mu     sync.Mutex
}

func New(cfg Config) *Dialer {
	return &Dialer{Config: cfg}
}

func (d *Dialer) Dial(ctx context.Context, t goxidized.Target, creds goxidized.Credentials) (goxidized.Session, error) {
	if t.JumpHost != "" {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "ssh jump host", Err: errors.New("jump-host proxying is configured but not implemented in this adapter yet")}
	}
	auth, err := authMethods(creds)
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureAuth, Op: "ssh auth", Err: err}
	}
	hostKey, err := d.hostKeyCallback()
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "ssh host key", Err: err}
	}
	cfg := &ssh.ClientConfig{
		User:            creds.Username,
		Auth:            auth,
		HostKeyCallback: hostKey,
		Timeout:         d.Config.ConnectTimeout,
	}
	addr := net.JoinHostPort(t.IPAddress, portString(t.Port))
	var dialer net.Dialer
	if d.Config.ConnectTimeout > 0 {
		dialer.Timeout = d.Config.ConnectTimeout
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "tcp dial", Err: err}
	}
	if d.Config.AuthTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(d.Config.AuthTimeout))
	}
	cconn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		if strings.Contains(strings.ToLower(err.Error()), "unable to authenticate") {
			return nil, &goxidized.BackupError{Category: goxidized.FailureAuth, Op: "ssh handshake", Err: err}
		}
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "ssh handshake", Err: err}
	}
	sessionDeadline := time.Time{}
	if d.Config.SessionDeadline > 0 {
		sessionDeadline = time.Now().Add(d.Config.SessionDeadline)
		_ = conn.SetDeadline(sessionDeadline)
	} else {
		_ = conn.SetDeadline(time.Time{})
	}
	commandTimeout := d.Config.CommandTimeout
	if commandTimeout <= 0 {
		commandTimeout = d.Config.IdleTimeout
	}
	return &Session{client: ssh.NewClient(cconn, chans, reqs), conn: conn, commandTimeout: commandTimeout, sessionDeadline: sessionDeadline}, nil
}

func authMethods(creds goxidized.Credentials) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if !creds.PrivateKeyPEM.IsZero() {
		signer, err := ssh.ParsePrivateKey(creds.PrivateKeyPEM.Reveal())
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if !creds.Password.IsZero() {
		methods = append(methods, ssh.Password(creds.Password.Reveal()))
	}
	if len(methods) == 0 {
		return nil, errors.New("no SSH auth method available")
	}
	return methods, nil
}

func (d *Dialer) hostKeyCallback() (ssh.HostKeyCallback, error) {
	switch strings.ToLower(d.Config.HostKeyMode) {
	case "", "strict":
		if d.Config.KnownHostsPath == "" {
			return nil, errors.New("known_hosts path is required in strict mode")
		}
		return knownhosts.New(d.Config.KnownHostsPath)
	case "tofu":
		path := d.Config.TOFUPath
		if path == "" {
			path = d.Config.KnownHostsPath
		}
		if path == "" {
			return nil, errors.New("tofu path is required in tofu mode")
		}
		return d.tofuCallback(path), nil
	case "insecure":
		if d.Config.InsecureWarning != nil {
			d.Config.InsecureWarning("ssh host key verification is disabled")
		}
		return ssh.InsecureIgnoreHostKey(), nil
	default:
		return nil, fmt.Errorf("unknown host key mode %q", d.Config.HostKeyMode)
	}
}

func (d *Dialer) tofuCallback(path string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		cb, err := knownhosts.New(path)
		if err == nil {
			err = cb(hostname, remote, key)
			if err == nil {
				return nil
			}
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) || len(keyErr.Want) > 0 {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		line := knownhosts.Line([]string{hostname}, key)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(line + "\n")
		return err
	}
}

type Session struct {
	client          *ssh.Client
	conn            net.Conn
	commandTimeout  time.Duration
	sessionDeadline time.Time
}

func (s *Session) Run(ctx context.Context, command string) ([]byte, error) {
	if s.commandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.commandTimeout)
		defer cancel()
	}
	if s.conn != nil {
		_ = s.conn.SetDeadline(s.deadline())
	}
	sess, err := s.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := sess.CombinedOutput(command)
		ch <- result{out: out, err: err}
	}()
	select {
	case <-ctx.Done():
		_ = sess.Close()
		return nil, &goxidized.BackupError{Category: goxidized.FailureTimeout, Op: command, Err: ctx.Err()}
	case res := <-ch:
		if s.conn != nil && !s.sessionDeadline.IsZero() {
			_ = s.conn.SetDeadline(s.sessionDeadline)
		}
		if res.err != nil {
			return res.out, res.err
		}
		return res.out, nil
	}
}

func (s *Session) Close() error {
	err := s.client.Close()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return err
}

func (s *Session) deadline() time.Time {
	if s.sessionDeadline.IsZero() {
		if s.commandTimeout > 0 {
			return time.Now().Add(s.commandTimeout)
		}
		return time.Time{}
	}
	if s.commandTimeout <= 0 {
		return s.sessionDeadline
	}
	commandDeadline := time.Now().Add(s.commandTimeout)
	if commandDeadline.Before(s.sessionDeadline) {
		return commandDeadline
	}
	return s.sessionDeadline
}

func portString(port int) string {
	if port == 0 {
		port = 22
	}
	return fmt.Sprint(port)
}
