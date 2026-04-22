package main

import (
	"reflect"
	"testing"
)

func TestSaveDomainGeoIPLockTags_ReplacesWholeSet(t *testing.T) {
	setupFeatureSuiteRouter(t)

	domain := "example.com"
	resolverGroup := resolverGroupRemote
	geodataVer := "test-ver"

	saveDomainGeoIPLockTags(domain, resolverGroup, geodataVer, []string{"1.1.1.0/24", "1.1.0.0/16", "invalid", "1.1.1.0/24"})
	got := loadDomainGeoIPLockedTags(domain, resolverGroup, geodataVer)
	want := []string{"1.1.0.0/16", "1.1.1.0/24"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("initial lock set mismatch: got=%v want=%v", got, want)
	}

	saveDomainGeoIPLockTags(domain, resolverGroup, geodataVer, []string{"2.2.0.0/16"})
	got = loadDomainGeoIPLockedTags(domain, resolverGroup, geodataVer)
	want = []string{"2.2.0.0/16"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replaced lock set mismatch: got=%v want=%v", got, want)
	}

	saveDomainGeoIPLockTags(domain, resolverGroup, geodataVer, nil)
	got = loadDomainGeoIPLockedTags(domain, resolverGroup, geodataVer)
	if len(got) != 0 {
		t.Fatalf("expected lock set cleared, got=%v", got)
	}
}
