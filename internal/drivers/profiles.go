package drivers

import (
	"regexp"

	"goxidized/internal/pipeline"
	"goxidized/pkg/goxidized"
)

const defaultMaxConfigBytes = 16 * 1024 * 1024

type profileCommand struct {
	Command      string
	AllowFailure bool
}

type outputMarker struct {
	Name     string
	Category goxidized.FailureCategory
	Pattern  *regexp.Regexp
}

type commandProfile struct {
	Vendor         string
	Prepare        []profileCommand
	FetchCommand   string
	PromptPatterns []*regexp.Regexp
	PagingPatterns []*regexp.Regexp
	Markers        []outputMarker
	NormalizeRules []*regexp.Regexp
	RedactionRules []pipeline.Rule
	MaxBytes       int
}

func (p commandProfile) clone() commandProfile {
	cp := p
	cp.Prepare = append([]profileCommand(nil), p.Prepare...)
	cp.PromptPatterns = append([]*regexp.Regexp(nil), p.PromptPatterns...)
	cp.PagingPatterns = append([]*regexp.Regexp(nil), p.PagingPatterns...)
	cp.Markers = append([]outputMarker(nil), p.Markers...)
	cp.NormalizeRules = append([]*regexp.Regexp(nil), p.NormalizeRules...)
	cp.RedactionRules = append([]pipeline.Rule(nil), p.RedactionRules...)
	if cp.MaxBytes == 0 {
		cp.MaxBytes = defaultMaxConfigBytes
	}
	return cp
}

func ciscoIOSXEProfile() commandProfile {
	return commandProfile{
		Vendor:       "cisco_iosxe",
		Prepare:      commands("terminal length 0", "terminal width 0"),
		FetchCommand: "show running-config",
		PromptPatterns: regexps(
			`^[A-Za-z0-9_.:/-]+(?:\([^)]+\))?[#>]`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: appendMarkers(commonCiscoMarkers(),
			marker("cisco privilege", goxidized.FailurePrivilege, `(?im)^% ?(?:Authorization failed|Access denied|Permission denied|Privilege level insufficient).*$`),
			marker("cisco timeout", goxidized.FailureTimeout, `(?im)^% ?(?:Timed out|Command timed out).*$`),
			marker("cisco oversized", goxidized.FailureCommand, `(?im)^% ?(?:Output truncated|Configuration too large|Exceeded maximum output).*$`),
		),
		NormalizeRules: commonCiscoNormalizeRules(),
		MaxBytes:       defaultMaxConfigBytes,
	}
}

func huaweiVRPProfile() commandProfile {
	return commandProfile{
		Vendor:       "huawei_vrp",
		Prepare:      commands("screen-length 0 temporary"),
		FetchCommand: "display current-configuration",
		PromptPatterns: regexps(
			`^(?:<[^>\n]+>|\[[^\]\n]+\])`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: []outputMarker{
			marker("huawei privilege", goxidized.FailurePrivilege, `(?im)^Error: (?:Permission denied|The user does not have permission|Insufficient permission).*$`),
			marker("huawei timeout", goxidized.FailureTimeout, `(?im)^(?:Error|Info): .*timed? ?out.*$`),
			marker("huawei oversized", goxidized.FailureCommand, `(?im)^Error: .*output.*(?:too large|exceed|truncated).*$`),
			marker("huawei command", goxidized.FailureCommand, `(?im)^Error: .*(?:Unrecognized command|The command is not found|Too many parameters|Incomplete command|Wrong parameter).*$`),
		},
		NormalizeRules: regexps(
			`(?im)^Info: Current configuration.*$`,
			`(?im)^#\s*display current-configuration.*$`,
		),
		RedactionRules: []pipeline.Rule{
			rule("huawei-local-user-password", "local_user_password", `(?im)^(\s*local-user\s+\S+\s+password\s+(?:(?:irreversible-cipher|cipher|simple)\s+)?)(\S+)(.*)$`, `${1}<redacted:local_user_password>${3}`),
		},
		MaxBytes: defaultMaxConfigBytes,
	}
}

func ciscoIOSXRProfile() commandProfile {
	return commandProfile{
		Vendor:       "cisco_iosxr",
		Prepare:      commands("terminal length 0", "terminal width 0"),
		FetchCommand: "show running-config",
		PromptPatterns: regexps(
			`^[A-Za-z0-9_.:/-]+(?:\([^)]+\))?[#>]`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: appendMarkers(commonCiscoMarkers(),
			marker("iosxr privilege", goxidized.FailurePrivilege, `(?im)^% ?(?:Authorization failed|Access denied|Permission denied|Insufficient privileges).*$`),
			marker("iosxr timeout", goxidized.FailureTimeout, `(?im)^% ?(?:Timed out|Command timed out).*$`),
			marker("iosxr oversized", goxidized.FailureCommand, `(?im)^% ?(?:Output truncated|Configuration too large|Exceeded maximum output).*$`),
		),
		NormalizeRules: regexps(
			`(?im)^Building configuration\.\.\..*$`,
			`(?im)^!! IOS XR Configuration.*$`,
			`(?im)^!! Last configuration change.*$`,
			`(?im)^Current configuration\s*:.*$`,
		),
		RedactionRules: []pipeline.Rule{
			rule("iosxr-nested-user-secret", "local_user_password", `(?im)^(\s*secret\s+(?:\d+\s+)?)(\S+)(.*)$`, `${1}<redacted:local_user_password>${3}`),
		},
		MaxBytes: defaultMaxConfigBytes,
	}
}

func juniperJunosProfile() commandProfile {
	return commandProfile{
		Vendor:       "juniper_junos",
		Prepare:      commands("set cli screen-length 0", "set cli screen-width 0"),
		FetchCommand: "show configuration | display set | no-more",
		PromptPatterns: regexps(
			`^[A-Za-z0-9_.@:/-]+[>%#]`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: []outputMarker{
			marker("junos privilege", goxidized.FailurePrivilege, `(?im)^(?:error: )?(?:permission denied|authorization failed|insufficient privileges).*$`),
			marker("junos timeout", goxidized.FailureTimeout, `(?im)^(?:error: )?.*timed? ?out.*$`),
			marker("junos oversized", goxidized.FailureCommand, `(?im)^(?:error: )?.*(?:output too large|output truncated|response too large).*$`),
			marker("junos command", goxidized.FailureCommand, `(?im)^(?:error: )?(?:syntax error|unknown command|invalid command).*$`),
		},
		NormalizeRules: regexps(
			`(?im)^## Last commit:.*$`,
			`(?im)^# Last commit:.*$`,
		),
		RedactionRules: []pipeline.Rule{
			rule("junos-login-password", "local_user_password", `(?im)^(\s*set\s+system\s+login\s+user\s+\S+\s+authentication\s+(?:encrypted-password|plain-text-password)\s+)("[^"\r\n]+"|\S+)(.*)$`, `${1}<redacted:local_user_password>${3}`),
			rule("junos-root-password", "local_user_password", `(?im)^(\s*set\s+system\s+root-authentication\s+(?:encrypted-password|plain-text-password)\s+)("[^"\r\n]+"|\S+)(.*)$`, `${1}<redacted:local_user_password>${3}`),
			rule("junos-snmp-community", "snmp_community", `(?im)^(\s*set\s+snmp\s+community\s+)("[^"\r\n]+"|\S+)(.*)$`, `${1}<redacted:snmp_community>${3}`),
			rule("junos-aaa-secret", "aaa_shared_secret", `(?im)^(\s*set\s+system\s+(?:radius-server|tacplus-server)\s+\S+\s+(?:secret|password)\s+)("[^"\r\n]+"|\S+)(.*)$`, `${1}<redacted:aaa_shared_secret>${3}`),
			rule("junos-routing-auth-key", "routing_auth_key", `(?im)^(\s*set\s+protocols\s+.+\s+authentication-key\s+)("[^"\r\n]+"|\S+)(.*)$`, `${1}<redacted:routing_auth_key>${3}`),
		},
		MaxBytes: defaultMaxConfigBytes,
	}
}

func zteZXR10Profile() commandProfile {
	return commandProfile{
		Vendor:       "zte_zxr10",
		Prepare:      commands("terminal length 0"),
		FetchCommand: "show running-config",
		PromptPatterns: regexps(
			`^[A-Za-z0-9_.:/-]+(?:\([^)]+\))?[#>]`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: []outputMarker{
			marker("zte privilege", goxidized.FailurePrivilege, `(?im)^(?:% ?)?(?:No privilege|Permission denied|Authorization failed|Insufficient privilege).*$`),
			marker("zte timeout", goxidized.FailureTimeout, `(?im)^(?:% ?)?(?:Command timed out|Timed out).*$`),
			marker("zte oversized", goxidized.FailureCommand, `(?im)^(?:% ?)?(?:Output too large|Output truncated|Exceeded maximum output).*$`),
			marker("zte command", goxidized.FailureCommand, `(?im)^(?:% ?)?(?:Invalid command|Incomplete command|Ambiguous command|Command execution failed|Code 202).*$`),
		},
		NormalizeRules: regexps(
			`(?im)^Building configuration\.\.\..*$`,
			`(?im)^Current configuration\s*:.*$`,
		),
		MaxBytes: defaultMaxConfigBytes,
	}
}

func ericssonIPOSProfile() commandProfile {
	return commandProfile{
		Vendor:       "ericsson_ipos",
		Prepare:      commands("terminal length 0", "terminal width 0"),
		FetchCommand: "show configuration",
		PromptPatterns: regexps(
			`^[A-Za-z0-9_.:@/-]+(?:\([^)]+\))?[#>]`,
		),
		PagingPatterns: commonPagingPatterns(),
		Markers: []outputMarker{
			marker("ipos privilege", goxidized.FailurePrivilege, `(?im)^(?:ERROR: )?(?:Permission denied|Authorization failed|Insufficient privileges).*$`),
			marker("ipos timeout", goxidized.FailureTimeout, `(?im)^(?:ERROR: )?.*timed? ?out.*$`),
			marker("ipos oversized", goxidized.FailureCommand, `(?im)^(?:ERROR: )?.*(?:output too large|output truncated|response too large).*$`),
			marker("ipos command", goxidized.FailureCommand, `(?im)^(?:ERROR: )?(?:Invalid command|Unknown command|Command failed).*$`),
		},
		NormalizeRules: regexps(
			`(?im)^Current configuration\s*:.*$`,
			`(?im)^Building configuration\.\.\..*$`,
		),
		RedactionRules: []pipeline.Rule{
			rule("ipos-user-password", "local_user_password", `(?im)^(\s*(?:system\s+)?user\s+\S+\s+(?:encrypted-password|password)\s+)(\S+)(.*)$`, `${1}<redacted:local_user_password>${3}`),
		},
		MaxBytes: defaultMaxConfigBytes,
	}
}

func commands(values ...string) []profileCommand {
	out := make([]profileCommand, 0, len(values))
	for _, value := range values {
		out = append(out, profileCommand{Command: value})
	}
	return out
}

func regexps(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, regexp.MustCompile(pattern))
	}
	return out
}

func rule(name, category, pattern, replacement string) pipeline.Rule {
	return pipeline.Rule{
		Name:        name,
		Category:    category,
		Pattern:     regexp.MustCompile(pattern),
		Replacement: replacement,
	}
}

func marker(name string, category goxidized.FailureCategory, pattern string) outputMarker {
	return outputMarker{
		Name:     name,
		Category: category,
		Pattern:  regexp.MustCompile(pattern),
	}
}

func appendMarkers(base []outputMarker, extra ...outputMarker) []outputMarker {
	out := append([]outputMarker(nil), base...)
	return append(out, extra...)
}

func commonPagingPatterns() []*regexp.Regexp {
	return regexps(
		`(?i)[ \t]*<---[ \t]*more[ \t]*--->[ \t]*`,
		`(?i)[ \t]*----[ \t]*more[ \t]*----[ \t]*`,
		`(?i)[ \t]*--+[ \t]*more[ \t]*--+[ \t]*`,
		`(?i)[ \t]*-+\(?more(?:[ \t]+\d+%)?\)?-+[ \t]*`,
	)
}

func commonCiscoMarkers() []outputMarker {
	return []outputMarker{
		marker("cisco command", goxidized.FailureCommand, `(?im)^% ?(?:Invalid input|Incomplete command|Ambiguous command|Unknown command).*$`),
	}
}

func commonCiscoNormalizeRules() []*regexp.Regexp {
	return regexps(
		`(?im)^Building configuration\.\.\..*$`,
		`(?im)^Current configuration\s*:.*$`,
	)
}
