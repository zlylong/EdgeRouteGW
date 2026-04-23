package main

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	domainRulePatternSuffix      = "suffix"
	domainRulePatternSingleLevel = "single_level"
)

type domainRulePattern struct {
	Raw        string
	Kind       string
	Base       string
	IsWildcard bool
}

var domainLabelRegexp = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func parseDomainRulePattern(input string) (domainRulePattern, error) {
	raw := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if raw == "" {
		return domainRulePattern{}, fmt.Errorf("domain rule value is empty")
	}

	pattern := domainRulePattern{Raw: raw, Kind: domainRulePatternSuffix, Base: raw}
	switch {
	case strings.HasPrefix(raw, "**."):
		pattern.Base = strings.TrimPrefix(raw, "**.")
		pattern.IsWildcard = true
	case strings.HasPrefix(raw, "*."):
		pattern.Kind = domainRulePatternSingleLevel
		pattern.Base = strings.TrimPrefix(raw, "*.")
		pattern.IsWildcard = true
	}
	if strings.Contains(pattern.Base, "*") {
		return domainRulePattern{}, fmt.Errorf("invalid wildcard domain pattern %q", input)
	}
	if err := validateDomainPatternBase(pattern.Base); err != nil {
		return domainRulePattern{}, err
	}
	return pattern, nil
}

func validateDomainPatternBase(base string) error {
	parts := strings.Split(base, ".")
	if len(parts) < 2 {
		return fmt.Errorf("domain rule %q must contain a root domain like example.com", base)
	}
	for _, part := range parts {
		if !domainLabelRegexp.MatchString(part) {
			return fmt.Errorf("invalid domain label %q", part)
		}
	}
	return nil
}

func isWildcardDomainRuleValue(value string) bool {
	pattern, err := parseDomainRulePattern(value)
	if err != nil {
		return false
	}
	return pattern.IsWildcard
}

func isDomainMatch(host string, pattern string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	parsed, err := parseDomainRulePattern(pattern)
	if err != nil {
		return false
	}
	switch parsed.Kind {
	case domainRulePatternSuffix:
		return host == parsed.Base || strings.HasSuffix(host, "."+parsed.Base)
	case domainRulePatternSingleLevel:
		if host == parsed.Base {
			return true
		}
		prefix, ok := strings.CutSuffix(host, "."+parsed.Base)
		if !ok || prefix == "" {
			return false
		}
		return !strings.Contains(prefix, ".")
	default:
		return false
	}
}

func buildXrayDomainRuleValues(value string) ([]string, error) {
	pattern, err := parseDomainRulePattern(value)
	if err != nil {
		return nil, err
	}
	switch pattern.Kind {
	case domainRulePatternSuffix:
		return []string{"domain:" + pattern.Base}, nil
	case domainRulePatternSingleLevel:
		quoted := regexp.QuoteMeta(pattern.Base)
		return []string{`regexp:^(?:([^.]+\.)?` + quoted + `)$`}, nil
	default:
		return nil, fmt.Errorf("unsupported domain rule pattern %q", value)
	}
}

func mosdnsRuleDomainValue(value string) (string, bool) {
	pattern, err := parseDomainRulePattern(value)
	if err != nil {
		return "", false
	}
	if pattern.Kind != domainRulePatternSuffix {
		return "", false
	}
	return pattern.Base, true
}
