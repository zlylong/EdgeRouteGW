package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// execInTx runs fn inside a transaction on the active DB and rolls back on
// any error. The OSPF batch writers used to ignore Begin/Exec/Commit errors
// entirely, which panicked on a nil *sql.Tx when the DB was unavailable.
func execInTx(fn func(tx *sql.Tx) error) error {
	d := getDB()
	if d == nil {
		return errors.New("database not initialised")
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// markRoutesFailedPolicy moves candidates that policy or the allowlist
// rejected to failed_policy so they are not retried every cycle. Routes in
// except are left alone.
func markRoutesFailedPolicy(ips []string, except map[string]struct{}) error {
	return execInTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("UPDATE routes_table SET status='failed_policy', miss_count=miss_count+1 WHERE ip=? AND status='candidate'")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, ip := range ips {
			if _, skip := except[ip]; skip {
				continue
			}
			if _, err := stmt.Exec(ip); err != nil {
				return err
			}
		}
		return nil
	})
}

func applyOspfDeleteBatch(toDel []string) bool {
	if len(toDel) == 0 {
		return false
	}
	var buf bytes.Buffer
	applied := make([]string, 0, len(toDel))
	for _, ip := range toDel {
		addOspfLog("[DEL] " + ip + " (Miss count >= 3)")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		applied = append(applied, ip)
		buf.WriteString(fmt.Sprintf("no ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	if len(applied) == 0 {
		return false
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] DEL batch=%d apply_failed: %v, out=%q", len(applied), err, strings.TrimSpace(out))
		return false
	}
	if err := execInTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("DELETE FROM routes_table WHERE ip=?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, ip := range applied {
			if _, err := stmt.Exec(ip); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Printf("[FRR] DEL batch=%d applied via vtysh but routes_table update failed: %v", len(applied), err)
		return false
	}
	log.Printf("[FRR] DEL batch=%d applied via vtysh", len(applied))
	return true
}

func applyOspfAddBatch(toAdd []string) bool {
	if len(toAdd) == 0 {
		return false
	}
	allowlist := loadOspfPublishAllowlist()
	var buf bytes.Buffer
	allowed := make([]string, 0, len(toAdd))
	skipped := 0
	for _, ip := range toAdd {
		if err := validateAdvertisableCIDR(ip); err != nil {
			skipped++
			logGatewayEventThrottled("ospf_publish_policy_reject", 30*time.Second, "warn", "ospf", "publish_policy_reject", "OSPF publish route rejected by policy", map[string]interface{}{"route": ip, "reason": err.Error()})
			continue
		}
		if !routeAllowedByOspfPublishAllowlist(ip, allowlist) {
			skipped++
			logGatewayEventThrottled("ospf_publish_allowlist_reject", 30*time.Second, "warn", "ospf", "publish_allowlist_reject", "OSPF publish route rejected by allowlist", map[string]interface{}{"route": ip})
			continue
		}
		allowed = append(allowed, ip)
		addOspfLog("[ADD] " + ip + " to published_set")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	if len(allowed) == 0 {
		if skipped > 0 {
			log.Printf("[FRR] ADD blocked by ospf publish allowlist: requested=%d skipped=%d", len(toAdd), skipped)
			// GC blocked candidates: if a candidate is blocked by policy/allowlist,
			// mark it as 'failed_policy' so it doesn't stay in 'candidate' forever.
			if err := markRoutesFailedPolicy(toAdd, nil); err != nil {
				log.Printf("[FRR] mark blocked candidates failed: %v", err)
			}
		}
		return false
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] ADD batch=%d apply_failed: %v, out=%q", len(allowed), err, strings.TrimSpace(out))
		return false
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, ip := range allowed {
		allowedSet[ip] = struct{}{}
	}
	if err := execInTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare("UPDATE routes_table SET status='published', last_seen=datetime('now'), miss_count=0 WHERE ip=?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, ip := range allowed {
			if _, err := stmt.Exec(ip); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Printf("[FRR] ADD batch=%d applied via vtysh but routes_table update failed: %v", len(allowed), err)
		return false
	}
	// Also mark skipped ones in this batch if some were allowed
	if err := markRoutesFailedPolicy(toAdd, allowedSet); err != nil {
		log.Printf("[FRR] mark skipped candidates failed: %v", err)
	}
	if skipped > 0 {
		log.Printf("[FRR] ADD batch=%d applied via vtysh (allowlist_skipped=%d)", len(allowed), skipped)
	} else {
		log.Printf("[FRR] ADD batch=%d applied via vtysh", len(allowed))
	}
	return true
}
