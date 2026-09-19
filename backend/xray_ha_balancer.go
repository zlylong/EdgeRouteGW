package main

import (
	"log"
	"strings"
)

// resolveHABalancer turns an "ha-<primary>-<fallback>" rule target into what
// Xray should actually be given, based on which of the two nodes still exist
// as outbounds.
//
// The pair is stored in rules.policy by node id, and nothing updates it when a
// node is deleted, disabled or re-imported under a new id. Emitting the
// balancer verbatim then leaves selector or fallbackTag naming an outbound
// that is not in the config. Xray accepts that silently: the config passes
// -test, the service starts, and the rule only misbehaves once the surviving
// node goes down — exactly when the fallback was supposed to help.
//
//   - both present:   the normal balancer
//   - primary only:   balancer without a fallbackTag
//   - fallback only:  no balancer, route the rule straight to the fallback
//   - neither:        (nil, "") so the caller's dead-target policy applies
func resolveHABalancer(bTag string, outbounds map[string]struct{}) (map[string]interface{}, string) {
	parts := strings.Split(strings.TrimPrefix(bTag, "bal-ha-"), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, ""
	}
	primary := "proxy-" + parts[0] + "-out"
	fallback := "proxy-" + parts[1] + "-out"
	_, hasPrimary := outbounds[primary]
	_, hasFallback := outbounds[fallback]

	switch {
	case hasPrimary && hasFallback:
		return map[string]interface{}{
			"tag":         bTag,
			"selector":    []string{primary},
			"fallbackTag": fallback,
		}, ""
	case hasPrimary:
		log.Printf("[WARN] HA rule target %s: fallback node %s does not exist or is disabled; running without a fallback", bTag, parts[1])
		return map[string]interface{}{
			"tag":      bTag,
			"selector": []string{primary},
		}, ""
	case hasFallback:
		log.Printf("[WARN] HA rule target %s: primary node %s does not exist or is disabled; routing directly to fallback node %s", bTag, parts[0], parts[1])
		return nil, fallback
	default:
		log.Printf("[WARN] HA rule target %s: neither node %s nor %s exists or is enabled", bTag, parts[0], parts[1])
		return nil, ""
	}
}
