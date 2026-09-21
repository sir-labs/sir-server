package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusDistinguishesRunningFromHealthy(t *testing.T) {
	for _, tt := range []struct{ state, health, want string }{{"running", "none", "unknown"}, {"running", "healthy", "healthy"}, {"running", "unhealthy", "degraded"}, {"running", "starting", "starting"}, {"exited", "healthy", "down"}, {"restarting", "healthy", "down"}} {
		got, _ := nodeStatus(tt.state, tt.health)
		if got != tt.want {
			t.Errorf("%+v got %s", tt, got)
		}
	}
}
func TestMetricRatesAndCounterReset(t *testing.T) {
	start := time.Now()
	prev := previousStat{at: start, cpu: 100, system: 1000, rx: 100, tx: 200}
	cur := previousStat{at: start.Add(10 * time.Second), cpu: 200, system: 2000, rx: 1100, tx: 2200}
	cpu, rx, tx := statRates(prev, cur, 4)
	if cpu == nil || *cpu != 40 || rx == nil || *rx != 100 || tx == nil || *tx != 200 {
		t.Fatal("wrong rates")
	}
	cpu, rx, tx = statRates(cur, prev, 4)
	if cpu != nil || rx != nil || tx != nil {
		t.Fatal("counter resets must be unknown, not negative")
	}
}

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func useProbe(t *testing.T, f fakeTransport) {
	t.Helper()
	old := probeClient
	probeClient = &http.Client{Transport: f}
	t.Cleanup(func() { probeClient = old })
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func tokenFixture(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("test-monitor-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_TOKEN_FILE", p)
}
func TestApplicationProbesCatchHealthyContainerWithBrokenAuth(t *testing.T) {
	tokenFixture(t)
	useProbe(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/health" {
			return response(200, `{"status":"ok"}`), nil
		}
		return response(401, `{"error":"unauthorized"}`), nil
	})
	checks := runServiceChecks(context.Background())
	for _, c := range checks {
		want := "failed"
		if c.ID == "auth" {
			want = "passed"
		}
		if c.Status != want {
			t.Errorf("%s = %s", c.ID, c.Status)
		}
	}
	b, _ := json.Marshal(checks)
	if strings.Contains(string(b), "test-monitor-secret") {
		t.Fatal("credential leak")
	}
}
func TestProbesRequireRealMCPResponse(t *testing.T) {
	tokenFixture(t)
	useProbe(t, func(r *http.Request) (*http.Response, error) { return response(200, `<html>Sign in</html>`), nil })
	for _, c := range runServiceChecks(context.Background()) {
		if c.Status != "failed" {
			t.Fatal("login page incorrectly counted as successful MCP")
		}
	}
}
func TestMissingCredentialNeverClaimsPassedPath(t *testing.T) {
	t.Setenv("MONITOR_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	useProbe(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/health" {
			t.Error("unexpected credentialless probe")
		}
		return response(200, `{"status":"ok"}`), nil
	})
	for _, c := range runServiceChecks(context.Background()) {
		if c.ID != "auth" && c.Status != "unknown" {
			t.Fatal("missing token should be unknown")
		}
	}
}
func TestSuccessfulProbeChain(t *testing.T) {
	tokenFixture(t)
	useProbe(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/health" || r.URL.Path == "/healthz" {
			return response(200, `{"status":"ok"}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer test-monitor-secret" {
			t.Error("missing probe authorization")
		}
		var payload struct{ Method string }
		json.NewDecoder(r.Body).Decode(&payload)
		if payload.Method == "initialize" {
			return response(200, `{"result":{"serverInfo":{"name":"sir-ocr"}}}`), nil
		}
		return response(200, `{"result":{"tools":[{"name":"ocr_list"}]}}`), nil
	})
	for _, c := range runServiceChecks(context.Background()) {
		if c.Status != "passed" {
			t.Errorf("%s: %s", c.ID, c.Detail)
		}
	}
}
func TestStatusEndpointPreservesStaleTimestamp(t *testing.T) {
	m := &monitor{snapshot: monitorSnapshot{SampledAt: "2020-01-01T00:00:00Z", Error: "Docker unavailable", Nodes: []serviceNode{{Name: "worker", Status: "down"}}}}
	w := httptest.NewRecorder()
	m.serve(w, httptest.NewRequest("GET", "/api/status", nil))
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status must not be cached")
	}
	var s monitorSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.SampledAt != "2020-01-01T00:00:00Z" || s.Nodes[0].Status != "down" {
		t.Fatal("lost stale/down evidence")
	}
}
func TestMonitorRoutesAndLegacyRoutes(t *testing.T) {
	for _, path := range []string{"/", "/routes", "/routes/view", "/api/status", "/healthz"} {
		w := httptest.NewRecorder()
		serveHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Errorf("%s failed", path)
		}
	}
	w := httptest.NewRecorder()
	serveHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	if w.Code != 404 {
		t.Fatal("unknown route must be 404")
	}
}
func TestHostMetricsRejectStaleSamples(t *testing.T) {
	useProbe(t, func(r *http.Request) (*http.Response, error) {
		return response(200, `{"labels":["time","used"],"data":[[1,999]]}`), nil
	})
	h := readHostMetrics(context.Background())
	if h.CPU != nil || h.RAMUsed != nil || h.DiskUsed != nil {
		t.Fatal("stale Netdata values should be unknown")
	}
}
