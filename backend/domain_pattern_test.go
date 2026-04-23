package main

import "testing"

func TestParseDomainRulePattern(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantKind     string
		wantBase     string
		wantErr      bool
		wantWildcard bool
	}{
		{name: "plain suffix", input: "c.com", wantKind: domainRulePatternSuffix, wantBase: "c.com"},
		{name: "double star suffix", input: "**.c.com", wantKind: domainRulePatternSuffix, wantBase: "c.com", wantWildcard: true},
		{name: "single star one level", input: "*.c.com", wantKind: domainRulePatternSingleLevel, wantBase: "c.com", wantWildcard: true},
		{name: "invalid star position", input: "foo.*.c.com", wantErr: true},
		{name: "invalid empty base", input: "*.", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDomainRulePattern(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Kind != tt.wantKind || got.Base != tt.wantBase || got.IsWildcard != tt.wantWildcard {
				t.Fatalf("unexpected pattern: %+v", got)
			}
		})
	}
}

func TestIsDomainMatchSupportsWildcardSemantics(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		pattern string
		want    bool
	}{
		{name: "plain root", host: "c.com", pattern: "c.com", want: true},
		{name: "plain any level", host: "bar.foo.c.com", pattern: "c.com", want: true},
		{name: "double star root", host: "c.com", pattern: "**.c.com", want: true},
		{name: "double star any level", host: "bar.foo.c.com", pattern: "**.c.com", want: true},
		{name: "single star root", host: "c.com", pattern: "*.c.com", want: true},
		{name: "single star one level", host: "foo.c.com", pattern: "*.c.com", want: true},
		{name: "single star multi level not match", host: "bar.foo.c.com", pattern: "*.c.com", want: false},
		{name: "single star sibling not match", host: "x.com", pattern: "*.c.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDomainMatch(tt.host, tt.pattern); got != tt.want {
				t.Fatalf("isDomainMatch(%q, %q)=%v want %v", tt.host, tt.pattern, got, tt.want)
			}
		})
	}
}
