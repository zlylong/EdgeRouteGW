// Package rulespec validates and translates rule values. It is shared by the
// API (reject bad input early) and the renderer (emit the matching Xray and
// Mosdns syntax), so both always agree on what a value means.
//
// Domain pattern semantics follow Xray:
//
//	c.com     root only            -> full:c.com
//	**.c.com  root + any subdomain -> domain:c.com
//	*.c.com   root + one label     -> regexp
package rulespec

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DomainKind is the pattern family a domain value belongs to.
type DomainKind string

const (
	DomainFull        DomainKind = "full"
	DomainSuffix      DomainKind = "suffix"
	DomainSingleLevel DomainKind = "single_level"
)

// DomainPattern is a parsed domain rule value.
type DomainPattern struct {
	Raw      string
	Kind     DomainKind
	Base     string
	Wildcard bool
}

var domainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrEmptyDomain is returned for blank input.
var ErrEmptyDomain = errors.New("domain is empty")

// ParseDomain normalises and classifies a domain rule value.
func ParseDomain(input string) (DomainPattern, error) {
	raw := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input), "."))
	if raw == "" {
		return DomainPattern{}, ErrEmptyDomain
	}
	p := DomainPattern{Raw: raw, Kind: DomainFull, Base: raw}
	switch {
	case strings.HasPrefix(raw, "**."):
		p.Kind, p.Base, p.Wildcard = DomainSuffix, strings.TrimPrefix(raw, "**."), true
	case strings.HasPrefix(raw, "*."):
		p.Kind, p.Base, p.Wildcard = DomainSingleLevel, strings.TrimPrefix(raw, "*."), true
	}
	if strings.Contains(p.Base, "*") {
		return DomainPattern{}, fmt.Errorf("invalid wildcard position in %q", input)
	}
	parts := strings.Split(p.Base, ".")
	if len(parts) < 2 {
		return DomainPattern{}, fmt.Errorf("%q must contain a root domain like example.com", p.Base)
	}
	for _, part := range parts {
		if !domainLabel.MatchString(part) {
			return DomainPattern{}, fmt.Errorf("invalid domain label %q in %q", part, input)
		}
	}
	return p, nil
}

// XrayValue is the routing rule "domain" entry for this pattern.
func (p DomainPattern) XrayValue() string {
	switch p.Kind {
	case DomainSuffix:
		return "domain:" + p.Base
	case DomainSingleLevel:
		return `regexp:^(?:([^.]+\.)?` + regexp.QuoteMeta(p.Base) + `)$`
	default:
		return "full:" + p.Base
	}
}

// MosdnsValue is the domain_set entry for this pattern.
func (p DomainPattern) MosdnsValue() string {
	switch p.Kind {
	case DomainSuffix:
		return "domain:" + p.Base
	case DomainSingleLevel:
		return `regexp:^([^.]+\.)?` + strings.ReplaceAll(p.Base, ".", `\.`) + `$`
	default:
		return "full:" + p.Base
	}
}

// Matches reports whether host matches the pattern (used by trace/tests).
func (p DomainPattern) Matches(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	switch p.Kind {
	case DomainSuffix:
		return host == p.Base || strings.HasSuffix(host, "."+p.Base)
	case DomainSingleLevel:
		if host == p.Base {
			return true
		}
		rest, ok := strings.CutSuffix(host, "."+p.Base)
		return ok && rest != "" && !strings.Contains(rest, ".")
	default:
		return host == p.Base
	}
}
