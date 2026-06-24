package sshtransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"goxidized/pkg/goxidized"
)

const (
	defaultPromptPattern = `(?m)(^|\n)[^\n]{1,96}[>#]\s*$`
	defaultMaxOutput     = int64(16 << 20)
)

type Config struct {
	ConnectTimeout   time.Duration
	AuthTimeout      time.Duration
	CommandTimeout   time.Duration
	IdleTimeout      time.Duration
	SessionDeadline  time.Duration
	InteractiveShell bool
	PromptPattern    string
	MaxOutputBytes   int64
	HostKeyMode      string
	KnownHostsPath   string
	TOFUPath         string
	InsecureWarning  func(string)
}

type Dialer struct {
	Config Config
	mu     sync.Mutex
}

func New(cfg Config) *Dialer {
	if cfg.PromptPattern == "" {
		cfg.PromptPattern = defaultPromptPattern
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutput
	}
	return &Dialer{Config: cfg}
}

func (d *Dialer) Dial(ctx context.Context, t goxidized.Target, creds goxidized.Credentials) (goxidized.Session, error) {
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
	targetAddr := net.JoinHostPort(t.IPAddress, portString(t.Port))
	conn, jumpClient, err := d.networkConn(ctx, t, cfg, targetAddr)
	if err != nil {
		return nil, err
	}
	if d.Config.AuthTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(d.Config.AuthTimeout))
	}
	cconn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, cfg)
	if err != nil {
		_ = conn.Close()
		if jumpClient != nil {
			_ = jumpClient.Close()
		}
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
	prompt, err := regexp.Compile(d.Config.PromptPattern)
	if err != nil {
		_ = conn.Close()
		if jumpClient != nil {
			_ = jumpClient.Close()
		}
		return nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "ssh prompt pattern", Err: err}
	}
	return &Session{
		client: ssh.NewClient(cconn, chans, reqs), conn: conn, jumpClient: jumpClient,
		commandTimeout: commandTimeout, idleTimeout: d.Config.IdleTimeout, sessionDeadline: sessionDeadline,
		interactive: d.Config.InteractiveShell, prompt: prompt, maxOutputBytes: d.Config.MaxOutputBytes,
	}, nil
}

func (d *Dialer) networkConn(ctx context.Context, t goxidized.Target, cfg *ssh.ClientConfig, targetAddr string) (net.Conn, *ssh.Client, error) {
	if t.JumpHost == "" {
		conn, err := d.tcpDial(ctx, targetAddr)
		if err != nil {
			return nil, nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "tcp dial", Err: err}
		}
		return conn, nil, nil
	}
	jump, err := parseJumpHost(t.JumpHost, cfg.User)
	if err != nil {
		return nil, nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "ssh jump host", Err: err}
	}
	jumpCfg := cloneClientConfig(cfg)
	jumpCfg.User = jump.username
	jumpAddr := net.JoinHostPort(jump.host, jump.port)
	jumpConn, err := d.tcpDial(ctx, jumpAddr)
	if err != nil {
		return nil, nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "jump tcp dial", Err: err}
	}
	if d.Config.AuthTimeout > 0 {
		_ = jumpConn.SetDeadline(time.Now().Add(d.Config.AuthTimeout))
	}
	cconn, chans, reqs, err := ssh.NewClientConn(jumpConn, jumpAddr, jumpCfg)
	if err != nil {
		_ = jumpConn.Close()
		return nil, nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "jump ssh handshake", Err: err}
	}
	jumpClient := ssh.NewClient(cconn, chans, reqs)
	conn, err := jumpClient.Dial("tcp", targetAddr)
	if err != nil {
		_ = jumpClient.Close()
		return nil, nil, &goxidized.BackupError{Category: goxidized.FailureConnect, Op: "jump target dial", Err: err}
	}
	return conn, jumpClient, nil
}

func (d *Dialer) tcpDial(ctx context.Context, addr string) (net.Conn, error) {
	var dialer net.Dialer
	if d.Config.ConnectTimeout > 0 {
		dialer.Timeout = d.Config.ConnectTimeout
	}
	return dialer.DialContext(ctx, "tcp", addr)
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
	jumpClient      *ssh.Client
	conn            net.Conn
	commandTimeout  time.Duration
	idleTimeout     time.Duration
	sessionDeadline time.Time
	interactive     bool
	prompt          *regexp.Regexp
	maxOutputBytes  int64

	shellMu sync.Mutex
	shell   *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
}

func (s *Session) Run(ctx context.Context, command string) ([]byte, error) {
	if s.interactive {
		return s.runInteractive(ctx, command)
	}
	return s.runExec(ctx, command)
}

func (s *Session) runExec(ctx context.Context, command string) ([]byte, error) {
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
		if int64(len(res.out)) > s.maxOutputBytes {
			return res.out[:s.maxOutputBytes], &goxidized.BackupError{Category: goxidized.FailureCommand, Op: command, Err: fmt.Errorf("output exceeded %d bytes", s.maxOutputBytes)}
		}
		if res.err != nil {
			return res.out, res.err
		}
		return res.out, nil
	}
}

func (s *Session) runInteractive(ctx context.Context, command string) ([]byte, error) {
	s.shellMu.Lock()
	defer s.shellMu.Unlock()
	if s.commandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.commandTimeout)
		defer cancel()
	}
	if err := s.ensureShell(ctx); err != nil {
		return nil, err
	}
	if s.conn != nil {
		_ = s.conn.SetDeadline(s.deadline())
	}
	if _, err := io.WriteString(s.stdin, command+"\n"); err != nil {
		return nil, err
	}
	out, err := s.readUntilPrompt(ctx, command)
	if s.conn != nil && !s.sessionDeadline.IsZero() {
		_ = s.conn.SetDeadline(s.sessionDeadline)
	}
	return out, err
}

func (s *Session) ensureShell(ctx context.Context) error {
	if s.shell != nil {
		return nil
	}
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty("vt100", 80, 200, modes); err != nil {
		_ = sess.Close()
		return err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		return err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		return err
	}
	pr, pw := io.Pipe()
	var writeMu sync.Mutex
	copyToPipe := func(r io.Reader) {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_, _ = pw.Write(buf[:n])
				writeMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	go copyToPipe(stdout)
	go copyToPipe(stderr)
	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = pw.Close()
		return err
	}
	s.shell = sess
	s.stdin = stdin
	s.stdout = pr

	drainCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = s.readUntilPrompt(drainCtx, "")
	return nil
}

func (s *Session) readUntilPrompt(ctx context.Context, command string) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, 4096)
	for {
		if s.maxOutputBytes > 0 && int64(out.Len()) > s.maxOutputBytes {
			return out.Bytes()[:s.maxOutputBytes], &goxidized.BackupError{Category: goxidized.FailureCommand, Op: command, Err: fmt.Errorf("output exceeded %d bytes", s.maxOutputBytes)}
		}
		if out.Len() > 0 && s.prompt != nil && s.prompt.Match(out.Bytes()) {
			return cleanInteractiveOutput(out.Bytes(), command, s.prompt), nil
		}
		if err := ctx.Err(); err != nil {
			return cleanInteractiveOutput(out.Bytes(), command, s.prompt), &goxidized.BackupError{Category: goxidized.FailureTimeout, Op: command, Err: err}
		}
		if s.conn != nil {
			deadline := time.Now().Add(readIdle(s.idleTimeout))
			if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
				deadline = dl
			}
			_ = s.conn.SetDeadline(deadline)
		}
		n, err := s.stdout.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			continue
		}
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			if out.Len() > 0 {
				return cleanInteractiveOutput(out.Bytes(), command, s.prompt), nil
			}
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if out.Len() > 0 {
				return cleanInteractiveOutput(out.Bytes(), command, s.prompt), nil
			}
			continue
		}
		if out.Len() > 0 {
			return cleanInteractiveOutput(out.Bytes(), command, s.prompt), nil
		}
		return nil, err
	}
}

func (s *Session) Close() error {
	var err error
	if s.shell != nil {
		err = s.shell.Close()
	}
	if s.client != nil {
		if closeErr := s.client.Close(); err == nil {
			err = closeErr
		}
	}
	if s.conn != nil {
		if closeErr := s.conn.Close(); err == nil {
			err = closeErr
		}
	}
	if s.jumpClient != nil {
		if closeErr := s.jumpClient.Close(); err == nil {
			err = closeErr
		}
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

type jumpSpec struct {
	username string
	host     string
	port     string
}

func parseJumpHost(raw, defaultUser string) (jumpSpec, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		return jumpSpec{}, errors.New("jump host is empty")
	}
	user := defaultUser
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user = spec[:at]
		spec = spec[at+1:]
	}
	host, port, err := net.SplitHostPort(spec)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			host = strings.Trim(spec, "[]")
			port = "22"
		} else {
			return jumpSpec{}, err
		}
	}
	if user == "" {
		return jumpSpec{}, errors.New("jump host user is empty")
	}
	if host == "" {
		return jumpSpec{}, errors.New("jump host address is empty")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return jumpSpec{}, fmt.Errorf("invalid jump host port %q", port)
	}
	return jumpSpec{username: user, host: host, port: port}, nil
}

func cloneClientConfig(in *ssh.ClientConfig) *ssh.ClientConfig {
	out := *in
	out.Auth = append([]ssh.AuthMethod(nil), in.Auth...)
	return &out
}

func cleanInteractiveOutput(raw []byte, command string, prompt *regexp.Regexp) []byte {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "--More--", "")
	lines := strings.Split(text, "\n")
	clean := make([]string, 0, len(lines))
	command = strings.TrimSpace(command)
	for _, line := range lines {
		trimmed := strings.TrimSpace(stripANSI(line))
		if trimmed == "" {
			continue
		}
		if command != "" && trimmed == command {
			continue
		}
		if prompt != nil && prompt.MatchString("\n"+trimmed) {
			continue
		}
		clean = append(clean, strings.TrimRight(stripANSI(line), " \t"))
	}
	return []byte(strings.TrimSpace(strings.Join(clean, "\n")) + "\n")
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
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

func portString(port int) string {
	if port == 0 {
		port = 22
	}
	return fmt.Sprint(port)
}
