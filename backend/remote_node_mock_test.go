package main

import (
	"errors"
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
