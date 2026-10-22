package v2

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/auth"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/provision"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func (d Deps) registerProvision(r *router) {
	r.handle(http.MethodGet, "/api/v2/hosts", d.listHosts)
	r.handle(http.MethodPost, "/api/v2/hosts", d.createHost)
	r.handle(http.MethodGet, "/api/v2/hosts/{id}", d.getHost)
	r.handle(http.MethodPut, "/api/v2/hosts/{id}", d.updateHost)
	r.handle(http.MethodDelete, "/api/v2/hosts/{id}", d.deleteHost)
	r.handle(http.MethodPost, "/api/v2/hosts/{id}/check", d.checkHost)
	r.handle(http.MethodPost, "/api/v2/hosts/{id}/deploy", d.deployHost)
	r.handle(http.MethodPost, "/api/v2/hosts/{id}/hostkey", d.rePinHost)
	r.handle(http.MethodPost, "/api/v2/hosts/{id}/tune", d.tuneHost)
	r.handle(http.MethodGet, "/api/v2/deploys", d.listDeploys)
	r.handle(http.MethodGet, "/api/v2/deploys/{id}", d.getDeploy)
	r.handle(http.MethodGet, "/api/v2/deploys/{id}/logs", d.deployLogs)
	r.handle(http.MethodPost, "/api/v2/deploys/{id}/rollback", d.rollbackDeploy)
}

// HostView is a host as returned by the API (no credential).
type HostView struct {
	store.RemoteHost
	HasCredential bool                      `json:"has_credential"`
	LastDeploy    *store.RemoteDeploy       `json:"last_deploy,omitempty"`
	Tune          *provision.HostTuneReport `json:"tune,omitempty"`
}

// HostInput creates or edits a host. Credential is write-only.
type HostInput struct {
	Name       string  `json:"name"`
	SSHHost    string  `json:"ssh_host"`
	SSHPort    int     `json:"ssh_port"`
	SSHUser    string  `json:"ssh_user"`
	AuthKind   string  `json:"ssh_auth_kind"`
	Credential *string `json:"credential"`
	Region     string  `json:"region"`
	Remark     string  `json:"remark"`
}

func (in *HostInput) validate() (store.RemoteHost, error) {
	h := store.RemoteHost{Name: strings.TrimSpace(in.Name), SSHHost: strings.TrimSpace(in.SSHHost), SSHPort: in.SSHPort, SSHUser: strings.TrimSpace(in.SSHUser), SSHAuthKind: strings.ToLower(strings.TrimSpace(in.AuthKind)), Region: strings.TrimSpace(in.Region), Remark: strings.TrimSpace(in.Remark)}
	if h.Name == "" || len(h.Name) > 64 {
		return h, invalid("name", "must be 1-64 characters")
	}
	if h.SSHHost == "" || strings.ContainsAny(h.SSHHost, " /\\@") {
		return h, invalid("ssh_host", "must be a hostname or IP")
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if h.SSHPort < 1 || h.SSHPort > 65535 {
		return h, invalid("ssh_port", "must be 1-65535")
	}
	if h.SSHUser == "" {
		h.SSHUser = "root"
	}
	if h.SSHAuthKind != "password" && h.SSHAuthKind != "key" {
		return h, invalid("ssh_auth_kind", "must be password or key")
	}
	return h, nil
}

func (d Deps) hostView(r *http.Request, h store.RemoteHost) HostView {
	v := HostView{RemoteHost: h, HasCredential: len(h.CredentialEnc) > 0}
	if deploys, err := d.Store.ListRemoteDeploys(r.Context(), h.ID, 1); err == nil && len(deploys) > 0 {
		v.LastDeploy = &deploys[0]
	}
	if d.Provision != nil {
		v.Tune = d.Provision.LastTune(r.Context(), h.ID)
	}
	return v
}

// tuneHost runs the environment tuning script on the host and returns the
// report (also stored and shown on the host afterwards).
func (d Deps) tuneHost(w http.ResponseWriter, r *http.Request) {
	if d.Provision == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "provisioning is disabled", nil)
		return
	}
	rep, err := d.Provision.Tune(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		d.fail(w, r, err)
	case errors.Is(err, provision.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil && rep == nil:
		d.fail(w, r, err)
	default:
		writeJSON(w, http.StatusOK, rep)
	}
}

func (d Deps) listHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := d.Store.ListRemoteHosts(r.Context())
	if err != nil {
		d.fail(w, r, err)
		return
	}
	items := make([]HostView, 0, len(hosts))
	for _, h := range hosts {
		items = append(items, d.hostView(r, h))
	}
	writeJSON(w, http.StatusOK, listResp[HostView]{Items: items})
}

func (d Deps) getHost(w http.ResponseWriter, r *http.Request) {
	h, err := d.Store.GetRemoteHost(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemResp[HostView]{Item: d.hostView(r, h)})
}

func (d Deps) createHost(w http.ResponseWriter, r *http.Request) {
	var in HostInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	h, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if in.Credential == nil || strings.TrimSpace(*in.Credential) == "" {
		d.fail(w, r, invalid("credential", "password or private key required"))
		return
	}
	if h.CredentialEnc, err = d.encrypt(strings.TrimSpace(*in.Credential)); err != nil {
		d.fail(w, r, err)
		return
	}
	h.ID, h.Status = uuid.NewString(), "unknown"
	if err := d.Store.InsertRemoteHost(r.Context(), h); err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, itemResp[HostView]{Item: d.hostView(r, h)})
}

func (d Deps) updateHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, err := d.Store.GetRemoteHost(ctx, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	var in HostInput
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	h, err := in.validate()
	if err != nil {
		d.fail(w, r, err)
		return
	}
	h.ID = cur.ID
	if in.Credential != nil && strings.TrimSpace(*in.Credential) != "" {
		if h.CredentialEnc, err = d.encrypt(strings.TrimSpace(*in.Credential)); err != nil {
			d.fail(w, r, err)
			return
		}
	}
	if err := d.Store.UpdateRemoteHost(ctx, h); err != nil {
		d.fail(w, r, err)
		return
	}
	if h.SSHHost != cur.SSHHost {
		_ = d.Store.SetRemoteHostKey(ctx, h.ID, "") // new address, new key
	}
	saved, _ := d.Store.GetRemoteHost(ctx, h.ID)
	writeJSON(w, http.StatusOK, itemResp[HostView]{Item: d.hostView(r, saved)})
}

func (d Deps) deleteHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := d.Store.GetRemoteHost(ctx, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	cleaned := false
	if r.URL.Query().Get("cleanup") == "1" && d.Provision != nil {
		if err := d.Provision.Cleanup(ctx, h); err != nil {
			d.Log.Warn("remote cleanup failed", "host", h.Name, "err", err)
		} else {
			cleaned = true
		}
	}
	if err := d.Store.DeleteRemoteHost(ctx, h.ID); err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": 1, "remote_cleaned": cleaned})
}

func (d Deps) checkHost(w http.ResponseWriter, r *http.Request) {
	if d.Provision == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "provisioning is disabled", nil)
		return
	}
	status, err := d.Provision.Probe(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		d.fail(w, r, err)
		return
	}
	res := map[string]any{"status": status}
	if err != nil {
		res["reason"] = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

func (d Deps) deployHost(w http.ResponseWriter, r *http.Request) {
	if d.Provision == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "provisioning is disabled", nil)
		return
	}
	var in provision.DeployRequest
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind != "wg" && in.Kind != "vless" {
		d.fail(w, r, invalid("kind", "must be wg or vless"))
		return
	}
	if in.Port != 0 && (in.Port < 1 || in.Port > 65535) {
		d.fail(w, r, invalid("port", "must be 1-65535"))
		return
	}
	id, err := d.Provision.Deploy(r.Context(), r.PathValue("id"), in)
	switch {
	case errors.Is(err, provision.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		d.fail(w, r, err)
	default:
		if p, ok := auth.FromContext(r.Context()); ok {
			_ = d.Store.AddEvent(r.Context(), model.Event{Level: model.LevelInfo, Module: "provision", Type: "deploy.started", Message: fmt.Sprintf("%s deploy queued on host %s", in.Kind, r.PathValue("id")), Actor: p.Actor()})
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"deploy_id": id})
	}
}

func (d Deps) rePinHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Fingerprint string `json:"fingerprint"` // empty = forget, re-pin on next contact
	}
	if err := decode(r, &in); err != nil {
		d.fail(w, r, err)
		return
	}
	fp := strings.TrimSpace(in.Fingerprint)
	if fp != "" && !strings.HasPrefix(fp, "SHA256:") {
		d.fail(w, r, invalid("fingerprint", "must look like SHA256:..."))
		return
	}
	if err := d.Store.SetRemoteHostKey(r.Context(), r.PathValue("id"), fp); err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host_key": fp})
}

// DeployView adds redacted parameters to a deploy row.
type DeployView struct {
	store.RemoteDeploy
	Params map[string]any `json:"params,omitempty"`
}

func (d Deps) listDeploys(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListRemoteDeploys(r.Context(), r.URL.Query().Get("host_id"), queryInt(r, "limit", 100))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[store.RemoteDeploy]{Items: items})
}

func (d Deps) getDeploy(w http.ResponseWriter, r *http.Request) {
	dep, err := d.Store.GetRemoteDeploy(r.Context(), r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	v := DeployView{RemoteDeploy: dep}
	if d.Provision != nil {
		v.Params, _ = d.Provision.RedactedParams(dep)
	}
	writeJSON(w, http.StatusOK, itemResp[DeployView]{Item: v})
}

func (d Deps) deployLogs(w http.ResponseWriter, r *http.Request) {
	items, err := d.Store.ListDeployLogs(r.Context(), r.PathValue("id"), queryInt64(r, "after_id"), queryInt(r, "limit", 2000))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResp[store.DeployLogLine]{Items: items})
}

func (d Deps) rollbackDeploy(w http.ResponseWriter, r *http.Request) {
	if d.Provision == nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "provisioning is disabled", nil)
		return
	}
	id, err := d.Provision.Rollback(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, provision.ErrBusy):
		writeErr(w, http.StatusConflict, codeConflict, err.Error(), nil)
	case err != nil:
		d.fail(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"deploy_id": id})
	}
}
