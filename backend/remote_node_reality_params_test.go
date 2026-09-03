package main

import (
	"strings"
	"testing"
)

func realityDeployReq() RemoteNodeReq {
	return RemoteNodeReq{
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
}

// stubSuccessfulSSH installs a connector whose RunCommand always succeeds and
// records every command it was handed.
func stubSuccessfulSSH(t *testing.T, ran *[]string) {
	t.Helper()
	oldConnect := getRemoteConnect()
	t.Cleanup(func() { setRemoteConnect(oldConnect) })
	setRemoteConnect(func(host string, port int, user string, authType string, credential string, expectedHostKey string) (remoteSSHClient, error) {
		return &fakeSSHClient{run: func(cmd string) (string, string, error) {
			*ran = append(*ran, cmd)
			return "", "", nil
		}}, nil
	})
}

func TestDoDeployRoutine_VlessDefaultsToPort443(t *testing.T) {
	setupFeatureSuiteRouter(t)
	var ran []string
	stubSuccessfulSSH(t, &ran)

	doDeployRoutine(2, realityDeployReq(), true, nil)

	var port int
	var serverName, dest string
	if err := db.QueryRow("SELECT port, server_name, dest FROM remote_node_vless WHERE node_id = 2").
		Scan(&port, &serverName, &dest); err != nil {
		t.Fatal(err)
	}
	// A REALITY inbound on a random high port contradicts its own cover story,
	// so a deploy that names no port must land on 443 rather than on whatever
	// the port allocator happened to pick.
	if port != 443 {
		t.Errorf("default REALITY port = %d, want 443", port)
	}
	if serverName != "www.microsoft.com" {
		t.Errorf("default serverName = %q, want www.microsoft.com", serverName)
	}
	if dest != "www.microsoft.com:443" {
		t.Errorf("default dest = %q, want www.microsoft.com:443", dest)
	}
}

func TestDoDeployRoutine_VlessHonoursExplicitOverrides(t *testing.T) {
	setupFeatureSuiteRouter(t)
	var ran []string
	stubSuccessfulSSH(t, &ran)

	req := realityDeployReq()
	req.Port = 8443
	req.ServerName = "dl.google.com"
	req.Dest = "dl.google.com:443"

	doDeployRoutine(2, req, true, nil)

	var port int
	var serverName, dest, shareLink string
	if err := db.QueryRow("SELECT port, server_name, dest, share_link FROM remote_node_vless WHERE node_id = 2").
		Scan(&port, &serverName, &dest, &shareLink); err != nil {
		t.Fatal(err)
	}
	if port != 8443 {
		t.Errorf("port = %d, want 8443 (explicit override)", port)
	}
	if serverName != "dl.google.com" {
		t.Errorf("serverName = %q, want dl.google.com", serverName)
	}
	if dest != "dl.google.com:443" {
		t.Errorf("dest = %q, want dl.google.com:443", dest)
	}
	if !strings.Contains(shareLink, "sni=dl.google.com") {
		t.Errorf("share link does not carry the overridden sni: %s", shareLink)
	}
}

func TestDoDeployRoutine_VlessRejectsMalformedParamsBeforeRunningScript(t *testing.T) {
	cases := []struct {
		label      string
		serverName string
		dest       string
		port       int
	}{
		{"serverName with inner space", "www.micro soft.com", "www.microsoft.com:443", 443},
		{"serverName with port", "www.microsoft.com:443", "www.microsoft.com:443", 443},
		{"serverName as IP", "93.184.216.34", "www.microsoft.com:443", 443},
		{"bare-label serverName", "localhost", "www.microsoft.com:443", 443},
		{"dest without port", "www.microsoft.com", "www.microsoft.com", 443},
		{"dest with bad port", "www.microsoft.com", "www.microsoft.com:0", 443},
		{"port out of range", "www.microsoft.com", "www.microsoft.com:443", 70000},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			setupFeatureSuiteRouter(t)
			var ran []string
			stubSuccessfulSSH(t, &ran)

			req := realityDeployReq()
			req.Port = tc.port
			req.ServerName = tc.serverName
			req.Dest = tc.dest

			doDeployRoutine(2, req, true, nil)

			// The install script is what carries these values onto the remote
			// host as root, so rejection has to happen before it is run.
			if len(ran) != 0 {
				t.Errorf("install script ran despite invalid params: %v", ran)
			}

			var status string
			if err := db.QueryRow("SELECT status FROM remote_nodes WHERE id = 2").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "Failed" {
				t.Errorf("status = %s, want Failed", status)
			}

			var logText string
			if err := db.QueryRow("SELECT log_text FROM remote_node_logs WHERE node_id = 2 ORDER BY id DESC LIMIT 1").
				Scan(&logText); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logText, "Invalid REALITY parameters") {
				t.Errorf("log does not explain the rejection: %s", logText)
			}
		})
	}
}

func TestDoDeployRoutine_VlessShareLinkEscapesParameters(t *testing.T) {
	setupFeatureSuiteRouter(t)
	var ran []string
	stubSuccessfulSSH(t, &ran)

	doDeployRoutine(2, realityDeployReq(), true, nil)

	var shareLink string
	if err := db.QueryRow("SELECT share_link FROM remote_node_vless WHERE node_id = 2").Scan(&shareLink); err != nil {
		t.Fatal(err)
	}
	// The query string ends at the fragment; every parameter before it must be
	// a single well-formed key=value pair.
	query := shareLink
	if i := strings.Index(query, "#"); i >= 0 {
		query = query[:i]
	}
	i := strings.Index(query, "?")
	if i < 0 {
		t.Fatalf("share link has no query string: %s", shareLink)
	}
	for _, pair := range strings.Split(query[i+1:], "&") {
		if strings.Count(pair, "=") != 1 {
			t.Errorf("malformed share link parameter %q in %s", pair, shareLink)
		}
	}
}

func TestDoDeployRoutine_VlessTreatsBlankOverridesAsUnset(t *testing.T) {
	setupFeatureSuiteRouter(t)
	var ran []string
	stubSuccessfulSSH(t, &ran)

	req := realityDeployReq()
	req.ServerName = "   "
	req.Dest = "  "

	doDeployRoutine(2, req, true, nil)

	var serverName, dest string
	if err := db.QueryRow("SELECT server_name, dest FROM remote_node_vless WHERE node_id = 2").
		Scan(&serverName, &dest); err != nil {
		t.Fatal(err)
	}
	// A form field the operator left blank must fall back to the default rather
	// than deploy a serverName no SNI can ever match.
	if serverName != "www.microsoft.com" || dest != "www.microsoft.com:443" {
		t.Errorf("blank overrides gave serverName=%q dest=%q, want the defaults", serverName, dest)
	}
}
