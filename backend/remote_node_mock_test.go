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

	oldConnect := remoteConnect
	defer func() { remoteConnect = oldConnect }()

	remoteConnect = func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			if cmd != "systemctl is-active xray" {
				t.Fatalf("unexpected command: %s", cmd)
			}
			return "active\n", "", nil
		}}, nil
	}

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

	oldConnect := remoteConnect
	defer func() { remoteConnect = oldConnect }()

	remoteConnect = func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return nil, errors.New("boom")
	}

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

	oldConnect := remoteConnect
	defer func() { remoteConnect = oldConnect }()

	remoteConnect = func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			return "apt stdout", "apt stderr", fmt.Errorf("Process exited with status 100")
		}}, nil
	}

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

func TestDoDeployRoutine_AutoUpdatesFingerprintAndRetries(t *testing.T) {
	setupFeatureSuiteRouter(t)

	oldConnect := remoteConnect
	defer func() { remoteConnect = oldConnect }()

	newFP := "SHA256:wo1Wuz3UhufbAdlhwnCKYyvQlYmcLeLBQVq3tkq1PRo"
	calls := 0
	remoteConnect = func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		calls++
		if calls == 1 {
			if expectedHostKey != "SHA256:old" {
				t.Fatalf("first connect expected old key, got %s", expectedHostKey)
			}
			return nil, fmt.Errorf("failed to dial: ssh: handshake failed: Strict Host Key checking failed. The server's fingerprint is %s. Please update", newFP)
		}
		if expectedHostKey != newFP {
			t.Fatalf("second connect expected new key, got %s", expectedHostKey)
		}
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			return "", "", nil
		}}, nil
	}

	req := RemoteNodeReq{
		Name:          "192.168.20.152",
		Type:          "vless",
		SSHHost:       "192.168.20.152",
		SSHPort:       22,
		SSHUser:       "root",
		SSHHostKey:    "SHA256:old",
		SSHAuthType:   "password",
		SSHCredential: "secret123",
		Region:        "lab",
		Remark:        "seed",
	}

	doDeployRoutine(2, req, true, nil)

	if calls != 2 {
		t.Fatalf("expected 2 connect attempts, got %d", calls)
	}

	var status, hostKey string
	if err := db.QueryRow("SELECT status, ssh_host_key FROM remote_nodes WHERE id = 2").Scan(&status, &hostKey); err != nil {
		t.Fatal(err)
	}
	if status != "Online" {
		t.Fatalf("want status Online got %s", status)
	}
	if hostKey != newFP {
		t.Fatalf("want updated hostkey %s got %s", newFP, hostKey)
	}

	var hasAutoUpdateLog int
	if err := db.QueryRow("SELECT COUNT(*) FROM remote_node_logs WHERE node_id=2 AND log_text LIKE '%Auto-updated SSH host fingerprint%'").Scan(&hasAutoUpdateLog); err != nil {
		t.Fatal(err)
	}
	if hasAutoUpdateLog == 0 {
		t.Fatal("expected auto-update fingerprint log entry")
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
