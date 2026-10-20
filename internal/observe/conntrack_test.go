package observe

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	c, ok := Parse("2026/10/20 08:51:58.123456 from 192.168.1.10:51234 accepted tcp:www.google.com:443 [tproxy_in -> node-abc] email: x")
	if !ok || c.Source != "192.168.1.10:51234" || c.Target != "www.google.com:443" || c.Inbound != "tproxy_in" || c.Outbound != "node-abc" || !c.Accepted || c.Network != "tcp" {
		t.Fatalf("%+v %v", c, ok)
	}
	c, ok = Parse("2026/10/20 08:51:58 from 10.0.0.2:1 rejected udp:1.1.1.1:53 [tproxy_in >> group-g1]")
	if !ok || c.Accepted || c.Outbound != "group-g1" || c.Network != "udp" {
		t.Fatalf("%+v %v", c, ok)
	}
	if _, ok := Parse("garbage"); ok {
		t.Fatal("garbage accepted")
	}
}

func TestTailFollowsAppendsAndTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	if err := os.WriteFile(path, []byte("2026/10/20 08:00:00 from 1.1.1.1:1 accepted tcp:old.example:443 [tproxy_in -> direct]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := NewConnTracker(path, nil, nil)
	tr.Capacity = 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = tr.Run(ctx) }()
	time.Sleep(700 * time.Millisecond)
	if n := len(tr.Recent(0, "")); n != 0 {
		t.Fatalf("history must not be replayed, got %d", n)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	for i := 0; i < 4; i++ {
		_, _ = f.WriteString("2026/10/20 08:00:0" + string(rune('1'+i)) + " from 192.168.1.2:5 accepted tcp:a" + string(rune('a'+i)) + ".example:443 [tproxy_in -> node-x]\n")
	}
	f.Close()
	deadline := time.Now().Add(3 * time.Second)
	for len(tr.Recent(0, "")) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	got := tr.Recent(0, "")
	if len(got) != 3 || got[0].Target != "ad.example:443" || got[2].Target != "ab.example:443" {
		t.Fatalf("ring content %+v", got)
	}
	if len(tr.Recent(10, "ac.example")) != 1 {
		t.Fatal("filter failed")
	}
	// Truncate and write a new line: must be picked up from offset 0.
	if err := os.WriteFile(path, []byte("2026/10/20 08:01:00 from 2.2.2.2:2 accepted udp:new.example:53 [tproxy_in -> block]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r := tr.Recent(1, ""); len(r) == 1 && r[0].Target == "new.example:53" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("line after truncation not seen: %+v", tr.Recent(0, ""))
}
