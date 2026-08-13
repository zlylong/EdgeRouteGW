package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSSHClient struct {
	run func(cmd string) (string, string, error)
}

func (f *fakeSSHClient) RunCommand(cmd string) (string, string, error) {
	if f.run != nil {
		return f.run(cmd)
	}
	return "", "", nil
}

func (f *fakeSSHClient) Close() error { return nil }

func TestCreateAndDeployRemoteNode_StoresNodeAndStartsAsyncDeploy(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldStarter := startDeployRoutine
	defer func() { startDeployRoutine = oldStarter }()

	var called bool
	var gotID int64
	var gotReq RemoteNodeReq
	var gotIsUpdate bool
	startDeployRoutine = func(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
		called = true
		gotID = id
		gotReq = req
		gotIsUpdate = isUpdate
	}

	body := `{"name":"mock-node","type":"vless","ssh_host":"10.0.0.9","ssh_port":22,"ssh_user":"root","ssh_auth_type":"password","ssh_credential":"secret123","ssh_host_key":"SHA256:test","region":"lab","remark":"seed"}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !called || gotID == 0 || gotReq.SSHHost != "10.0.0.9" || gotIsUpdate {
		t.Fatalf("unexpected starter call: called=%v id=%d req=%+v update=%v", called, gotID, gotReq, gotIsUpdate)
	}
}

func TestCheckRemoteNode_UsesMockSSHConnector(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()

	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			if cmd != "systemctl is-active xray" {
				t.Fatalf("unexpected command: %s", cmd)
			}
			return "active\n", "", nil
		}}, nil
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/remote_nodes/2/check"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"Online"`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestRegenerateRemoteNodeParams_ArchivesHistoryAndStartsMockDeploy(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldStarter := startDeployRoutine
	defer func() { startDeployRoutine = oldStarter }()

	var called bool
	var gotIsUpdate bool
	var gotParams map[string]interface{}
	startDeployRoutine = func(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
		called = true
		gotIsUpdate = isUpdate
		gotParams = params
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/remote_nodes/2/regenerate"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !called || !gotIsUpdate || gotParams != nil {
		t.Fatalf("unexpected starter call: called=%v isUpdate=%v params=%v", called, gotIsUpdate, gotParams)
	}
}

func TestRollbackRemoteNode_LoadsHistoryAndStartsMockDeploy(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldStarter := startDeployRoutine
	defer func() { startDeployRoutine = oldStarter }()

	var called bool
	var gotIsUpdate bool
	var gotParams map[string]interface{}
	startDeployRoutine = func(id int64, req RemoteNodeReq, isUpdate bool, params map[string]interface{}) {
		called = true
		gotIsUpdate = isUpdate
		gotParams = params
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedJSONRequest(http.MethodPost, "/api/remote_nodes/2/rollback", `{"history_id":1}`))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !called || !gotIsUpdate {
		t.Fatalf("unexpected starter call: called=%v isUpdate=%v", called, gotIsUpdate)
	}
	if gotParams == nil {
		t.Fatal("expected rollback params")
	}
	if gotParams["port"] == nil {
		t.Fatalf("expected historical port in params: %v", gotParams)
	}
}

func TestCheckRemoteNode_OfflineWhenConnectorFails(t *testing.T) {
	r := setupFeatureSuiteRouter(t)

	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()

	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return nil, errors.New("boom")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authedRequest(http.MethodPost, "/api/remote_nodes/2/check"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestDoDeployRoutine_LogsRemoteStdoutAndStderrOnFailure(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()

	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			return "apt stdout", "apt stderr", fmt.Errorf("Process exited with status 100")
		}}, nil
	})

	req := RemoteNodeReq{
		Name:          "192.168.20.152",
		Type:          "vless",
		SSHHost:       "192.168.20.152",
		SSHPort:       22,
		SSHUser:       "root",
		SSHHostKey:    "SHA256:test",
		SSHAuthType:   "password",
		SSHCredential: "secret123",
		Region:        "lab",
		Remark:        "seed",
	}

	doDeployRoutine(2, req, true, nil)

	var status string
	if err := db.QueryRow("SELECT status FROM remote_nodes WHERE id = 2").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "Failed" {
		t.Fatalf("want status Failed got %s", status)
	}

	var logText string
	if err := db.QueryRow("SELECT log_text FROM remote_node_logs WHERE node_id = 2 ORDER BY id DESC LIMIT 1").Scan(&logText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Process exited with status 100", "apt stdout", "apt stderr"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in log text: %s", want, logText)
		}
	}
}

func TestDoDeployRoutine_RefusesToAutoTrustRotatedFingerprint(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldConnect := getRemoteConnect()
	defer func() { setRemoteConnect(oldConnect) }()

	newFP := "SHA256:wo1Wuz3UhufbAdlhwnCKYyvQlYmcLeLBQVq3tkq1PRo"
	calls := 0
	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		calls++
		if expectedHostKey != "SHA256:test" {
			t.Fatalf("expected pinned key, got %s", expectedHostKey)
		}
		return nil, fmt.Errorf("failed to dial: ssh: handshake failed: Strict Host Key checking failed. The server's fingerprint is %s. Please update", newFP)
	})

	req := RemoteNodeReq{
		Name:          "192.168.20.152",
		Type:          "vless",
		SSHHost:       "192.168.20.152",
		SSHPort:       22,
		SSHUser:       "root",
		SSHHostKey:    "SHA256:test",
		SSHAuthType:   "password",
		SSHCredential: "secret123",
		Region:        "lab",
		Remark:        "seed",
	}

	doDeployRoutine(2, req, true, nil)

	if calls != 1 {
		t.Fatalf("expected a single connect attempt (no auto-trust retry), got %d", calls)
	}

	var status, hostKey string
	if err := db.QueryRow("SELECT status, ssh_host_key FROM remote_nodes WHERE id = 2").Scan(&status, &hostKey); err != nil {
		t.Fatal(err)
	}
	if status != "Failed" {
		t.Fatalf("want status Failed got %s", status)
	}
	if hostKey != "SHA256:test" {
		t.Fatalf("stored host key must not be silently rotated, got %s", hostKey)
	}

	var refusalLog int
	if err := db.QueryRow("SELECT COUNT(*) FROM remote_node_logs WHERE node_id=2 AND log_text LIKE '%refusing to auto-trust rotated SSH host key%'").Scan(&refusalLog); err != nil {
		t.Fatal(err)
	}
	if refusalLog == 0 {
		t.Fatal("expected refusal log entry")
	}
}

func TestWrapRemoteCommandWithSudo_NonRootUsesSudo(t *testing.T) {
	req := RemoteNodeReq{SSHUser: "ubuntu"}
	got := wrapRemoteCommandWithSudo(req, "systemctl is-active xray", false)
	if !strings.HasPrefix(got, "sudo -n bash -lc ") {
		t.Fatalf("expected sudo wrapper, got: %s", got)
	}
}

func TestWrapRemoteCommandWithSudo_RootNoWrap(t *testing.T) {
	req := RemoteNodeReq{SSHUser: "root"}
	got := wrapRemoteCommandWithSudo(req, "systemctl is-active xray", false)
	if got != "systemctl is-active xray" {
		t.Fatalf("expected raw command, got: %s", got)
	}
}

func TestPinInitialHostKeyIsConditional(t *testing.T) {
	setupFeatureSuiteRouter(t)
	repo := NewRemoteNodesRepository()

	// Node 2 is seeded with a pinned key; the conditional pin must not overwrite it.
	pinned, err := repo.PinInitialHostKey(2, "SHA256:SHOULD-NOT-APPLY")
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Fatal("pin must be rejected when a key is already pinned")
	}
	var k string
	if err := db.QueryRow("SELECT ssh_host_key FROM remote_nodes WHERE id=2").Scan(&k); err != nil {
		t.Fatal(err)
	}
	if k != "SHA256:test" {
		t.Fatalf("stored key was overwritten: %s", k)
	}

	// Clear the key; the conditional pin should now succeed and store the value.
	if _, err := db.Exec("UPDATE remote_nodes SET ssh_host_key='' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	pinned, err = repo.PinInitialHostKey(2, "SHA256:newfp")
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("pin should succeed when no key is pinned")
	}
	if err := db.QueryRow("SELECT ssh_host_key FROM remote_nodes WHERE id=2").Scan(&k); err != nil {
		t.Fatal(err)
	}
	if k != "SHA256:newfp" {
		t.Fatalf("pin not stored, got %s", k)
	}
}

func postJSON(target, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestUpdateRemoteNodeHostKeyEndpoint(t *testing.T) {
	r := setupFeatureSuiteRouter(t)
	validFP := "SHA256:ADjw2yeU9EmUjcrBrwreHH7cJLe3lNRiPHFhTu3PPio"

	w := httptest.NewRecorder()
	r.ServeHTTP(w, postJSON("/api/remote_nodes/2/hostkey", `{"ssh_host_key":"garbage"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid fingerprint: want 400 got %d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, postJSON("/api/remote_nodes/2/hostkey", `{"ssh_host_key":"`+validFP+`"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("valid update: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var k string
	if err := db.QueryRow("SELECT ssh_host_key FROM remote_nodes WHERE id=2").Scan(&k); err != nil {
		t.Fatal(err)
	}
	if k != validFP {
		t.Fatalf("host key not updated, got %s", k)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, postJSON("/api/remote_nodes/999/hostkey", `{"ssh_host_key":"`+validFP+`"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing node: want 404 got %d", w.Code)
	}
}
