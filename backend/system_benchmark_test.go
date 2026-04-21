package main

import (
	"encoding/json"
	"testing"
	"time"
)

func clearSessionsForBench() {
	sessions.Range(func(key, value interface{}) bool {
		sessions.Delete(key)
		return true
	})
}

func BenchmarkBuildBaseXrayConfig_ModeA(b *testing.B) {
	for b.Loop() {
		_ = buildBaseXrayConfig("A")
	}
}

func BenchmarkBuildBaseXrayConfig_ModeB(b *testing.B) {
	for b.Loop() {
		_ = buildBaseXrayConfig("B")
	}
}

func BenchmarkBuildBaseXrayConfigParallel_ModeB(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = buildBaseXrayConfig("B")
		}
	})
}

func BenchmarkBuildAndMarshalXrayConfigParallel_ModeB(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cfg := buildBaseXrayConfig("B")
			_, _ = json.Marshal(cfg)
		}
	})
}

func BenchmarkValidateSessionParallel(b *testing.B) {
	clearSessionsForBench()
	token := "bench-valid-token"
	sessions.Store(token, SessionInfo{ExpiresAt: time.Now().Add(24 * time.Hour)})
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if !validateSession(token) {
				b.Fatalf("validateSession unexpectedly failed")
			}
		}
	})
}

func BenchmarkCreateSessionParallel(b *testing.B) {
	clearSessionsForBench()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			token, err := createSession()
			if err != nil || token == "" {
				b.Fatalf("createSession failed: %v", err)
			}
			sessions.Delete(token)
		}
	})
}
