package dotenv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"goxidized/pkg/goxidized"
)

type Provider struct {
	Path            string
	RequireChmod600 bool
	values          map[string]string
}

func New(path string, requireChmod600 bool) *Provider {
	return &Provider{Path: path, RequireChmod600: requireChmod600}
}

func (p *Provider) Resolve(ctx context.Context, ref string) (goxidized.Credentials, error) {
	select {
	case <-ctx.Done():
		return goxidized.Credentials{}, ctx.Err()
	default:
	}
	if p.values == nil {
		if err := p.load(); err != nil {
			return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "dotenv load", Err: err}
		}
	}
	name := strings.TrimPrefix(ref, "dotenv://")
	if name == ref {
		name = strings.TrimPrefix(ref, "env://")
	}
	name = normalizeKey(name)
	if name == "" {
		return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "dotenv resolve", Err: errors.New("empty credential ref")}
	}
	creds := goxidized.Credentials{
		Username:      p.lookup(name + "_USERNAME"),
		Password:      goxidized.NewSecretString(p.lookup(name + "_PASSWORD")),
		PrivateKeyPEM: goxidized.NewSecretBytes([]byte(p.lookup(name + "_PRIVATE_KEY_PEM"))),
		EnableSecret:  goxidized.NewSecretString(p.lookup(name + "_ENABLE_SECRET")),
		Source:        "dotenv",
	}
	switch {
	case !creds.PrivateKeyPEM.IsZero():
		creds.AuthType = "private_key"
	case !creds.Password.IsZero():
		creds.AuthType = "password"
	}
	if creds.Username == "" {
		return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "dotenv resolve", Err: fmt.Errorf("%s_USERNAME is required", name)}
	}
	if creds.Password.IsZero() && creds.PrivateKeyPEM.IsZero() {
		return goxidized.Credentials{}, &goxidized.BackupError{Category: goxidized.FailureCredentialProvider, Op: "dotenv resolve", Err: fmt.Errorf("%s_PASSWORD or %s_PRIVATE_KEY_PEM is required", name, name)}
	}
	return creds, nil
}

func (p *Provider) load() error {
	values := map[string]string{}
	for _, env := range os.Environ() {
		k, v, ok := strings.Cut(env, "=")
		if ok {
			values[k] = v
		}
	}
	if p.Path != "" {
		if p.RequireChmod600 && runtime.GOOS != "windows" {
			info, err := os.Stat(p.Path)
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o077 != 0 {
				return fmt.Errorf("%s permissions must not allow group/other access", p.Path)
			}
		}
		f, err := os.Open(p.Path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			values[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
		if err := sc.Err(); err != nil {
			return err
		}
	}
	p.values = values
	return nil
}

func (p *Provider) lookup(key string) string {
	if p.values == nil {
		return ""
	}
	return p.values[key]
}

func normalizeKey(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.Trim(ref, "/")
	ref = strings.ReplaceAll(ref, "-", "_")
	ref = strings.ReplaceAll(ref, ".", "_")
	ref = strings.ToUpper(ref)
	return ref
}
