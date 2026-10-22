package v2

import (
	"database/sql"
	"fmt"
	"github.com/zlylong/EdgeRouteGW/internal/update"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/desired"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/store"
	"github.com/zlylong/EdgeRouteGW/internal/version"
)

func (d Deps) registerBackup(r *router) {
	r.handle(http.MethodGet, "/api/v2/backup", d.exportBackup)
	r.handle(http.MethodPost, "/api/v2/restore", d.restoreBackup)
}

// Backup is the portable desired-state document. Credentials are included
// only when requested; without them nodes come back and need their secrets
// re-entered.
type Backup struct {
	Format         string                    `json:"format"` // "edgeroutegw-backup/1"
	ExportedAt     time.Time                 `json:"exported_at"`
	Version        string                    `json:"version"`
	IncludeSecrets bool                      `json:"include_secrets"`
	Network        model.NetworkConfig       `json:"network"`
	DNS            model.DNSConfig           `json:"dns"`
	Nodes          []backupNode              `json:"nodes"`
	Groups         []model.OutboundGroup     `json:"groups"`
	RuleSets       []model.RuleSet           `json:"rule_sets"`
	Rules          []model.Rule              `json:"rules"`
	DevicePolicies []model.DevicePolicy      `json:"device_policies"`
	Bypass         []model.BypassDestination `json:"bypass"`
	GeodataSched   string                    `json:"geodata_schedule,omitempty"`
	UpdateSched    map[string]string         `json:"update_schedules,omitempty"` // xray / mosdns auto-update specs
}

type backupNode struct {
	model.Node
	Credential string `json:"credential,omitempty"`
}

const backupFormat = "edgeroutegw-backup/1"

func (d Deps) exportBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	withSecrets := r.URL.Query().Get("include_secrets") == "1"
	if withSecrets {
		if !sessionOnly(w, r) {
			return
		}
		_ = d.Store.AddEvent(ctx, model.Event{Level: model.LevelWarn, Module: "backup", Type: "export.secrets", Message: "backup exported with node credentials", Actor: actorOf(r)})
	}
	snap, err := desired.Load(ctx, d.Store, d.Keyring)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	b := Backup{Format: backupFormat, ExportedAt: time.Now().UTC(), Version: version.Get().Version, IncludeSecrets: withSecrets,
		Network: snap.Network, DNS: snap.DNS, Groups: snap.Groups, RuleSets: snap.RuleSets, Rules: snap.Rules, DevicePolicies: snap.DevicePolicies, Bypass: snap.Bypass}
	for _, n := range snap.Nodes {
		bn := backupNode{Node: n}
		if withSecrets {
			bn.Credential = n.Credential
		}
		b.Nodes = append(b.Nodes, bn)
	}
	if b.Nodes == nil {
		b.Nodes = []backupNode{}
	}
	b.GeodataSched, _ = d.Store.GetSetting(ctx, "geodata.auto_update")
	for _, name := range []string{update.Xray, update.Mosdns} {
		if v, _ := d.Store.GetSetting(ctx, update.ScheduleKeyFor(name)); v != "" && v != "off" {
			if b.UpdateSched == nil {
				b.UpdateSched = map[string]string{}
			}
			b.UpdateSched[name] = v
		}
	}
	name := "edgeroutegw-backup-" + time.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, b)
}

// restoreBackup replaces the whole desired state with the document. Ids are
// preserved so rules keep referring to the same nodes and groups.
func (d Deps) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var b Backup
	if err := decode(r, &b); err != nil {
		d.fail(w, r, err)
		return
	}
	if b.Format != backupFormat {
		d.fail(w, r, invalid("format", "unsupported backup format %q", b.Format))
		return
	}
	nodeIDs := map[string]bool{}
	for i := range b.Nodes {
		if b.Nodes[i].ID == "" {
			b.Nodes[i].ID = uuid.NewString()
		}
		if _, err := uuid.Parse(b.Nodes[i].ID); err != nil {
			d.fail(w, r, invalid("nodes", "node id %q is not a UUID", b.Nodes[i].ID))
			return
		}
		nodeIDs[b.Nodes[i].ID] = true
	}
	groupIDs := map[string]bool{}
	for _, g := range b.Groups {
		groupIDs[g.ID] = true
		for _, m := range g.MemberIDs {
			if !nodeIDs[m] {
				d.fail(w, r, invalid("groups", "group %q references unknown node %s", g.Name, m))
				return
			}
		}
	}
	setIDs := map[string]bool{}
	for _, s := range b.RuleSets {
		setIDs[s.ID] = true
	}
	for _, rule := range b.Rules {
		if !setIDs[rule.SetID] {
			d.fail(w, r, invalid("rules", "rule %s references unknown set %s", rule.MatchValue, rule.SetID))
			return
		}
		if rule.ActionKind == model.ActionNode && !nodeIDs[rule.ActionRef] || rule.ActionKind == model.ActionGroup && !groupIDs[rule.ActionRef] {
			d.fail(w, r, invalid("rules", "rule %s references unknown target %s", rule.MatchValue, rule.ActionRef))
			return
		}
	}
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("restore backup from %s", b.ExportedAt.Format(time.RFC3339)), func(tx *sql.Tx) error {
		for _, t := range []string{"rules", "rule_sets", "outbound_group_members", "outbound_groups", "nodes", "device_policies", "bypass_destinations"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
				return err
			}
		}
		for _, n := range b.Nodes {
			var enc []byte
			if n.Credential != "" {
				var err error
				if enc, err = d.encrypt(n.Credential); err != nil {
					return err
				}
			}
			if n.Params == "" {
				n.Params = "{}"
			}
			if err := store.InsertNode(ctx, tx, store.NodeRow{Node: n.Node, CredentialEnc: enc}); err != nil {
				return fmt.Errorf("node %s: %w", n.Name, err)
			}
		}
		for _, g := range b.Groups {
			if err := store.UpsertGroup(ctx, tx, g); err != nil {
				return fmt.Errorf("group %s: %w", g.Name, err)
			}
		}
		for _, s := range b.RuleSets {
			if err := store.InsertRuleSet(ctx, tx, s.ID, s.Name); err != nil {
				return err
			}
			if !s.Enabled {
				_ = store.UpdateRuleSet(ctx, tx, s.ID, s.Name, false)
			}
		}
		for _, rule := range b.Rules {
			if err := store.InsertRule(ctx, tx, rule); err != nil {
				return fmt.Errorf("rule %s: %w", rule.MatchValue, err)
			}
		}
		for _, p := range b.DevicePolicies {
			if err := store.InsertDevicePolicy(ctx, tx, p); err != nil {
				return err
			}
		}
		for _, bp := range b.Bypass {
			if err := store.InsertBypassDestination(ctx, tx, bp); err != nil {
				return err
			}
		}
		if err := store.UpdateNetworkConfig(ctx, tx, b.Network); err != nil {
			return err
		}
		return store.UpdateDNSConfig(ctx, tx, b.DNS)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if b.GeodataSched != "" {
		_ = d.Store.SetSetting(ctx, "geodata.auto_update", b.GeodataSched)
	}
	for _, name := range []string{update.Xray, update.Mosdns} {
		if v := b.UpdateSched[name]; v != "" {
			if _, err := update.ParseSchedule(v); err == nil {
				_ = d.Store.SetSetting(ctx, update.ScheduleKeyFor(name), v)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"generation": gen, "nodes": len(b.Nodes), "rules": len(b.Rules), "secrets_restored": b.IncludeSecrets})
}
