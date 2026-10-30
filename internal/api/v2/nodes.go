package v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/observe"
	"github.com/zlylong/EdgeRouteGW/internal/sharelink"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// NodeView is a node as the API returns it: the credential is replaced by a
// presence flag.
type NodeView struct {
	model.Node
	HasCredential bool `json:"has_credential"`
	// Probe is the latest end-to-end check (nil before the first round).
	Probe *observe.LatencyResult `json:"probe,omitempty"`
}

func viewNode(n store.NodeRow) NodeView {
	return NodeView{Node: n.Node, HasCredential: len(n.CredentialEnc) > 0}
}

// NodeInput is the create/update body. Credential is write-only: omit it on
// update to keep the stored one, send "" to clear it.
type NodeInput struct {
	Name       string  `json:"name"`
	Protocol   string  `json:"protocol"`
	Address    string  `json:"address"`
	Port       int     `json:"port"`
	Params     string  `json:"params"`
	Enabled    *bool   `json:"enabled"`
	Credential *string `json:"credential"`
}

var protocols = map[model.Protocol]bool{
	model.ProtocolVMess: true, model.ProtocolVLESS: true, model.ProtocolTrojan: true, model.ProtocolShadowsocks: true,
	model.ProtocolSocks: true, model.ProtocolHTTP: true, model.ProtocolWireGuard: true, model.ProtocolCustom: true, model.ProtocolHysteria: true,
}

// credentialRequired lists protocols that cannot work without a secret.
var credentialRequired = map[model.Protocol]bool{
	model.ProtocolVMess: true, model.ProtocolVLESS: true, model.ProtocolTrojan: true, model.ProtocolShadowsocks: true, model.ProtocolWireGuard: true, model.ProtocolHysteria: true,
}

func (in *NodeInput) validate(hasCredential bool) (model.Node, error) {
	n := model.Node{
		Name:     strings.TrimSpace(in.Name),
		Protocol: model.Protocol(strings.ToLower(strings.TrimSpace(in.Protocol))),
		Address:  strings.TrimSpace(in.Address),
		Port:     in.Port,
		Params:   strings.TrimSpace(in.Params),
		Enabled:  true,
	}
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if n.Name == "" || len(n.Name) > 64 {
		return n, invalid("name", "must be 1-64 characters")
	}
	if !protocols[n.Protocol] {
		return n, invalid("protocol", "unsupported protocol %q", in.Protocol)
	}
	if n.Address == "" || strings.ContainsAny(n.Address, " /\\") {
		return n, invalid("address", "must be a hostname or IP")
	}
	if n.Port < 1 || n.Port > 65535 {
		return n, invalid("port", "must be 1-65535")
	}
	if n.Params == "" {
		n.Params = "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(n.Params), &obj); err != nil || obj == nil {
		return n, invalid("params", "must be a JSON object")
	}
	if n.Protocol == model.ProtocolCustom || n.Protocol == model.ProtocolWireGuard {
		if _, ok := obj["settings"].(map[string]any); !ok {
			return n, invalid("params", "%s nodes need params.settings", n.Protocol)
		}
	}
	if credentialRequired[n.Protocol] && !hasCredential {
		return n, invalid("credential", "%s nodes need a credential", n.Protocol)
	}
	return n, nil
}

func (d Deps) encrypt(cred string) ([]byte, error) {
	if d.Keyring == nil {
		return []byte(cred), nil
	}
	return d.Keyring.Encrypt(cred)
}

func (d Deps) listNodes(w http.ResponseWriter, r *http.Request) {
	rows, err := d.Store.ListNodes(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	items := make([]NodeView, 0, len(rows))
	var probes map[string]observe.LatencyResult
	if d.Latency != nil {
		probes = d.Latency.Results()
	}
	for _, n := range rows {
		v := viewNode(n)
		if pr, ok := probes[n.ID]; ok {
			pr := pr
			v.Probe = &pr
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, listResp[NodeView]{Items: items})
}

func (d Deps) getNode(w http.ResponseWriter, r *http.Request) {
	n, err := d.Store.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[NodeView]{Item: viewNode(n)})
}

func (d Deps) createNode(w http.ResponseWriter, r *http.Request) {
	var in NodeInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	cred := ""
	if in.Credential != nil {
		cred = strings.TrimSpace(*in.Credential)
	}
	n, err := in.validate(cred != "")
	if err != nil {
		d.fail(w, r, err)
		return
	}
	enc, err := d.encrypt(cred)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	n.ID = uuid.NewString()
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("add node %q", n.Name), func(tx *sql.Tx) error {
		return store.InsertNode(ctx, tx, store.NodeRow{Node: n, CredentialEnc: enc})
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	row, _ := d.Store.GetNode(ctx, n.ID)
	writeJSON(w, http.StatusCreated, itemResp[NodeView]{Item: viewNode(row), Generation: gen})
}

func (d Deps) updateNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	existing, err := d.Store.GetNode(ctx, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var in NodeInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	var enc []byte // nil = keep
	hasCred := len(existing.CredentialEnc) > 0
	if in.Credential != nil {
		c := strings.TrimSpace(*in.Credential)
		hasCred = c != ""
		if enc, err = d.encrypt(c); err != nil {
			d.fail(w, r, err)
			return
		}
		if enc == nil {
			enc = []byte{} // explicit clear
		}
	}
	n, err := in.validate(hasCred)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	n.ID = existing.ID
	gen, err := d.mutate(ctx, r, fmt.Sprintf("update node %q", n.Name), func(tx *sql.Tx) error {
		return store.UpdateNode(ctx, tx, store.NodeRow{Node: n, CredentialEnc: enc})
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	row, _ := d.Store.GetNode(ctx, n.ID)
	writeJSON(w, http.StatusOK, itemResp[NodeView]{Item: viewNode(row), Generation: gen})
}

func (d Deps) deleteNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var retargeted int64
	gen, err := d.mutate(ctx, r, "delete node "+id, func(tx *sql.Tx) (err error) {
		retargeted, err = store.DeleteNode(ctx, tx, id)
		return err
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1, Retargeted: retargeted})
}

func (d Deps) setDefaultNode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, "set default node "+id, func(tx *sql.Tx) error {
		return store.SetDefaultNode(ctx, tx, id)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen})
}

func (d Deps) setNodeEnabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	gen, err := d.mutate(ctx, r, fmt.Sprintf("node %s enabled=%v", id, in.Enabled), func(tx *sql.Tx) error {
		return store.SetNodeEnabled(ctx, tx, id, in.Enabled)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen})
}

// ImportInput carries share links; with dry_run the parsed nodes are returned
// without being saved so the UI can show a preview.
type ImportInput struct {
	Links  []string `json:"links"`
	DryRun bool     `json:"dry_run"`
}

// ImportResult reports one link's outcome.
type ImportResult struct {
	Link  string    `json:"link"`
	Node  *NodeView `json:"node,omitempty"`
	Error string    `json:"error,omitempty"`
}

func (d Deps) importNodes(w http.ResponseWriter, r *http.Request) {
	var in ImportInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	if len(in.Links) == 0 || len(in.Links) > 200 {
		d.fail(w, r, invalid("links", "provide 1-200 links"))
		return
	}
	results := make([]ImportResult, 0, len(in.Links))
	var rows []store.NodeRow
	for _, link := range in.Links {
		res := ImportResult{Link: redactLink(link)}
		p, err := sharelink.Parse(link)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		enc, err := d.encrypt(p.Credential)
		if err != nil {
			d.fail(w, r, err)
			return
		}
		p.Node.ID = uuid.NewString()
		row := store.NodeRow{Node: p.Node, CredentialEnc: enc}
		v := viewNode(row)
		res.Node = &v
		results = append(results, res)
		rows = append(rows, row)
	}
	if in.DryRun || len(rows) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "imported": 0, "dry_run": in.DryRun})
		return
	}
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("import %d node(s)", len(rows)), func(tx *sql.Tx) error {
		for _, row := range rows {
			if err := store.InsertNode(ctx, tx, row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"results": results, "imported": len(rows), "generation": gen})
}

// redactLink hides the credential part of a share link in responses.
func redactLink(link string) string {
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return link
	}
	if at := strings.LastIndex(rest, "@"); at > 0 {
		return scheme + "://***@" + rest[at+1:]
	}
	if len(rest) > 12 {
		return scheme + "://" + rest[:8] + "…"
	}
	return scheme + "://…"
}

// --- groups ---

// GroupInput is the create/update body for an outbound group.
type GroupInput struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	MemberIDs []string `json:"member_ids"`
	Strategy  string   `json:"strategy"`
}

func (in *GroupInput) validate() (model.OutboundGroup, error) {
	g := model.OutboundGroup{Name: strings.TrimSpace(in.Name), Kind: model.GroupKind(strings.ToLower(strings.TrimSpace(in.Kind))), Strategy: strings.TrimSpace(in.Strategy)}
	if g.Name == "" || len(g.Name) > 64 {
		return g, invalid("name", "must be 1-64 characters")
	}
	if g.Kind != model.GroupFailover && g.Kind != model.GroupLoadBalance {
		return g, invalid("kind", "must be failover or loadbalance")
	}
	if len(in.MemberIDs) < 1 {
		return g, invalid("member_ids", "at least one member")
	}
	seen := map[string]bool{}
	for _, id := range in.MemberIDs {
		if id == "" || seen[id] {
			return g, invalid("member_ids", "ids must be non-empty and unique")
		}
		seen[id] = true
	}
	g.MemberIDs = in.MemberIDs
	if g.Strategy == "" {
		g.Strategy = "{}"
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(g.Strategy), &obj); err != nil || obj == nil {
		return g, invalid("strategy", "must be a JSON object")
	}
	return g, nil
}

func (d Deps) listGroups(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListGroups(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[model.OutboundGroup]{Items: items})
}

func (d Deps) getGroup(w http.ResponseWriter, r *http.Request) {
	g, err := d.Store.GetGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[model.OutboundGroup]{Item: g})
}

func (d Deps) createGroup(w http.ResponseWriter, r *http.Request) {
	var in GroupInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	g, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	g.ID = uuid.NewString()
	ctx := r.Context()
	gen, err := d.mutate(ctx, r, fmt.Sprintf("add group %q", g.Name), func(tx *sql.Tx) error {
		return store.UpsertGroup(ctx, tx, g)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	saved, _ := d.Store.GetGroup(ctx, g.ID)
	writeJSON(w, http.StatusCreated, itemResp[model.OutboundGroup]{Item: saved, Generation: gen})
}

func (d Deps) updateGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := d.Store.GetGroup(ctx, r.PathValue("id")); err != nil {
		d.fail(w, r, err)
		return
	}
	var in GroupInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	g, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	g.ID = r.PathValue("id")
	gen, err := d.mutate(ctx, r, fmt.Sprintf("update group %q", g.Name), func(tx *sql.Tx) error {
		return store.UpsertGroup(ctx, tx, g)
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	saved, _ := d.Store.GetGroup(ctx, g.ID)
	writeJSON(w, http.StatusOK, itemResp[model.OutboundGroup]{Item: saved, Generation: gen})
}

func (d Deps) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var retargeted int64
	gen, err := d.mutate(ctx, r, "delete group "+id, func(tx *sql.Tx) (err error) {
		retargeted, err = store.DeleteGroup(ctx, tx, id)
		return err
	})
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, genResp{Generation: gen, Deleted: 1, Retargeted: retargeted})
}
