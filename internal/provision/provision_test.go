package provision

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zlylong/EdgeRouteGW/internal/events"
	"github.com/zlylong/EdgeRouteGW/internal/secret"
	"github.com/zlylong/EdgeRouteGW/internal/store"
)

func newSvc(t *testing.T, dialer Dialer) (*Service, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	kr, _ := secret.FromBytes(bytes.Repeat([]byte{9}, 32))
	notified := 0
	s := &Service{Store: st, Keyring: kr, Dialer: dialer, Bus: events.New(64), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Notify: func() { notified++ }, Timeout: time.Minute, MaxConcurrent: 2, running: map[string]bool{}}
	enc, _ := kr.Encrypt("pw")
	h := store.RemoteHost{ID: "h1", Name: "SG-box", SSHHost: "203.0.113.5", SSHPort: 22, SSHUser: "root", SSHAuthKind: "password", CredentialEnc: enc, Status: "unknown"}
	if err := st.InsertRemoteHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	return s, st, h.ID
}

func waitDeploy(t *testing.T, st *store.Store, id string) store.RemoteDeploy {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := st.GetRemoteDeploy(context.Background(), id)
		if err == nil && (d.State == "succeeded" || d.State == "failed") {
			return d
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("deploy did not finish")
	return store.RemoteDeploy{}
}

func TestKeysAndScripts(t *testing.T) {
	wg, err := NewWGParams(0)
	if err != nil || wg.Port != 51820 || len(wg.ServerPriv) != 44 || wg.ServerPub == wg.ClientPub {
		t.Fatalf("wg params %+v %v", wg, err)
	}
	v, err := NewVlessParams(0, "", "")
	if err != nil || v.Port != 443 || v.ServerName != DefaultRealityDest || v.Dest != DefaultRealityDest+":443" || len(v.ShortID) != 8 || strings.Contains(v.RealityPub, "=") {
		t.Fatalf("vless params %+v %v", v, err)
	}
	ws := WGInstallScript(wg)
	if !strings.Contains(ws, "wg-quick@wg0") || strings.Contains(ws, wg.ServerPriv) {
		t.Fatal("wg script must embed the config base64, not the raw key")
	}
	vs := VlessInstallScript(v)
	for _, want := range []string{"SHA2-256=", "REALITY smoke test", "xray.service", "Xray-linux-arm64-v8a.zip"} {
		if !strings.Contains(vs, want) {
			t.Fatalf("vless script missing %q", want)
		}
	}
	if !strings.Contains(CleanupScript("vless"), "disable --now xray") {
		t.Fatal("cleanup")
	}
}

func TestDeploySuccessRegistersNodeAndPinsKey(t *testing.T) {
	gate := make(chan struct{}) // holds the first deploy open until the busy check ran
	fd := &FakeDialer{Fingerprint: "SHA256:abc", Handler: func(script string, onLine func(string, string)) error {
		onLine("stdout", "installing")
		onLine("stdout", "REALITY smoke test passed")
		<-gate
		return nil
	}}
	s, st, hostID := newSvc(t, fd)
	ctx := context.Background()
	id, err := s.Deploy(ctx, hostID, DeployRequest{Kind: "vless", Port: 8443, ServerName: "www.apple.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second deploy should be busy, got %v", err)
	}
	close(gate)
	d := waitDeploy(t, st, id)
	if d.State != "succeeded" || d.NodeID == "" {
		t.Fatalf("deploy %+v", d)
	}
	h, _ := st.GetRemoteHost(ctx, hostID)
	if h.HostKey != "SHA256:abc" || h.Status != "online" {
		t.Fatalf("host %+v", h)
	}
	n, err := st.GetNode(ctx, d.NodeID)
	if err != nil || n.Protocol != "vless" || n.Port != 8443 || n.Address != "203.0.113.5" || !strings.Contains(n.Params, `"security":"reality"`) || len(n.CredentialEnc) == 0 {
		t.Fatalf("node %+v %v", n, err)
	}
	logs, _ := st.ListDeployLogs(ctx, id, 0, 100)
	joined := ""
	for _, l := range logs {
		joined += l.Line + "\n"
	}
	if !strings.Contains(joined, "pinned host key") || !strings.Contains(joined, "smoke test passed") || !strings.Contains(joined, "node registered") {
		t.Fatalf("logs:\n%s", joined)
	}
	params, _ := s.RedactedParams(d)
	if _, leaked := params["reality_priv"]; leaked || params["uuid"] == nil {
		t.Fatalf("redacted params %v", params)
	}
	// Re-deploy reuses the same node id.
	id2, err := s.Deploy(ctx, hostID, DeployRequest{Kind: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	d2 := waitDeploy(t, st, id2)
	if d2.NodeID != d.NodeID {
		t.Fatalf("redeploy should reuse node: %s vs %s", d2.NodeID, d.NodeID)
	}
	// Rollback to the first deploy's parameters.
	id3, err := s.Rollback(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	d3 := waitDeploy(t, st, id3)
	if d3.State != "succeeded" || len(fd.Scripts) != 6 || d3.NodeID != d.NodeID { // 3 installs + 3 tuning runs
		t.Fatalf("rollback state=%s scripts=%d node=%s", d3.State, len(fd.Scripts), d3.NodeID)
	}
	p3, _ := s.RedactedParams(d3)
	if p3["port"].(float64) != 8443 || p3["uuid"] != params["uuid"] {
		t.Fatalf("rollback must reuse the original parameters: %v", p3)
	}
}

func TestDeployFailureAndHostKeyMismatch(t *testing.T) {
	fd := &FakeDialer{Fingerprint: "SHA256:new", Handler: func(_ string, onLine func(string, string)) error {
		onLine("stderr", "apt broke")
		return errors.New("exit 1")
	}}
	s, st, hostID := newSvc(t, fd)
	ctx := context.Background()
	id, _ := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	d := waitDeploy(t, st, id)
	if d.State != "failed" || !strings.Contains(d.Error, "install script") || d.NodeID != "" {
		t.Fatalf("deploy %+v", d)
	}
	h, _ := st.GetRemoteHost(ctx, hostID)
	if h.Status != "failed" {
		t.Fatalf("host status %s", h.Status)
	}
	// Pin a different key: the dial must be refused.
	_ = st.SetRemoteHostKey(ctx, hostID, "SHA256:old")
	id, _ = s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	d = waitDeploy(t, st, id)
	if d.State != "failed" || !strings.Contains(d.Error, "host key mismatch") {
		t.Fatalf("mismatch deploy %+v", d)
	}
	if status, err := s.Probe(ctx, hostID); status != "offline" || err == nil {
		t.Fatalf("probe %s %v", status, err)
	}
}

func TestDeployOfSecondKindGetsItsOwnNode(t *testing.T) {
	fd := &FakeDialer{Fingerprint: "SHA256:abc", Handler: func(script string, onLine func(string, string)) error {
		onLine("stdout", "REALITY smoke test passed")
		return nil
	}}
	s, st, hostID := newSvc(t, fd)
	ctx := context.Background()
	id1, err := s.Deploy(ctx, hostID, DeployRequest{Kind: "vless", Port: 8443, ServerName: "www.apple.com"})
	if err != nil {
		t.Fatal(err)
	}
	d1 := waitDeploy(t, st, id1)
	id2, err := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	if err != nil {
		t.Fatal(err)
	}
	d2 := waitDeploy(t, st, id2)
	if d1.State != "succeeded" || d2.State != "succeeded" {
		t.Fatalf("deploys %+v %+v", d1, d2)
	}
	if d1.NodeID == d2.NodeID {
		t.Fatal("WireGuard deploy must not overwrite the VLESS node")
	}
	nodes, _ := st.ListNodes(ctx)
	if len(nodes) != 2 {
		t.Fatalf("expected two nodes, got %d", len(nodes))
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[string(n.Protocol)] = n.Name
	}
	if names["vless"] == names["wireguard"] {
		t.Fatalf("node names collide: %v", names)
	}
	// Re-deploying the same kind reuses its node.
	id3, _ := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	if d3 := waitDeploy(t, st, id3); d3.NodeID != d2.NodeID {
		t.Fatalf("wg re-deploy should reuse node %s, got %s", d2.NodeID, d3.NodeID)
	}
}

func TestReuseSkipsNodeOverwrittenByOtherKind(t *testing.T) {
	fd := &FakeDialer{Fingerprint: "SHA256:abc", Handler: func(script string, onLine func(string, string)) error {
		onLine("stdout", "REALITY smoke test passed")
		return nil
	}}
	s, st, hostID := newSvc(t, fd)
	ctx := context.Background()
	id1, _ := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	d1 := waitDeploy(t, st, id1)
	// Simulate the pre-rc.8 damage: the wg deploy's node now holds a VLESS definition.
	row, err := st.GetNode(ctx, d1.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	row.Protocol = "vless"
	if _, err := st.Mutate(ctx, store.Mutation{Actor: "test"}, func(tx *sql.Tx) error { return store.UpdateNode(ctx, tx, row) }); err != nil {
		t.Fatal(err)
	}
	id2, _ := s.Deploy(ctx, hostID, DeployRequest{Kind: "wg"})
	d2 := waitDeploy(t, st, id2)
	if d2.State != "succeeded" || d2.NodeID == d1.NodeID {
		t.Fatalf("wg deploy must create a fresh node instead of overwriting the vless one: %+v", d2)
	}
	nodes, _ := st.ListNodes(ctx)
	if len(nodes) != 2 {
		t.Fatalf("expected two nodes, got %d", len(nodes))
	}
}

func TestTuneRunsScriptAndStoresReport(t *testing.T) {
	fd := &FakeDialer{Fingerprint: "SHA256:abc", Handler: func(script string, onLine func(string, string)) error {
		if strings.Contains(script, "TUNE_DONE") {
			onLine("stdout", "TUNE|host|info|4 CPU, kernel 6.1, kvm|")
			onLine("stdout", "TUNE|bbr|applied|已启用 bbr / fq|")
			onLine("stdout", "TUNE|nic-queues|unavailable|只有 1 个队列|在宿主机开多队列")
			onLine("stdout", "not a tune line")
			onLine("stdout", "TUNE_DONE")
			return nil
		}
		onLine("stdout", "REALITY smoke test passed")
		return nil
	}}
	s, st, hostID := newSvc(t, fd)
	ctx := context.Background()
	rep, err := s.Tune(ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 3 || rep.Items[1].Key != "bbr" || rep.Items[1].Status != "applied" || rep.Items[2].Hint != "在宿主机开多队列" || rep.Items[1].Title == "" {
		t.Fatalf("report %+v", rep)
	}
	if got := s.LastTune(ctx, hostID); got == nil || len(got.Items) != 3 {
		t.Fatalf("stored report %+v", got)
	}
	// A deploy also tunes the host and logs the items.
	id, _ := s.Deploy(ctx, hostID, DeployRequest{Kind: "vless", Port: 8443, ServerName: "www.apple.com"})
	d := waitDeploy(t, st, id)
	if d.State != "succeeded" {
		t.Fatalf("deploy %+v", d)
	}
	logs, _ := st.ListDeployLogs(ctx, id, 0, 500)
	found := false
	for _, l := range logs {
		if strings.Contains(l.Line, "tune bbr: applied") {
			found = true
		}
	}
	if !found {
		t.Fatalf("deploy log lacks tuning lines: %+v", logs)
	}
}
