package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustContain(t *testing.T, conf string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(conf, w) {
			t.Errorf("missing %q in:\n%s", w, conf)
		}
	}
}

func mustNotContain(t *testing.T, conf string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(conf, b) {
			t.Errorf("unexpected %q in:\n%s", b, conf)
		}
	}
}

func TestAuthGate(t *testing.T) {
	if !isGated(map[string]string{}, "app."+domain) {
		t.Error("default should be gated")
	}
	if isGated(map[string]string{"proxy.auth": "false"}, "app."+domain) {
		t.Error("proxy.auth=false should be public")
	}
	if isGated(map[string]string{}, authHost) {
		t.Error("auth host must never be gated")
	}

	gated, _ := renderHostConf(confData{Name: "app", IP: "172.18.0.9", Port: "80", Hostname: "app." + domain, Gated: true})
	mustContain(t, gated,
		"auth_request /_sir_auth;",
		"auth_request_set $sir_auth_email $upstream_http_x_auth_email;",
		"error_page 401 = @sir_login;",
		"proxy_set_header X-Auth-Email $sir_auth_email;",
		"location = /_sir_auth {",
		"internal;",
		"resolver 127.0.0.11",
		"set $sir_auth_upstream "+authUpstream+";",
		"proxy_pass $sir_auth_upstream/session/verify;",
		"proxy_pass_request_body off;",
		"return 302 https://"+authHost+"/login?rd=https://$host$request_uri;",
		`proxy_set_header Connection "upgrade";`,
		"proxy_set_header Authorization $http_authorization;",
		"proxy_set_header X-Original-Host $host;",
		"proxy_set_header X-Original-Method $request_method;",
		"proxy_set_header X-Real-IP $sir_client_ip;",
		"if ($sir_pat) {",
		`add_header WWW-Authenticate 'Bearer realm="sir-labs"' always;`,
		"default_type application/json;",
		`return 401 '{"error":"invalid_token"}\n';`,
		"proxy_set_header Authorization $sir_backend_auth;",
	)
	mustNotContain(t, gated, "/session/verify {")

	public, _ := renderHostConf(confData{Name: "pub", IP: "172.18.0.10", Port: "80", Hostname: "pub." + domain, Gated: false})
	mustContain(t, public, `proxy_set_header X-Auth-Email "";`, `proxy_set_header X-Auth-Role "";`, "proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Authorization $sir_backend_auth;")
	mustNotContain(t, public, "auth_request", "_sir_auth", "sir-auth", "/session/verify")

	authConf, _ := renderHostConf(confData{Name: "sir-auth", IP: "172.18.0.11", Port: "8080", Hostname: authHost, Gated: isGated(nil, authHost)})
	mustNotContain(t, authConf, "auth_request")
	mustContain(t, authConf, "location = /session/verify {\n        return 404;", "proxy_set_header Authorization $sir_backend_auth;")

	internal := renderInternalConf(internalHost, []internalSvc{
		{Name: "priv", Port: "3001", Gated: true},
		{Name: "open", Port: "3002", Gated: false},
	})
	mustContain(t, internal, "location = /_sir_auth {", "location @sir_login {")
	idx := strings.Index(internal, "location = / {")
	priv := strings.Index(internal, "location /3001/ {")
	open := strings.Index(internal, "location /3002/ {")
	if idx < 0 || priv < 0 || open < 0 {
		t.Fatalf("missing locations:\n%s", internal)
	}
	mustContain(t, internal[idx:priv], "auth_request /_sir_auth;")
	mustContain(t, internal[priv:open], "auth_request /_sir_auth;", "proxy_set_header X-Auth-Email $sir_auth_email;", "proxy_set_header Authorization $sir_backend_auth;")
	mustNotContain(t, internal[open:], "auth_request")
	mustContain(t, internal[open:], `proxy_set_header X-Auth-Email "";`, "proxy_set_header Authorization $sir_backend_auth;")
	mustContain(t, internal, "proxy_set_header X-Real-IP $sir_client_ip;", "if ($sir_pat) {")

	t.Run("kill switch", func(t *testing.T) {
		authEnabled = false
		defer func() { authEnabled = true }()
		conf, _ := renderHostConf(confData{Name: "app", IP: "1.2.3.4", Port: "80",
			Hostname: "app." + domain, Gated: isGated(nil, "app."+domain)})
		mustNotContain(t, conf, "auth_request", "_sir_auth")
		mustContain(t, conf, "proxy_set_header Authorization $sir_backend_auth;")
		assertBaseConfs(t)
		mustNotContain(t, renderInternalConf(internalHost,
			[]internalSvc{{Name: "x", Port: "3001", Gated: isGated(nil, internalHost)}}),
			"auth_request", "_sir_auth")
	})

	assertBaseConfs(t)

	// DUMP_DIR lets the rendered confs be validated with a real `nginx -t`.
	if dir := os.Getenv("DUMP_DIR"); dir != "" {
		writeBaseConfs(dir)
		for name, c := range map[string]string{"app.conf": gated, "pub.conf": public, "sir-auth.conf": authConf, "internal.conf": internal} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(c), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// assertBaseConfs checks the maps file is rewritten even when nothing is gated
// and stale confs are cleaned up.
func assertBaseConfs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "stale.conf"), []byte("x"), 0644) //nolint:errcheck
	writeBaseConfs(dir)
	if _, err := os.Stat(filepath.Join(dir, "stale.conf")); err == nil {
		t.Error("stale.conf not removed")
	}
	maps, err := os.ReadFile(filepath.Join(dir, "00-sir-auth.conf"))
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, string(maps),
		"map $http_authorization $sir_pat {",
		`"~*^Bearer\s+sirpat_" 1;`,
		"map $http_authorization $sir_backend_auth {",
		"default $http_authorization;",
		"map $http_cf_connecting_ip $sir_client_ip {",
		`"" $remote_addr;`,
	)
	if _, err := os.Stat(filepath.Join(dir, "default.conf")); err != nil {
		t.Error(err)
	}
}
