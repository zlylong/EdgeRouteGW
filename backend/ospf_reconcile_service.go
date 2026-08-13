package main

import (
	"bytes"
	"fmt"
	"log"
	"sort"
	"strings"
)

func reconcilePublishedRoutesWithFRR() {
	frrRoutes, err := readFRRTaggedStaticRoutes()
	if err != nil {
		log.Printf("[WARN] OSPF reconcile skip: %v", err)
		return
	}
	rows, err := getDB().Query("SELECT ip, status FROM routes_table WHERE source='static'")
	if err != nil {
		log.Printf("[WARN] OSPF reconcile query failed: %v", err)
		return
	}
	defer rows.Close()

	tx, err := getDB().Begin()
	if err != nil {
		log.Printf("[WARN] OSPF reconcile begin failed: %v", err)
		return
	}

	updatedToPublished := 0
	demotedToCandidate := 0
	totalPublished := 0
	dbStaticSet := make(map[string]struct{})
	for rows.Next() {
		var ip string
		var status string
		if rows.Scan(&ip, &status) != nil {
			continue
		}
		routeKey, ok := normalizeRouteKey(ip)
		if !ok {
			continue
		}
		dbStaticSet[routeKey] = struct{}{}
		_, inFRR := frrRoutes[routeKey]
		if status == "published" {
			totalPublished++
		}
		if inFRR && status != "published" {
			if _, err := tx.Exec("UPDATE routes_table SET status='published', last_seen=datetime('now'), miss_count=0 WHERE ip=?", routeKey); err == nil {
				updatedToPublished++
			}
			continue
		}
		if !inFRR && status == "published" {
			if _, err := tx.Exec("UPDATE routes_table SET status='candidate', miss_count=0 WHERE ip=?", routeKey); err == nil {
				demotedToCandidate++
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		log.Printf("[WARN] OSPF reconcile row err: %v", err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[WARN] OSPF reconcile commit failed: %v", err)
		return
	}

	var mode string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil || strings.TrimSpace(mode) == "" {
		mode = "A"
	}
	mode = strings.TrimSpace(mode)

	prunedOrphans := 0
	if mode == "C" {
		orphanRoutes := make([]string, 0)
		for routeKey := range frrRoutes {
			if _, ok := dbStaticSet[routeKey]; ok {
				continue
			}
			orphanRoutes = append(orphanRoutes, routeKey)
		}
		if len(orphanRoutes) > 0 {
			sort.Strings(orphanRoutes)
			var buf bytes.Buffer
			for _, routeKey := range orphanRoutes {
				buf.WriteString(fmt.Sprintf("no ip route %s 127.0.0.1 tag 100\n", routeKey))
			}
			out, err := runVtyshConfigBatch(buf.String())
			if err != nil {
				log.Printf("[WARN] OSPF reconcile orphan prune failed: count=%d err=%v out=%q", len(orphanRoutes), err, strings.TrimSpace(out))
			} else {
				prunedOrphans = len(orphanRoutes)
				for _, routeKey := range orphanRoutes {
					delete(frrRoutes, routeKey)
				}
				log.Printf("[OSPF] reconcile pruned FRR orphan tagged routes in mode C: %d", prunedOrphans)
			}
		}
	}

	if updatedToPublished > 0 || demotedToCandidate > 0 || totalPublished != len(frrRoutes) || prunedOrphans > 0 {
		log.Printf("[OSPF] reconcile DB<->FRR: mode=%s frr_tagged=%d db_published(before)=%d promoted=%d demoted=%d pruned_orphans=%d", mode, len(frrRoutes), totalPublished, updatedToPublished, demotedToCandidate, prunedOrphans)
	}
}
