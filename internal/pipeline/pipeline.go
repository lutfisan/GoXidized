package pipeline

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"goxidized/pkg/goxidized"
)

const RulesetVersion = "2026-06-23.1"

type Rule struct {
	Name        string
	Category    string
	Pattern     *regexp.Regexp
	Replacement string
}

type Processor struct {
	StrictMode     bool
	HMACKey        []byte
	NormalizeRules []*regexp.Regexp
	RedactionRules []Rule
}

func NewProcessor(strict bool, hmacKey []byte) Processor {
	return Processor{
		StrictMode: strict,
		HMACKey:    append([]byte(nil), hmacKey...),
		NormalizeRules: []*regexp.Regexp{
			regexp.MustCompile(`(?im)^\s*!?\s*(last (configuration )?change|last login|uptime|current time|time:|clock:).*$`),
			regexp.MustCompile(`(?im)^\s*--More--\s*$`),
			regexp.MustCompile(`(?im)^\s*\x1b\[[0-9;]*[A-Za-z]\s*$`),
		},
		RedactionRules: defaultRedactionRules(),
	}
}

func defaultRedactionRules() []Rule {
	return []Rule{
		{Name: "snmp-community", Category: "snmp_community", Pattern: regexp.MustCompile(`(?im)^(\s*(?:snmp-server\s+community|snmp\s+community|snmp-agent\s+community\s+(?:read|write))\s+)(\S+)(.*)$`), Replacement: `${1}<redacted:snmp_community>${3}`},
		{Name: "enable-secret", Category: "enable_secret", Pattern: regexp.MustCompile(`(?im)^(\s*(?:enable|super)\s+(?:secret|password)\s+(?:(?:\d+|cipher|simple)\s+)?)(\S+)(.*)$`), Replacement: `${1}<redacted:enable_secret>${3}`},
		{Name: "local-user-password", Category: "local_user_password", Pattern: regexp.MustCompile(`(?im)^(\s*username\s+\S+\s+(?:privilege\s+\d+\s+)?(?:password|secret)\s+(?:(?:\d+|cipher|simple)\s+)?)(\S+)(.*)$`), Replacement: `${1}<redacted:local_user_password>${3}`},
		{Name: "aaa-shared-secret", Category: "aaa_shared_secret", Pattern: regexp.MustCompile(`(?im)^(\s*(?:tacacs-server|radius-server|hwtacacs|radius)\b.*\bkey\s+(?:cipher\s+|simple\s+|7\s+)?)(\S+)(.*)$`), Replacement: `${1}<redacted:aaa_shared_secret>${3}`},
		{Name: "routing-auth-key", Category: "routing_auth_key", Pattern: regexp.MustCompile(`(?im)^(\s*(?:neighbor\s+\S+\s+password|authentication-key|ospf authentication-key|isis authentication-key)\s+(?:cipher\s+|simple\s+)?)(\S+)(.*)$`), Replacement: `${1}<redacted:routing_auth_key>${3}`},
		{Name: "ipsec-psk", Category: "ipsec_psk", Pattern: regexp.MustCompile(`(?im)^(\s*(?:pre-shared-key|ike.*key|ipsec.*key)\s+(?:cipher\s+|simple\s+)?)(\S+)(.*)$`), Replacement: `${1}<redacted:ipsec_psk>${3}`},
	}
}

func (p Processor) Normalize(ctx context.Context, in []byte) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	out := bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte("\r"), []byte("\n"))
	for _, rule := range p.NormalizeRules {
		out = rule.ReplaceAll(out, nil)
	}
	lines := strings.Split(string(out), "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	sortStableConfigLines(kept)
	return []byte(strings.Join(kept, "\n") + "\n"), nil
}

func sortStableConfigLines(lines []string) {
	// Keep command order intact; this hook exists so future vendor-specific
	// canonicalization has one controlled location.
	_ = lines
}

func (p Processor) Redact(ctx context.Context, in []byte) ([]byte, goxidized.RedactionReport, error) {
	select {
	case <-ctx.Done():
		return nil, goxidized.RedactionReport{}, ctx.Err()
	default:
	}
	text := string(in)
	report := goxidized.RedactionReport{Fingerprints: map[string]string{}}
	categorySet := map[string]struct{}{}
	for _, rule := range p.RedactionRules {
		matches := rule.Pattern.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) > 2 {
				report.SecretsFound++
				categorySet[rule.Category] = struct{}{}
				if len(p.HMACKey) > 0 {
					report.Fingerprints[rule.Category] = fingerprint(p.HMACKey, match[2])
				}
			}
		}
		text = rule.Pattern.ReplaceAllString(text, rule.Replacement)
	}
	for cat := range categorySet {
		report.Categories = append(report.Categories, cat)
	}
	sort.Strings(report.Categories)
	if len(report.Fingerprints) == 0 {
		report.Fingerprints = nil
	}
	if p.StrictMode && looksSecretLike(text) {
		return nil, report, &goxidized.BackupError{Category: goxidized.FailureRedaction, Op: "strict mode", Err: fmt.Errorf("unclassified secret-shaped content remains")}
	}
	return []byte(text), report, nil
}

func looksSecretLike(text string) bool {
	patterns := []string{
		`(?im)\bpassword\s+\S+`,
		`(?im)\bsecret\s+\S+`,
		`(?im)\bcommunity\s+\S+`,
		`(?im)\bpre-shared-key\s+\S+`,
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "<redacted:") {
			continue
		}
		for _, p := range patterns {
			if regexp.MustCompile(p).MatchString(line) {
				return true
			}
		}
	}
	return false
}

func fingerprint(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
