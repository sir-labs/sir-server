package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	ct "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

//go:embed dashboard.html
var statusPage []byte

type serviceNode struct {
	Name      string   `json:"name"`
	Project   string   `json:"project"`
	State     string   `json:"state"`
	Health    string   `json:"health"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason"`
	StartedAt string   `json:"started_at"`
	CheckedAt string   `json:"checked_at"`
	Restarts  int      `json:"restarts"`
	CPU       *float64 `json:"cpu_percent"`
	Memory    uint64   `json:"memory_bytes"`
	Limit     uint64   `json:"memory_limit"`
	RX        *float64 `json:"rx_bytes_sec"`
	TX        *float64 `json:"tx_bytes_sec"`
	PIDs      uint64   `json:"pids"`
	StatsOK   bool     `json:"stats_ok"`
	URL       string   `json:"url,omitempty"`
}
type flowEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}
type serviceCheck struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Nodes     []string `json:"nodes"`
	Status    string   `json:"status"`
	Detail    string   `json:"detail"`
	Latency   float64  `json:"latency_ms"`
	CheckedAt string   `json:"checked_at"`
}
type hostMetrics struct {
	CPU       *float64 `json:"cpu_percent"`
	RAMUsed   *float64 `json:"ram_used_bytes"`
	DiskUsed  *float64 `json:"disk_used_bytes"`
	DiskAvail *float64 `json:"disk_available_bytes"`
}
type monitorSnapshot struct {
	Host      hostMetrics    `json:"host"`
	SampledAt string         `json:"sampled_at"`
	Error     string         `json:"error,omitempty"`
	Nodes     []serviceNode  `json:"nodes"`
	Edges     []flowEdge     `json:"edges"`
	Checks    []serviceCheck `json:"checks"`
	CPUs      int            `json:"host_cpus"`
	Memory    uint64         `json:"host_memory_bytes"`
}
type previousStat struct {
	id                  string
	at                  time.Time
	cpu, system, rx, tx uint64
}
type monitor struct {
	mu       sync.RWMutex
	snapshot monitorSnapshot
	previous map[string]previousStat
}

var liveMonitor = &monitor{previous: map[string]previousStat{}}

func nodeStatus(state, health string) (string, string) {
	if state != "running" {
		return "down", "Container " + state
	}
	switch health {
	case "healthy":
		return "healthy", "Docker healthcheck passed"
	case "unhealthy":
		return "degraded", "Docker healthcheck failed"
	case "starting":
		return "starting", "Healthcheck is starting"
	default:
		return "unknown", "Running; no Docker healthcheck configured"
	}
}

func (m *monitor) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	m.mu.RLock()
	defer m.mu.RUnlock()
	json.NewEncoder(w).Encode(m.snapshot)
}

func (m *monitor) run(ctx context.Context, cli *client.Client) {
	for {
		m.collect(ctx, cli)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (m *monitor) collect(ctx context.Context, cli *client.Client) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	containers, err := cli.ContainerList(ctx, ct.ListOptions{All: true})
	if err != nil {
		m.mu.Lock()
		m.snapshot.Error = "Docker inventory unavailable; displayed values are last known"
		m.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	result := monitorSnapshot{Nodes: []serviceNode{}, Edges: []flowEdge{}, Checks: []serviceCheck{}}
	if info, e := cli.Info(ctx); e == nil {
		result.CPUs = info.NCPU
		result.Memory = uint64(info.MemTotal)
	}
	mu.RLock()
	routeCopy := make(map[string]route, len(routes))
	for k, v := range routes {
		routeCopy[k] = v
	}
	mu.RUnlock()
	for _, c := range containers {
		if len(c.Names) > 0 && c.Labels["proxy.enable"] == "true" {
			name := strings.TrimPrefix(c.Names[0], "/")
			host := c.Labels["proxy.host"]
			if host != "" {
				routeCopy[name] = route{Name: name, Hostname: host, URL: "https://" + host}
			}
		}
	}
	var wg sync.WaitGroup
	var out sync.Mutex
	slots := make(chan struct{}, 6)
	next := make(map[string]previousStat)
	for _, c := range containers {
		if len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if strings.HasPrefix(name, "mcp-dns-test-") || strings.HasPrefix(name, "buildx_buildkit_") || c.Labels["monitor.exclude"] == "true" {
			continue
		}
		wg.Add(1)
		go func(c ct.Summary, name string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			n := serviceNode{Name: name, Project: c.Labels["com.docker.compose.project"], State: c.State, Health: "none", CheckedAt: now.Format(time.RFC3339)}
			detail, e := cli.ContainerInspect(ctx, c.ID)
			if e == nil && detail.State != nil {
				n.State = detail.State.Status
				n.StartedAt = detail.State.StartedAt
				n.Restarts = detail.RestartCount
				if detail.State.Health != nil {
					n.Health = detail.State.Health.Status
				}
			}
			n.Status, n.Reason = nodeStatus(n.State, n.Health)
			if e != nil {
				n.Status = "unknown"
				n.Reason = "Container inspection unavailable"
			}
			if r, ok := routeCopy[name]; ok {
				n.URL = r.URL
			}
			if n.State == "running" {
				sctx, stop := context.WithTimeout(ctx, 3*time.Second)
				reader, e := cli.ContainerStatsOneShot(sctx, c.ID)
				if e == nil {
					var stats ct.StatsResponse
					if json.NewDecoder(io.LimitReader(reader.Body, 2<<20)).Decode(&stats) == nil {
						n.StatsOK = true
						n.Memory = stats.MemoryStats.Usage
						n.Limit = stats.MemoryStats.Limit
						n.PIDs = stats.PidsStats.Current
						cache := stats.MemoryStats.Stats["inactive_file"]
						if cache == 0 {
							cache = stats.MemoryStats.Stats["total_inactive_file"]
						}
						if n.Memory >= cache {
							n.Memory -= cache
						}
						var rx, tx uint64
						for _, v := range stats.Networks {
							rx += v.RxBytes
							tx += v.TxBytes
						}
						cur := previousStat{c.ID, now, stats.CPUStats.CPUUsage.TotalUsage, stats.CPUStats.SystemUsage, rx, tx}
						if prev, ok := m.previous[name]; ok && prev.id == cur.id {
							n.CPU, n.RX, n.TX = statRates(prev, cur, stats.CPUStats.OnlineCPUs)
						}
						out.Lock()
						next[name] = cur
						out.Unlock()
					}
					reader.Body.Close()
				}
				stop()
			}
			out.Lock()
			result.Nodes = append(result.Nodes, n)
			out.Unlock()
		}(c, name)
	}
	wg.Wait()
	// Retain known deleted services until the monitor restarts, rather than making failures disappear.
	m.mu.RLock()
	old := m.snapshot.Nodes
	m.mu.RUnlock()
	present := map[string]bool{}
	for _, n := range result.Nodes {
		present[n.Name] = true
	}
	for _, n := range old {
		if !present[n.Name] {
			n.State = "missing"
			n.Status = "down"
			n.Reason = "Previously observed container is missing"
			n.StatsOK = false
			n.CPU = nil
			n.RX = nil
			n.TX = nil
			n.CheckedAt = now.Format(time.RFC3339)
			result.Nodes = append(result.Nodes, n)
		}
	}
	result.Edges = topology(result.Nodes, routeCopy)
	result.Checks = runServiceChecks(ctx)
	result.Host = readHostMetrics(ctx)
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })
	result.SampledAt = time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	m.snapshot = result
	m.mu.Unlock()
	m.previous = next
}

func statRates(prev, cur previousStat, cpus uint32) (*float64, *float64, *float64) {
	var cpu, rx, tx *float64
	if cpus > 0 && cur.system > prev.system && cur.cpu >= prev.cpu {
		v := float64(cur.cpu-prev.cpu) / float64(cur.system-prev.system) * float64(cpus) * 100
		cpu = &v
	}
	dt := cur.at.Sub(prev.at).Seconds()
	if dt > 0 && cur.rx >= prev.rx && cur.tx >= prev.tx {
		r := float64(cur.rx-prev.rx) / dt
		t := float64(cur.tx-prev.tx) / dt
		rx = &r
		tx = &t
	}
	return cpu, rx, tx
}

func topology(nodes []serviceNode, rs map[string]route) []flowEdge {
	edges := []flowEdge{
		{"cloudflared", "sir-nginx", "HTTP ingress"},
		{"sir-nginx", "sir-auth", "Verify identity"},
		{"sir-auth", "sir-auth-sir-auth-db-1", "Sessions / accounts"},
		{"sir-mcp-mcp-1", "sir-ocr-api-1", "OCR API"},
		{"sir-ocr-api-1", "sir-ocr-rabbitmq-1", "Publish jobs"},
		{"sir-ocr-rabbitmq-1", "sir-ocr-worker-1", "Consume jobs"},
	}
	for name := range rs {
		if name != "sir-auth" {
			edges = append(edges, flowEdge{"sir-nginx", name, "HTTP route"})
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].From+edges[i].To < edges[j].From+edges[j].To })
	return edges
}

var probeClient = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}

func probe(ctx context.Context, url, host, token, method string, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("User-Agent", "sir-system-health/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

func runServiceChecks(ctx context.Context) []serviceCheck {
	tokenBytes, _ := os.ReadFile(getenv("MONITOR_TOKEN_FILE", "/run/sir-monitor/token"))
	token := strings.TrimSpace(string(tokenBytes))
	checks := []serviceCheck{
		{ID: "auth", Name: "Auth HTTP health", Nodes: []string{"sir-auth"}},
		{ID: "ocr", Name: "Gateway → OCR health", Nodes: []string{"sir-nginx", "sir-auth", "sir-ocr-api-1"}},
		{ID: "mcp", Name: "Gateway → MCP initialize + tools", Nodes: []string{"sir-nginx", "sir-auth", "sir-mcp-mcp-1"}},
		{ID: "public", Name: "Public HTTPS → MCP initialize", Nodes: []string{"cloudflared", "sir-nginx", "sir-auth", "sir-mcp-mcp-1"}},
	}
	var wg sync.WaitGroup
	for i := range checks {
		wg.Add(1)
		go func(c *serviceCheck) {
			defer wg.Done()
			t := time.Now()
			defer func() {
				c.Latency = float64(time.Since(t).Milliseconds())
				c.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			}()
			c.Status = "unknown"
			c.Detail = "Probe credential not configured"
			if c.ID != "auth" && token == "" {
				return
			}
			var code int
			var data []byte
			var err error
			switch c.ID {
			case "auth":
				code, data, err = probe(ctx, "http://sir-auth:8080/health", "", "", "GET", nil)
			case "ocr":
				code, data, err = probe(ctx, "http://"+nginxContainer+"/healthz", "ocr."+domain, token, "GET", nil)
			default:
				target := "http://" + nginxContainer + "/mcp"
				host := "mcp." + domain
				if c.ID == "public" {
					target = "https://" + host + "/mcp"
				}
				init := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"system-health","version":"1"}}}`)
				code, data, err = probe(ctx, target, host, token, "POST", init)
				if err == nil && code == 200 {
					var v struct {
						Result struct {
							ServerInfo struct {
								Name string `json:"name"`
							} `json:"serverInfo"`
						} `json:"result"`
					}
					if json.Unmarshal(data, &v) != nil || v.Result.ServerInfo.Name == "" {
						err = fmt.Errorf("protocol response invalid")
					}
					if err == nil && c.ID == "mcp" {
						code, data, err = probe(ctx, target, host, token, "POST", []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
						if err == nil && code == 200 {
							var v struct {
								Result struct {
									Tools []json.RawMessage `json:"tools"`
								} `json:"result"`
							}
							if json.Unmarshal(data, &v) != nil || len(v.Result.Tools) == 0 {
								err = fmt.Errorf("tools response invalid")
							}
						}
					}
				}
			}
			if err != nil {
				c.Status = "failed"
				c.Detail = "Connection, timeout or response validation failed"
				return
			}
			if code != 200 {
				c.Status = "failed"
				c.Detail = fmt.Sprintf("HTTP %d", code)
				return
			}
			if c.ID == "auth" || c.ID == "ocr" {
				var v struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(data, &v) != nil || v.Status != "ok" {
					c.Status = "failed"
					c.Detail = "Health response invalid"
					return
				}
			}
			c.Status = "passed"
			c.Detail = "HTTP 200 · response verified"
		}(&checks[i])
	}
	wg.Wait()
	return checks
}

// Netdata dimensions use percentage, MiB (RAM), and GiB (disk).
func readHostMetrics(ctx context.Context) hostMetrics {
	h := hostMetrics{}
	for _, chart := range []string{"system.cpu", "system.ram", "disk_space./"} {
		code, body, err := probe(ctx, "http://sir-monitor-monitor-1:19999/api/v1/data?chart="+chart+"&after=-10&points=1&format=json", "", "", "GET", nil)
		if err != nil || code != 200 {
			continue
		}
		var v struct {
			Labels []string     `json:"labels"`
			Data   [][]*float64 `json:"data"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Data) == 0 || len(v.Data[0]) != len(v.Labels) || len(v.Data[0]) == 0 || v.Data[0][0] == nil {
			continue
		}
		if age := time.Now().Unix() - int64(*v.Data[0][0]); age > 60 || age < -10 {
			continue
		}
		values := map[string]float64{}
		valid := true
		for i, label := range v.Labels {
			if v.Data[0][i] == nil {
				valid = false
				break
			}
			values[label] = *v.Data[0][i]
		}
		if !valid {
			continue
		}
		switch chart {
		case "system.cpu":
			var sum float64
			for name, val := range values {
				if name != "time" && name != "idle" {
					sum += val
				}
			}
			h.CPU = &sum
		case "system.ram":
			if val, ok := values["used"]; ok {
				val *= 1024 * 1024
				h.RAMUsed = &val
			}
		case "disk_space./":
			if val, ok := values["used"]; ok {
				val *= 1024 * 1024 * 1024
				h.DiskUsed = &val
			}
			if val, ok := values["avail"]; ok {
				val *= 1024 * 1024 * 1024
				h.DiskAvail = &val
			}
		}
	}
	return h
}
