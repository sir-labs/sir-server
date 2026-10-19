package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaimSurvivesContainerLifecycle(t *testing.T) {
	s := &managedSnapshot{Claims: []domainClaim{{Host: "demo.sir-labs.com", Kind: "repository", Project: "demo", Service: "app"}}}
	if !nativeClaimAllowed(s, "demo.sir-labs.com", map[string]string{"com.docker.compose.project": "demo", "com.docker.compose.service": "app"}) {
		t.Fatal("owner refused")
	}
	if nativeClaimAllowed(s, "demo.sir-labs.com", map[string]string{"com.docker.compose.project": "other", "com.docker.compose.service": "app"}) {
		t.Fatal("claim stolen")
	}
	if nativeClaimAllowed(s, "unclaimed.sir-labs.com", nil) {
		t.Fatal("unregistered domain")
	}
}
func TestManagedConfigConflictAndValidation(t *testing.T) {
	s := &managedSnapshot{Token: strings.Repeat("a", 64), Routes: []managedRoute{{Host: "demo.sir-labs.com", ID: "11111111-1111-1111-1111-111111111111"}}}
	if appendManaged(s, map[string]string{}, map[string]route{}, map[string]string{"demo.sir-labs.com": "legacy"}) == nil {
		t.Fatal("duplicate accepted")
	}
	configs := map[string]string{}
	if err := appendManaged(s, configs, map[string]route{}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	for _, v := range configs {
		if !strings.Contains(v, "host.docker.internal:18081") || !strings.Contains(v, "proxy_buffering off") {
			t.Fatal(v)
		}
	}
	s.Routes[0].Host = "evil; injected"
	if appendManaged(s, configs, map[string]route{}, map[string]string{}) == nil {
		t.Fatal("injection accepted")
	}
}
func TestRestoreConfiguration(t *testing.T) {
	old := confDir
	confDir = t.TempDir()
	defer func() { confDir = old }()
	p := filepath.Join(confDir, "old.conf")
	os.WriteFile(p, []byte("old"), 0600)
	saved := backupConfigs()
	os.WriteFile(p, []byte("bad"), 0600)
	os.WriteFile(filepath.Join(confDir, "new.conf"), []byte("new"), 0600)
	restoreConfigs(saved)
	b, _ := os.ReadFile(p)
	if string(b) != "old" {
		t.Fatal("not restored")
	}
	if _, err := os.Stat(filepath.Join(confDir, "new.conf")); err == nil {
		t.Fatal("new config retained")
	}
}
