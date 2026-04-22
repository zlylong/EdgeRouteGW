package main

import (
	"errors"
	"testing"
)

func TestApplyOspfAddBatch_DoesNotMarkPublishedOnVtyshFailure(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('8.8.8.8', 'google.com', 'static', datetime('now'), datetime('now'), 300, 'candidate', 0)"); err != nil {
		t.Fatal(err)
	}

	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) {
		return "apply failed", errors.New("vtysh failed")
	}
	defer func() { runVtyshConfigBatch = oldRunner }()

	if applyOspfAddBatch([]string{"8.8.8.8"}) {
		t.Fatal("expected add batch failure")
	}

	var status string
	if err := db.QueryRow("SELECT status FROM routes_table WHERE ip='8.8.8.8'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "candidate" {
		t.Fatalf("unexpected status after failed add: %s", status)
	}
}

func TestApplyOspfAddBatch_MarksPublishedOnSuccess(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('1.1.1.1', 'cloudflare.com', 'static', datetime('now'), datetime('now'), 300, 'candidate', 0)"); err != nil {
		t.Fatal(err)
	}

	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) {
		return "ok", nil
	}
	defer func() { runVtyshConfigBatch = oldRunner }()

	if !applyOspfAddBatch([]string{"1.1.1.1"}) {
		t.Fatal("expected add batch success")
	}

	var status string
	if err := db.QueryRow("SELECT status FROM routes_table WHERE ip='1.1.1.1'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "published" {
		t.Fatalf("unexpected status after successful add: %s", status)
	}
}

func TestApplyOspfDeleteBatch_DoesNotDeleteOnVtyshFailure(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('9.9.9.9', 'quad9.net', 'static', datetime('now'), datetime('now'), 300, 'published', 3)"); err != nil {
		t.Fatal(err)
	}

	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) {
		return "apply failed", errors.New("vtysh failed")
	}
	defer func() { runVtyshConfigBatch = oldRunner }()

	if applyOspfDeleteBatch([]string{"9.9.9.9"}) {
		t.Fatal("expected delete batch failure")
	}

	var cnt int
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE ip='9.9.9.9'").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("route unexpectedly deleted on failure, count=%d", cnt)
	}
}

func TestApplyOspfDeleteBatch_DeletesOnSuccess(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES ('4.4.4.4', 'example.net', 'static', datetime('now'), datetime('now'), 300, 'published', 3)"); err != nil {
		t.Fatal(err)
	}

	oldRunner := runVtyshConfigBatch
	runVtyshConfigBatch = func(config string) (string, error) {
		return "ok", nil
	}
	defer func() { runVtyshConfigBatch = oldRunner }()

	if !applyOspfDeleteBatch([]string{"4.4.4.4"}) {
		t.Fatal("expected delete batch success")
	}

	var cnt int
	if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE ip='4.4.4.4'").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("route not deleted after success, count=%d", cnt)
	}
}
