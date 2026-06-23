package pipeline

import (
	"context"
	"strings"

	"goxidized/pkg/goxidized"
)

func UnifiedDiff(ctx context.Context, targetID string, fromRev, toRev string, oldContent, newContent []byte) (goxidized.DiffResult, error) {
	select {
	case <-ctx.Done():
		return goxidized.DiffResult{}, ctx.Err()
	default:
	}
	oldLines := splitLines(string(oldContent))
	newLines := splitLines(string(newContent))
	diffLines := []string{"--- " + fromRev, "+++ " + toRev}
	i, j := 0, 0
	added, removed := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			diffLines = append(diffLines, " "+oldLines[i])
			i++
			j++
		case j < len(newLines) && (i >= len(oldLines) || !containsAhead(oldLines, i, newLines[j], 6)):
			diffLines = append(diffLines, "+"+newLines[j])
			added++
			j++
		case i < len(oldLines):
			diffLines = append(diffLines, "-"+oldLines[i])
			removed++
			i++
		default:
			j++
		}
	}
	res := goxidized.DiffResult{
		TargetID: targetID, FromRevision: fromRev, ToRevision: toRev,
		UnifiedDiff: strings.Join(diffLines, "\n") + "\n",
		AddedLines:  added, RemovedLines: removed,
	}
	res.Risk, res.Categories, res.RuleHits = ClassifyRisk(res.UnifiedDiff)
	return res, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func containsAhead(lines []string, start int, value string, max int) bool {
	end := start + max
	if end > len(lines) {
		end = len(lines)
	}
	for i := start; i < end; i++ {
		if lines[i] == value {
			return true
		}
	}
	return false
}

func ClassifyRisk(diff string) (goxidized.RiskLevel, []string, []string) {
	type rule struct {
		level goxidized.RiskLevel
		cat   string
		hit   string
		terms []string
	}
	rules := []rule{
		{goxidized.RiskCritical, "aaa", "aaa-disabled-or-admin", []string{"no aaa", "authentication none", "privilege 15", "role network-admin", "logging disable"}},
		{goxidized.RiskHigh, "aaa", "aaa-secret-or-server", []string{"tacacs", "radius", "hwtacacs", "aaa_shared_secret"}},
		{goxidized.RiskHigh, "snmp", "snmp-community", []string{"snmp", "snmp_community"}},
		{goxidized.RiskHigh, "vpn", "vpn-or-routing-auth", []string{"ipsec", "pre-shared-key", "routing_auth_key", "bgp"}},
		{goxidized.RiskMedium, "routing", "routing-policy", []string{"route-policy", "policy-options", "router bgp", "ospf", "isis"}},
		{goxidized.RiskMedium, "management", "mgmt-service", []string{"ntp", "syslog", "logging host", "dns", "name-server"}},
	}
	lower := strings.ToLower(diff)
	max := goxidized.RiskLow
	catSet := map[string]struct{}{}
	hitSet := map[string]struct{}{}
	for _, r := range rules {
		for _, term := range r.terms {
			if strings.Contains(lower, strings.ToLower(term)) {
				if riskRank(r.level) > riskRank(max) {
					max = r.level
				}
				catSet[r.cat] = struct{}{}
				hitSet[r.hit] = struct{}{}
			}
		}
	}
	cats := make([]string, 0, len(catSet))
	for cat := range catSet {
		cats = append(cats, cat)
	}
	hits := make([]string, 0, len(hitSet))
	for hit := range hitSet {
		hits = append(hits, hit)
	}
	if strings.TrimSpace(diff) == "" {
		max = goxidized.RiskNone
	}
	return max, cats, hits
}

func riskRank(level goxidized.RiskLevel) int {
	switch level {
	case goxidized.RiskCritical:
		return 4
	case goxidized.RiskHigh:
		return 3
	case goxidized.RiskMedium:
		return 2
	case goxidized.RiskLow:
		return 1
	default:
		return 0
	}
}
