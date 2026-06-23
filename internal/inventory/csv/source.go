package csv

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"goxidized/pkg/goxidized"
)

type Source struct {
	Path string
}

type ValidationError struct {
	Line   int
	Reason string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

func New(path string) Source {
	return Source{Path: path}
}

func (s Source) Load(ctx context.Context) ([]goxidized.Target, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	targets, validationErrs, err := Parse(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(validationErrs) > 0 {
		return targets, errors.Join(validationErrs...)
	}
	return targets, nil
}

func (s Source) Watch(ctx context.Context) (<-chan []goxidized.Target, error) {
	ch := make(chan []goxidized.Target)
	close(ch)
	return ch, nil
}

func Parse(ctx context.Context, r io.Reader) ([]goxidized.Target, []error, error) {
	var raw []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw = append(raw, line)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if len(raw) == 0 {
		return nil, nil, nil
	}
	if strings.Contains(raw[0], ",") {
		return parseComma(ctx, raw)
	}
	return parseColon(ctx, raw)
}

func parseComma(ctx context.Context, lines []string) ([]goxidized.Target, []error, error) {
	reader := csv.NewReader(strings.NewReader(strings.Join(lines, "\n")))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, nil, err
	}
	if len(records) == 0 {
		return nil, nil, nil
	}
	header := map[string]int{}
	for i, h := range records[0] {
		header[strings.ToLower(strings.TrimSpace(h))] = i
	}
	var targets []goxidized.Target
	var errs []error
	ids := map[string]struct{}{}
	for rowIndex, rec := range records[1:] {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}
		t := goxidized.Target{
			ID:            get(rec, header, "id"),
			Hostname:      get(rec, header, "hostname"),
			IPAddress:     get(rec, header, "ip_address"),
			Vendor:        get(rec, header, "vendor"),
			Group:         get(rec, header, "group"),
			Site:          get(rec, header, "site"),
			Role:          get(rec, header, "role"),
			JumpHost:      get(rec, header, "jump_host"),
			CredentialRef: get(rec, header, "credential_ref"),
			Enabled:       parseBoolDefault(get(rec, header, "enabled"), true),
			TelnetEnabled: parseBoolDefault(get(rec, header, "telnet_enabled"), false),
		}
		t.Tags = splitList(get(rec, header, "tags"))
		port, err := parsePort(get(rec, header, "port"))
		if err != nil {
			errs = append(errs, ValidationError{Line: rowIndex + 2, Reason: err.Error()})
			continue
		}
		t.Port = port
		if t.ID == "" {
			t.ID = t.Hostname
		}
		if err := validateTarget(t, ids); err != nil {
			errs = append(errs, ValidationError{Line: rowIndex + 2, Reason: err.Error()})
			continue
		}
		ids[t.ID] = struct{}{}
		targets = append(targets, t)
	}
	return targets, errs, nil
}

func parseColon(ctx context.Context, lines []string) ([]goxidized.Target, []error, error) {
	var targets []goxidized.Target
	var errs []error
	ids := map[string]struct{}{}
	for i, line := range lines {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}
		parts := strings.Split(line, ":")
		if len(parts) < 5 {
			errs = append(errs, ValidationError{Line: i + 1, Reason: "router.db line must be hostname:vendor:ip_address:group:credential_ref[:site][:role]"})
			continue
		}
		credentialRef, site, role := parseColonTail(parts[4:])
		t := goxidized.Target{
			ID:            strings.TrimSpace(parts[0]),
			Hostname:      strings.TrimSpace(parts[0]),
			Vendor:        strings.TrimSpace(parts[1]),
			IPAddress:     strings.TrimSpace(parts[2]),
			Group:         strings.TrimSpace(parts[3]),
			CredentialRef: credentialRef,
			Site:          site,
			Role:          role,
			Enabled:       true,
			Port:          22,
		}
		if err := validateTarget(t, ids); err != nil {
			errs = append(errs, ValidationError{Line: i + 1, Reason: err.Error()})
			continue
		}
		ids[t.ID] = struct{}{}
		targets = append(targets, t)
	}
	return targets, errs, nil
}

func parseColonTail(parts []string) (credentialRef, site, role string) {
	if len(parts) == 0 {
		return "", "", ""
	}
	if len(parts) >= 2 && strings.HasPrefix(parts[1], "//") {
		credentialRef = strings.TrimSpace(parts[0] + ":" + parts[1])
		if len(parts) > 2 {
			site = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			role = strings.TrimSpace(parts[3])
		}
		return credentialRef, site, role
	}
	credentialRef = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		site = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		role = strings.TrimSpace(parts[2])
	}
	return credentialRef, site, role
}

func get(rec []string, header map[string]int, name string) string {
	i, ok := header[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[i])
}

func parseBoolDefault(v string, def bool) bool {
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parsePort(v string) (int, error) {
	if v == "" {
		return 22, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q", v)
	}
	return n, nil
}

func splitList(v string) []string {
	if v == "" {
		return nil
	}
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == '|' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func validateTarget(t goxidized.Target, ids map[string]struct{}) error {
	if t.ID == "" {
		return errors.New("id is required")
	}
	if _, ok := ids[t.ID]; ok {
		return fmt.Errorf("duplicate id %q", t.ID)
	}
	if t.Hostname == "" {
		return errors.New("hostname is required")
	}
	if t.IPAddress == "" {
		return errors.New("ip_address is required")
	}
	if t.Vendor == "" {
		return errors.New("vendor is required")
	}
	if t.Group == "" {
		return errors.New("group is required")
	}
	if t.CredentialRef == "" {
		return errors.New("credential_ref is required")
	}
	if t.Port <= 0 || t.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}
