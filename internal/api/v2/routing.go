package v2

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/rulespec"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// --- rule sets ---

// RuleSetInput is the create/update body.
type RuleSetInput struct {
	Name    string `json:"name"`
	Enabled *bool  `json:"enabled"`
}

func (d Deps) listRuleSets(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListRuleSets(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.RuleSet]{Items: items})
}

func (d Deps) createRuleSet(w http.ResponseWriter, r *http.Request) {
	var in RuleSetInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 {
		d.fail(w, r, invalid("name", "must be 1-64 characters"))
		return
	}
	id := uuid.NewString()
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("add rule set %q", name), func(tx *sql.Tx) error {
		if err := store.InsertRuleSet(ctx, tx, id, name); err != nil {
			return err
		}
		if in.Enabled != nil && !*in.Enabled {
			return store.UpdateRuleSet(ctx, tx, id, name, false)
		}
		return nil
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	sets, _ := d.Store.ListRuleSets(ctx)
	for _, s := range sets {
		if s.ID == id {
			writeJSON(w, http.StatusCreated, itemResp[model.RuleSet]{Item: s, Generation: gen})
			return
		}
	}
	writeJSON(w, http.StatusCreated, genResp{Generation: gen})
}

func (d Deps) updateRuleSet(w http.ResponseWriter, r *http.Request) {
	var in RuleSetInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	sets, err := d.Store.ListRuleSets(ctx)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var cur *model.RuleSet
	for i := range sets {
		if sets[i].ID == id {
			cur = &sets[i]
		}
	}
	if cur == nil {
		d.fail(w, r, store.ErrNotFound)
		return
	}
	name := cur.Name
	if strings.TrimSpace(in.Name) != "" {
		name = strings.TrimSpace(in.Name)
	}
	if len(name) > 64 {
		d.fail(w, r, invalid("name", "must be 1-64 characters"))
		return
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	gen, err := d.mutate(ctx, r, fmt.Sprintf("update rule set %q", name), func(tx *sql.Tx) error {
		return store.UpdateRuleSet(ctx, tx, id, name, enabled)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	cur.Name, cur.Enabled = name, enabled
	writeJSON(w, http.StatusOK, itemResp[model.RuleSet]{Item: *cur, Generation: gen})
}

func (d Deps) deleteRuleSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "delete rule set "+id, func(tx *sql.Tx) error {
		return store.DeleteRuleSet(ctx, tx, id)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1})
}

type reorderInput struct {
	IDs []string `json:"ids"`
}

func (d Deps) reorderRuleSets(w http.ResponseWriter, r *http.Request) {
	var in reorderInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, "reorder rule sets", func(tx *sql.Tx) error {
		if err := store.ReorderRuleSets(ctx, tx, in.IDs); err != nil {
			return invalid("ids", "%v", err)
		}
		return nil
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen})
}

func (d Deps) reorderRules(w http.ResponseWriter, r *http.Request) {
	var in reorderInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	setID := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "reorder rules in set "+setID, func(tx *sql.Tx) error {
		if err := store.ReorderRules(ctx, tx, setID, in.IDs); err != nil {
			return invalid("ids", "%v", err)
		}
		return nil
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen})
}

// --- rules ---

// RuleInput is the create/update body. For create, a comma/newline separated
// MatchValue with match_kind=domain expands into one rule per value.
type RuleInput struct {
	SetID      string `json:"set_id"`
	MatchKind  string `json:"match_kind"`
	MatchValue string `json:"match_value"`
	ActionKind string `json:"action_kind"`
	ActionRef  string `json:"action_ref"`
	Enabled    *bool  `json:"enabled"`
	Remark     string `json:"remark"`
}

// validateRule normalises one rule and checks references against the store.
func (d Deps) validateRule(r *http.Request, in RuleInput, value string) (model.Rule, error) {
	ctx := r.Context()
	rule := model.Rule{
		SetID:      strings.TrimSpace(in.SetID),
		MatchKind:  model.MatchKind(strings.ToLower(strings.TrimSpace(in.MatchKind))),
		ActionKind: model.ActionKind(strings.ToLower(strings.TrimSpace(in.ActionKind))),
		ActionRef:  strings.TrimSpace(in.ActionRef),
		Enabled:    true,
		Remark:     strings.TrimSpace(in.Remark),
	}
	if in.Enabled != nil {
		rule.Enabled = *in.Enabled
	}
	if len(rule.Remark) > 200 {
		return rule, invalid("remark", "at most 200 characters")
	}
	net, err := d.Store.NetworkConfig(ctx)
	if err != nil {
		return rule, err
	}
	switch rule.MatchKind {
	case model.MatchDomain:
		p, err := rulespec.ParseDomain(value)
		if err != nil {
			return rule, invalid("match_value", "%v", err)
		}
		if p.Wildcard && net.Mode != model.ModeA {
			return rule, invalid("match_value", "wildcard domains (*.x, **.x) are only supported in Mode A; Mode %s publishes routes per resolved domain", net.Mode)
		}
		rule.MatchValue = p.Raw
	case model.MatchIP:
		v, err := rulespec.NormalizeIPOrCIDR(value)
		if err != nil {
			return rule, invalid("match_value", "%v", err)
		}
		rule.MatchValue = v
	case model.MatchGeosite, model.MatchGeoIP:
		v, err := rulespec.NormalizeGeoTag(value)
		if err != nil {
			return rule, invalid("match_value", "%v", err)
		}
		rule.MatchValue = v
	default:
		return rule, invalid("match_kind", "must be domain, ip, geosite or geoip")
	}
	switch rule.ActionKind {
	case model.ActionDirect, model.ActionProxy, model.ActionBlock:
		if rule.ActionRef != "" {
			return rule, invalid("action_ref", "must be empty for %s", rule.ActionKind)
		}
	case model.ActionNode:
		if _, err := d.Store.GetNode(ctx, rule.ActionRef); err != nil {
			return rule, invalid("action_ref", "node %q does not exist", rule.ActionRef)
		}
	case model.ActionGroup:
		if _, err := d.Store.GetGroup(ctx, rule.ActionRef); err != nil {
			return rule, invalid("action_ref", "group %q does not exist", rule.ActionRef)
		}
	default:
		return rule, invalid("action_kind", "must be direct, proxy, block, node or group")
	}
	return rule, nil
}

func splitValues(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' || r == ' ' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (d Deps) listRules(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListRules(r.Context(), r.URL.Query().Get("set_id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.Rule]{Items: items})
}

func (d Deps) createRules(w http.ResponseWriter, r *http.Request) {
	var in RuleInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	values := splitValues(in.MatchValue)
	if len(values) == 0 {
		d.fail(w, r, invalid("match_value", "required"))
		return
	}
	if len(values) > 500 {
		d.fail(w, r, invalid("match_value", "at most 500 values per request"))
		return
	}
	if strings.TrimSpace(in.SetID) == "" {
		d.fail(w, r, invalid("set_id", "required"))
		return
	}
	rules := make([]model.Rule, 0, len(values))
	for _, v := range values {
		rule, err := d.validateRule(r, in, v)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		rule.ID = uuid.NewString()
		rules = append(rules, rule)
	}
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("add %d rule(s) to set %s", len(rules), in.SetID), func(tx *sql.Tx) error {
		for _, rule := range rules {
			if err := store.InsertRule(ctx, tx, rule); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	saved, _ := d.Store.ListRules(ctx, in.SetID)
	ids := map[string]bool{}
	for _, rule := range rules {
		ids[rule.ID] = true
	}
	items := make([]model.Rule, 0, len(rules))
	for _, s := range saved {
		if ids[s.ID] {
			items = append(items, s)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"items": items, "generation": gen})
}

func (d Deps) updateRule(w http.ResponseWriter, r *http.Request) {
	var in RuleInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	rule, err := d.validateRule(r, in, in.MatchValue)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	rule.ID = r.PathValue("id")
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, "update rule "+rule.ID, func(tx *sql.Tx) error {
		return store.UpdateRule(ctx, tx, rule)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[model.Rule]{Item: rule, Generation: gen})
}

func (d Deps) deleteRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "delete rule "+id, func(tx *sql.Tx) error {
		return store.DeleteRule(ctx, tx, id)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1})
}

func (d Deps) moveRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SetID string `json:"set_id"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, fmt.Sprintf("move rule %s to set %s", id, in.SetID), func(tx *sql.Tx) error {
		return store.MoveRule(ctx, tx, id, strings.TrimSpace(in.SetID))
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen})
}

// --- device policies ---

// DevicePolicyInput is the create/update body.
type DevicePolicyInput struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Action string `json:"action"`
	Remark string `json:"remark"`
}

func (in *DevicePolicyInput) validate() (model.DevicePolicy, error) {
	p := model.DevicePolicy{Kind: model.DevicePolicyKind(strings.ToLower(strings.TrimSpace(in.Kind))), Action: model.DeviceAction(strings.ToLower(strings.TrimSpace(in.Action))), Remark: strings.TrimSpace(in.Remark)}
	switch p.Kind {
	case model.DeviceMAC:
		v, err := rulespec.NormalizeMAC(in.Value)
		if err != nil {
			return p, invalid("value", "%v", err)
		}
		p.Value = v
	case model.DeviceIP, model.DeviceCIDR:
		v, err := rulespec.NormalizeIPOrCIDR(in.Value)
		if err != nil {
			return p, invalid("value", "%v", err)
		}
		p.Value = v
		if strings.HasSuffix(v, "/32") || strings.HasSuffix(v, "/128") {
			p.Kind = model.DeviceIP
		} else {
			p.Kind = model.DeviceCIDR
		}
	default:
		return p, invalid("kind", "must be mac, ip or cidr")
	}
	switch p.Action {
	case model.DeviceProxy, model.DeviceDirect, model.DeviceReject:
	default:
		return p, invalid("action", "must be proxy, direct or reject")
	}
	if len(p.Remark) > 200 {
		return p, invalid("remark", "at most 200 characters")
	}
	return p, nil
}

func (d Deps) listDevicePolicies(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListDevicePolicies(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.DevicePolicy]{Items: items})
}

func (d Deps) createDevicePolicy(w http.ResponseWriter, r *http.Request) {
	var in DevicePolicyInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	p, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	p.ID = uuid.NewString()
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("add device policy %s %s", p.Value, p.Action), func(tx *sql.Tx) error {
		return store.InsertDevicePolicy(ctx, tx, p)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, itemResp[model.DevicePolicy]{Item: p, Generation: gen})
}

func (d Deps) updateDevicePolicy(w http.ResponseWriter, r *http.Request) {
	var in DevicePolicyInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	p, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	p.ID = r.PathValue("id")
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, "update device policy "+p.ID, func(tx *sql.Tx) error {
		return store.UpdateDevicePolicy(ctx, tx, p)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[model.DevicePolicy]{Item: p, Generation: gen})
}

func (d Deps) deleteDevicePolicy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "delete device policy "+id, func(tx *sql.Tx) error {
		return store.DeleteDevicePolicy(ctx, tx, id)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1})
}

// --- bypass destinations ---

// BypassInput is the create body.
type BypassInput struct {
	CIDR   string `json:"cidr"`
	Remark string `json:"remark"`
}

func (d Deps) listBypass(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListBypassDestinations(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.BypassDestination]{Items: items})
}

func (d Deps) createBypass(w http.ResponseWriter, r *http.Request) {
	var in BypassInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	v, err := rulespec.NormalizeIPOrCIDR(in.CIDR)
	if err != nil {
		d.fail(w, r, invalid("cidr", "%v", err))
		return
	}
	if rulespec.IsIPv6Prefix(v) {
		d.fail(w, r, invalid("cidr", "bypass destinations are IPv4 only"))
		return
	}
	b := model.BypassDestination{ID: uuid.NewString(), CIDR: v, Remark: strings.TrimSpace(in.Remark)}
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, "add bypass destination "+v, func(tx *sql.Tx) error {
		return store.InsertBypassDestination(ctx, tx, b)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, itemResp[model.BypassDestination]{Item: b, Generation: gen})
}

func (d Deps) deleteBypass(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "delete bypass destination "+id, func(tx *sql.Tx) error {
		return store.DeleteBypassDestination(ctx, tx, id)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1})
}
