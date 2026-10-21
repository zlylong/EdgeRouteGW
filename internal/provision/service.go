package provision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/model"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

// Bus event types.
const (
	EventDeployLog   events.Type = "provision.log"
	EventDeployState events.Type = "provision.state"
	EventHostStatus  events.Type = "provision.host"
)

// Service runs deployments.
type Service struct {
	Store   *store.Store
	Keyring *secret.Keyring
	Dialer  Dialer
	Bus     *events.Bus
	Log     *slog.Logger
	// Notify wakes the reconciler after a node is created.
	Notify func()
	// Timeout bounds one install script.
	Timeout time.Duration
	// MaxConcurrent bounds parallel deploys across hosts.
	MaxConcurrent int

	mu      sync.Mutex
	running map[string]bool // host id -> deploying
	sem     chan struct{}
}

// New builds a service with the SSH dialer.
func New(st *store.Store, kr *secret.Keyring, bus *events.Bus, notify func(), log *slog.Logger) *Service {
	return &Service{Store: st, Keyring: kr, Dialer: SSHDialer{Timeout: 15 * time.Second}, Bus: bus, Log: log, Notify: notify, Timeout: 10 * time.Minute, MaxConcurrent: 2, running: map[string]bool{}}
}

// DeployRequest describes what to put on a host.
type DeployRequest struct {
	Kind       string `json:"kind"` // wg | vless
	Port       int    `json:"port"`
	ServerName string `json:"server_name"` // vless: REALITY SNI
	Dest       string `json:"dest"`        // vless: camouflage dest host:port
	// NodeName names the outbound node created on success; defaults to the
	// host name.
	NodeName string `json:"node_name"`
}

// ErrBusy means the host already has a deploy in flight.
var ErrBusy = errors.New("a deploy is already running for this host")

func (s *Service) target(h store.RemoteHost) (Target, error) {
	cred, err := s.Keyring.Decrypt(h.CredentialEnc)
	if err != nil {
		return Target{}, fmt.Errorf("decrypt credential: %w", err)
	}
	return Target{Host: h.SSHHost, Port: h.SSHPort, User: h.SSHUser, AuthKind: h.SSHAuthKind, Credential: cred, HostKey: h.HostKey}, nil
}

// Probe connects, pins the host key on first contact, and runs the check
// script of the host's last successful deploy (or a trivial command).
func (s *Service) Probe(ctx context.Context, hostID string) (status string, err error) {
	h, err := s.Store.GetRemoteHost(ctx, hostID)
	if err != nil {
		return "", err
	}
	t, err := s.target(h)
	if err != nil {
		return "", err
	}
	sess, fp, err := s.Dialer.Dial(ctx, t)
	if err != nil {
		_ = s.Store.SetRemoteHostStatus(ctx, hostID, "offline")
		s.publishHost(hostID, "offline")
		return "offline", err
	}
	defer sess.Close()
	if h.HostKey == "" && fp != "" {
		_ = s.Store.SetRemoteHostKey(ctx, hostID, fp)
	}
	kind := ""
	if deploys, err := s.Store.ListRemoteDeploys(ctx, hostID, 20); err == nil {
		for _, d := range deploys {
			if d.State == "succeeded" {
				kind = d.Kind
				break
			}
		}
	}
	var out []string
	runErr := sess.Run(ctx, CheckScript(kind), 20*time.Second, func(_, line string) { out = append(out, line) })
	status = "online"
	if kind != "" && runErr != nil {
		status = "failed"
		err = fmt.Errorf("service check failed: %v (%s)", runErr, strings.Join(out, " | "))
	}
	_ = s.Store.SetRemoteHostStatus(ctx, hostID, status)
	s.publishHost(hostID, status)
	return status, err
}

func (s *Service) publishHost(id, status string) {
	if s.Bus != nil {
		s.Bus.Publish(EventHostStatus, map[string]any{"host_id": id, "status": status})
	}
}

// Deploy queues a deployment and runs it in the background. It returns the
// deploy id immediately.
func (s *Service) Deploy(ctx context.Context, hostID string, req DeployRequest) (string, error) {
	h, err := s.Store.GetRemoteHost(ctx, hostID)
	if err != nil {
		return "", err
	}
	var params any
	switch req.Kind {
	case "wg":
		p, err := NewWGParams(req.Port)
		if err != nil {
			return "", err
		}
		if err := validPort(p.Port); err != nil {
			return "", err
		}
		params = p
	case "vless":
		p, err := NewVlessParams(req.Port, strings.TrimSpace(req.ServerName), strings.TrimSpace(req.Dest))
		if err != nil {
			return "", err
		}
		if err := validPort(p.Port); err != nil {
			return "", err
		}
		params = p
	default:
		return "", fmt.Errorf("unsupported deploy kind %q", req.Kind)
	}
	return s.enqueue(ctx, h, req.Kind, params, req.NodeName)
}

// Rollback re-deploys the parameters of a previous successful deploy.
func (s *Service) Rollback(ctx context.Context, deployID string) (string, error) {
	d, err := s.Store.GetRemoteDeploy(ctx, deployID)
	if err != nil {
		return "", err
	}
	if d.State != "succeeded" && d.State != "rolled_back" {
		return "", fmt.Errorf("deploy %s is %s; only successful deploys can be restored", deployID, d.State)
	}
	h, err := s.Store.GetRemoteHost(ctx, d.HostID)
	if err != nil {
		return "", err
	}
	raw, err := s.Keyring.Decrypt(d.ParamsEnc)
	if err != nil {
		return "", err
	}
	var params any
	switch d.Kind {
	case "wg":
		var p WGParams
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return "", err
		}
		params = p
	case "vless":
		var p VlessParams
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return "", err
		}
		params = p
	}
	return s.enqueue(ctx, h, d.Kind, params, "")
}

func (s *Service) enqueue(ctx context.Context, h store.RemoteHost, kind string, params any, nodeName string) (string, error) {
	s.mu.Lock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[h.ID] {
		s.mu.Unlock()
		return "", ErrBusy
	}
	s.running[h.ID] = true
	if s.sem == nil {
		n := s.MaxConcurrent
		if n <= 0 {
			n = 2
		}
		s.sem = make(chan struct{}, n)
	}
	s.mu.Unlock()

	raw, _ := json.Marshal(params)
	enc, err := s.Keyring.Encrypt(string(raw))
	if err != nil {
		s.release(h.ID)
		return "", err
	}
	d := store.RemoteDeploy{ID: uuid.NewString(), HostID: h.ID, Kind: kind, ParamsEnc: enc, State: "queued"}
	if err := s.Store.InsertRemoteDeploy(ctx, d); err != nil {
		s.release(h.ID)
		return "", err
	}
	_ = s.Store.SetRemoteHostStatus(ctx, h.ID, "deploying")
	s.publishHost(h.ID, "deploying")
	if nodeName == "" {
		nodeName = h.Name
	}
	go s.run(context.Background(), h, d, params, nodeName)
	return d.ID, nil
}

func (s *Service) release(hostID string) {
	s.mu.Lock()
	delete(s.running, hostID)
	s.mu.Unlock()
}

func (s *Service) logLine(ctx context.Context, deployID, stream, line string) {
	id, _ := s.Store.AppendDeployLog(ctx, deployID, stream, line)
	if s.Bus != nil {
		s.Bus.Publish(EventDeployLog, map[string]any{"deploy_id": deployID, "id": id, "stream": stream, "line": line, "at": time.Now().UTC()})
	}
}

func (s *Service) setState(ctx context.Context, d store.RemoteDeploy, state, errMsg string) {
	_ = s.Store.SetRemoteDeployState(ctx, d.ID, state, errMsg)
	if s.Bus != nil {
		s.Bus.Publish(EventDeployState, map[string]any{"deploy_id": d.ID, "host_id": d.HostID, "state": state, "error": errMsg})
	}
}

func (s *Service) run(ctx context.Context, h store.RemoteHost, d store.RemoteDeploy, params any, nodeName string) {
	defer s.release(h.ID)
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	fail := func(err error) {
		s.Log.Warn("deploy failed", "host", h.Name, "deploy", d.ID, "err", err)
		s.logLine(ctx, d.ID, "system", "FAILED: "+err.Error())
		s.setState(ctx, d, "failed", err.Error())
		_ = s.Store.SetRemoteHostStatus(ctx, h.ID, "failed")
		s.publishHost(h.ID, "failed")
		_ = s.Store.AddEvent(ctx, model.Event{Level: model.LevelError, Module: "provision", Type: "deploy.failed", Message: fmt.Sprintf("deploy %s on %s failed: %v", d.Kind, h.Name, err)})
	}

	s.setState(ctx, d, "running", "")
	s.logLine(ctx, d.ID, "system", fmt.Sprintf("connecting to %s@%s:%d", h.SSHUser, h.SSHHost, h.SSHPort))
	t, err := s.target(h)
	if err != nil {
		fail(err)
		return
	}
	sess, fp, err := s.Dialer.Dial(ctx, t)
	if err != nil {
		if errors.Is(err, ErrHostKeyMismatch) {
			s.logLine(ctx, d.ID, "system", "host key changed; re-pin it from the host page if this is expected")
		}
		fail(err)
		return
	}
	defer sess.Close()
	if h.HostKey == "" && fp != "" {
		_ = s.Store.SetRemoteHostKey(ctx, h.ID, fp)
		s.logLine(ctx, d.ID, "system", "pinned host key "+fp)
	}

	var script string
	switch p := params.(type) {
	case WGParams:
		script = WGInstallScript(p)
	case VlessParams:
		script = VlessInstallScript(p)
	}
	s.logLine(ctx, d.ID, "system", "running install script ("+d.Kind+")")
	if err := sess.Run(ctx, script, s.Timeout, func(stream, line string) { s.logLine(ctx, d.ID, stream, line) }); err != nil {
		fail(fmt.Errorf("install script: %w", err))
		return
	}

	nodeID, err := s.registerNode(ctx, h, d, params, nodeName)
	if err != nil {
		fail(fmt.Errorf("register node: %w", err))
		return
	}
	s.logLine(ctx, d.ID, "system", "outbound node registered: "+nodeID)
	s.setState(ctx, d, "succeeded", "")
	_ = s.Store.SetRemoteHostStatus(ctx, h.ID, "online")
	s.publishHost(h.ID, "online")
	_ = s.Store.AddEvent(ctx, model.Event{Level: model.LevelInfo, Module: "provision", Type: "deploy.succeeded", Message: fmt.Sprintf("%s deployed on %s", d.Kind, h.Name)})
	if s.Notify != nil {
		s.Notify()
	}
}

// registerNode creates (or updates) the outbound node for a deploy and marks
// older deploys of the same host as superseded.
func (s *Service) registerNode(ctx context.Context, h store.RemoteHost, d store.RemoteDeploy, params any, nodeName string) (string, error) {
	var n model.Node
	var cred string
	switch p := params.(type) {
	case WGParams:
		settings := map[string]any{
			"address": []string{p.ClientAddr},
			"mtu":     p.MTU,
			"peers":   []any{map[string]any{"endpoint": fmt.Sprintf("%s:%d", h.SSHHost, p.Port), "publicKey": p.ServerPub, "allowedIPs": []string{"0.0.0.0/0", "::/0"}}},
		}
		raw, _ := json.Marshal(map[string]any{"settings": settings})
		n = model.Node{Name: nodeName, Protocol: model.ProtocolWireGuard, Address: h.SSHHost, Port: p.Port, Params: string(raw), Enabled: true}
		cred = p.ClientPriv
	case VlessParams:
		raw, _ := json.Marshal(map[string]any{"type": "tcp", "security": "reality", "sni": p.ServerName, "pbk": p.RealityPub, "sid": p.ShortID, "fp": "chrome", "flow": "xtls-rprx-vision"})
		n = model.Node{Name: nodeName, Protocol: model.ProtocolVLESS, Address: h.SSHHost, Port: p.Port, Params: string(raw), Enabled: true}
		cred = p.UUID
	default:
		return "", fmt.Errorf("unknown params")
	}
	enc, err := s.Keyring.Encrypt(cred)
	if err != nil {
		return "", err
	}
	// Reuse the node of the previous successful deploy of the same kind on
	// this host so rules pointing at it keep working across re-deploys and
	// rollbacks. A different kind (WireGuard next to VLESS) gets its own
	// node: both services can run on one host at the same time.
	existing := ""
	if deploys, err := s.Store.ListRemoteDeploys(ctx, h.ID, 50); err == nil {
		for _, old := range deploys {
			if old.ID != d.ID && old.NodeID != "" && old.Kind == d.Kind {
				// The node must still be of this kind: builds before rc.8 let
				// a deploy of another kind overwrite it, leaving records of
				// both kinds pointing at one node.
				if row, err := s.Store.GetNode(ctx, old.NodeID); err == nil && row.Protocol == n.Protocol {
					existing = old.NodeID
					break
				}
			}
		}
	}
	if existing == "" {
		// A second kind on the same host must not collide on the default
		// name (the host's name).
		if nodes, err := s.Store.ListNodes(ctx); err == nil {
			for _, other := range nodes {
				if other.Name == n.Name && other.Protocol != n.Protocol {
					label := map[string]string{"wg": "WireGuard", "vless": "VLESS"}[d.Kind]
					n.Name = fmt.Sprintf("%s (%s)", n.Name, label)
					break
				}
			}
		}
	}
	summary := fmt.Sprintf("register node for %s deploy on %s", d.Kind, h.Name)
	if existing != "" {
		n.ID = existing
		_, err = s.Store.Mutate(ctx, store.Mutation{Actor: "provision", Summary: summary}, func(tx *sql.Tx) error {
			return store.UpdateNode(ctx, tx, store.NodeRow{Node: n, CredentialEnc: enc})
		})
	} else {
		n.ID = uuid.NewString()
		_, err = s.Store.Mutate(ctx, store.Mutation{Actor: "provision", Summary: summary}, func(tx *sql.Tx) error {
			return store.InsertNode(ctx, tx, store.NodeRow{Node: n, CredentialEnc: enc})
		})
	}
	if err != nil {
		return "", err
	}
	return n.ID, s.Store.LinkDeployNode(ctx, d.ID, n.ID)
}

// Cleanup removes the deployed service from a host (best effort, used on
// host deletion).
func (s *Service) Cleanup(ctx context.Context, h store.RemoteHost) error {
	t, err := s.target(h)
	if err != nil {
		return err
	}
	sess, _, err := s.Dialer.Dial(ctx, t)
	if err != nil {
		return err
	}
	defer sess.Close()
	deploys, _ := s.Store.ListRemoteDeploys(ctx, h.ID, 50)
	kinds := map[string]bool{}
	for _, d := range deploys {
		if d.State == "succeeded" {
			kinds[d.Kind] = true
		}
	}
	for k := range kinds {
		if err := sess.Run(ctx, CleanupScript(k), time.Minute, func(string, string) {}); err != nil {
			return err
		}
	}
	return nil
}

// RedactedParams returns the deploy parameters without private keys, for
// the UI's history view.
func (s *Service) RedactedParams(d store.RemoteDeploy) (map[string]any, error) {
	raw, err := s.Keyring.Decrypt(d.ParamsEnc)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	for _, k := range []string{"server_priv", "client_priv", "reality_priv"} {
		delete(m, k)
	}
	return m, nil
}
