package main

import (
	"log"
	"os"
)

func scheduleApplyFallbackIfRuntimeReady(needMosdns bool) {
	if _, err := os.Stat(getPath("core", "xray", "xray")); err != nil {
		log.Printf("[WARN] skip scheduled apply fallback: xray binary not ready: %v", err)
		return
	}
	scheduleApplyWithMosdns(needMosdns)
}
