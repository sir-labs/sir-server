package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	containertypes "github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	networktypes "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	domain         = getenv("DOMAIN", "sir-labs.com")
	nginxContainer = getenv("NGINX_CONTAINER", "sir-nginx")
	proxyNetwork   = getenv("PROXY_NETWORK", "sir-server_sir-net")
	confDir        = getenv("CONF_DIR", "/etc/nginx/conf.d")
	dashboardPort  = getenv("DASHBOARD_PORT", "8080")
)

var nginxConfTmpl = template.Must(template.New("nginx").Parse(
	`upstream {{.Name}} {
    server {{.IP}}:{{.Port}};
}

server {
    listen 80;
    server_name {{.Hostname}};

    location / {
        proxy_pass http://{{.Name}};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
`))

const defaultConf = `server {
    listen 80 default_server;
    server_name _;
    root /usr/share/nginx/html;

    location / {
        try_files /fallback.html =404;
    }
}
`

const dashboardHead = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>sir-server proxy</title>
  <style>
    body { font-family: monospace; padding: 2rem; background: #111; color: #eee; }
    h1 { color: #0cf; margin-bottom: 1.5rem; }
    table { border-collapse: collapse; width: 100%; }
    th { text-align: left; padding: .5rem 1rem; background: #222; color: #888; }
    td { padding: .5rem 1rem; border-bottom: 1px solid #222; }
    a { color: #0cf; text-decoration: none; }
    a:hover { text-decoration: underline; }
    .empty { color: #555; padding: 1rem; }
  </style>
</head>
<body>
  <h1>sir-server proxy</h1>
`

type route struct {
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	URL          string `json:"url"`
	IP           string `json:"ip"`
	Port         string `json:"port"`
	RegisteredAt string `json:"registered_at"`
}

var (
	mu     sync.RWMutex
	routes = map[string]route{}
)

func serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/routes" {
		mu.RLock()
		data := make([]route, 0, len(routes))
		for _, rt := range routes {
			data = append(data, rt)
		}
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data) //nolint:errcheck
		return
	}

	mu.RLock()
	list := make([]route, 0, len(routes))
	for _, rt := range routes {
		list = append(list, rt)
	}
	mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHead)
	if len(list) == 0 {
		fmt.Fprint(w, `  <p class="empty">No active routes.</p>`)
	} else {
		fmt.Fprint(w, "  <table><tr><th>Container</th><th>URL</th><th>Upstream</th><th>Registered</th></tr>")
		for _, rt := range list {
			name := html.EscapeString(rt.Name)
			hostname := html.EscapeString(rt.Hostname)
			ip := html.EscapeString(rt.IP)
			port := html.EscapeString(rt.Port)
			fmt.Fprintf(w,
				"<tr><td>%s</td><td><a href=\"http://%s\" target=\"_blank\">%s</a></td><td>%s:%s</td><td>%s</td></tr>",
				name, hostname, hostname, ip, port, rt.RegisteredAt,
			)
		}
		fmt.Fprint(w, "</table>")
	}
	fmt.Fprint(w, "\n</body>\n</html>\n")
}

func getConnectedIDs(ctx context.Context, cli *client.Client, networkName string) map[string]bool {
	net, err := cli.NetworkInspect(ctx, networkName, networktypes.InspectOptions{})
	if err != nil {
		return map[string]bool{}
	}
	ids := make(map[string]bool, len(net.Containers))
	for id := range net.Containers {
		ids[id] = true
	}
	return ids
}

func ensureOnNetwork(ctx context.Context, cli *client.Client, containerID, networkName string, connected map[string]bool) {
	if connected[containerID] {
		return
	}
	err := cli.NetworkConnect(ctx, networkName, containerID, &networktypes.EndpointSettings{})
	if err == nil {
		log.Printf("[INFO] Connected %s to %s", containerID, networkName)
		return
	}
	if errdefs.IsConflict(err) || strings.Contains(err.Error(), "already exists") {
		return
	}
	if errdefs.IsNotFound(err) {
		log.Printf("[WARNING] Network %s not found, skipping connect", networkName)
		return
	}
	log.Printf("[WARNING] Could not connect %s to %s: %v", containerID, networkName, err)
}

func getContainerIP(ctx context.Context, cli *client.Client, containerID, networkName string) string {
	for range 4 {
		info, err := cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return ""
		}
		if nets := info.NetworkSettings.Networks; nets != nil {
			if n, ok := nets[networkName]; ok && n.IPAddress != "" {
				return n.IPAddress
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// fallback: any network
	info, err := cli.ContainerInspect(ctx, containerID)
	if err == nil {
		for _, n := range info.NetworkSettings.Networks {
			if n.IPAddress != "" {
				log.Printf("[WARNING] %s not on %s, using fallback IP — upstream may be unreachable", containerID, networkName)
				return n.IPAddress
			}
		}
	}
	return ""
}

func reloadNginx(ctx context.Context, cli *client.Client) {
	info, err := cli.ContainerInspect(ctx, nginxContainer)
	if err != nil {
		if errdefs.IsNotFound(err) {
			log.Printf("[WARNING] Nginx container %s not found", nginxContainer)
		} else {
			log.Printf("[WARNING] Nginx not ready, skipping reload: %v", err)
		}
		return
	}

	execResp, err := cli.ContainerExecCreate(ctx, info.ID, containertypes.ExecOptions{
		Cmd:          []string{"nginx", "-t"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		log.Printf("[WARNING] Nginx not ready, skipping reload: %v", err)
		return
	}

	hijacked, err := cli.ContainerExecAttach(ctx, execResp.ID, containertypes.ExecStartOptions{})
	if err != nil {
		log.Printf("[WARNING] Nginx exec attach failed: %v", err)
		return
	}
	defer hijacked.Close()

	var outBuf, errBuf bytes.Buffer
	stdcopy.StdCopy(&outBuf, &errBuf, hijacked.Reader) //nolint:errcheck

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		log.Printf("[WARNING] Nginx exec inspect failed: %v", err)
		return
	}
	if inspect.ExitCode != 0 {
		log.Printf("[ERROR] nginx config test failed:\n%s", errBuf.String())
		return
	}

	if err := cli.ContainerKill(ctx, info.ID, "HUP"); err != nil {
		log.Printf("[WARNING] Nginx kill HUP failed: %v", err)
		return
	}
	log.Printf("[INFO] Nginx reloaded")
}

type confData struct {
	Name, IP, Port, Hostname string
}

func generateConfigs(ctx context.Context, cli *client.Client) {
	log.Printf("[INFO] Regenerating nginx configs...")

	connectedIDs := getConnectedIDs(ctx, cli, proxyNetwork)
	containers, err := cli.ContainerList(ctx, containertypes.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "proxy.enable=true")),
	})
	if err != nil {
		log.Printf("[ERROR] ContainerList failed: %v", err)
		return
	}

	newRoutes := map[string]route{}
	confContents := map[string]string{}
	usedHostnames := map[string]string{}

	for _, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		port := c.Labels["proxy.port"]
		if port == "" {
			port = "80"
		}
		hostname := c.Labels["proxy.host"]
		if hostname == "" {
			hostname = fmt.Sprintf("%s.%s", name, domain)
		}

		if owner, dup := usedHostnames[hostname]; dup {
			log.Printf("[WARNING] Duplicate proxy.host %s: %s already claimed by %s, skipping", hostname, name, owner)
			continue
		}
		usedHostnames[hostname] = name

		ensureOnNetwork(ctx, cli, c.ID, proxyNetwork, connectedIDs)

		ip := getContainerIP(ctx, cli, c.ID, proxyNetwork)
		if ip == "" {
			log.Printf("[WARNING] No IP found for %s, skipping", name)
			continue
		}

		var buf bytes.Buffer
		if err := nginxConfTmpl.Execute(&buf, confData{Name: name, IP: ip, Port: port, Hostname: hostname}); err != nil {
			log.Printf("[ERROR] Template error for %s: %v", name, err)
			continue
		}
		confContents[name] = buf.String()
		newRoutes[name] = route{
			Name:         name,
			Hostname:     hostname,
			URL:          fmt.Sprintf("http://%s", hostname),
			IP:           ip,
			Port:         port,
			RegisteredAt: time.Now().Format("15:04:05"),
		}
		log.Printf("[INFO] Proxy: %s -> %s:%s", hostname, ip, port)
	}

	mu.Lock()
	defer mu.Unlock()

	matches, _ := filepath.Glob(filepath.Join(confDir, "*.conf"))
	for _, f := range matches {
		os.Remove(f) //nolint:errcheck
	}
	os.WriteFile(filepath.Join(confDir, "default.conf"), []byte(defaultConf), 0644)    //nolint:errcheck
	for name, conf := range confContents {
		os.WriteFile(filepath.Join(confDir, name+".conf"), []byte(conf), 0644) //nolint:errcheck
	}

	for k := range routes {
		delete(routes, k)
	}
	for k, v := range newRoutes {
		routes[k] = v
	}

	reloadNginx(ctx, cli)
}

func watchEvents(ctx context.Context, cli *client.Client) {
	log.Printf("[INFO] Watching Docker events (domain=%s, network=%s)...", domain, proxyNetwork)
	for {
		eventCh, errCh := cli.Events(ctx, dockerevents.ListOptions{
			Filters: filters.NewArgs(
				filters.Arg("type", "container"),
				filters.Arg("event", "start"),
				filters.Arg("event", "die"),
			),
		})
	inner:
		for {
			select {
			case ev, ok := <-eventCh:
				if !ok {
					break inner
				}
				cname := ev.Actor.Attributes["name"]
				log.Printf("[INFO] Event: %s %s", ev.Action, cname)
				generateConfigs(ctx, cli)
			case err, ok := <-errCh:
				if ok && err != nil {
					log.Printf("[WARNING] Docker event stream error: %v — reconnecting in 5s", err)
				}
				break inner
			}
		}
		time.Sleep(5 * time.Second)
		generateConfigs(ctx, cli)
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	http.HandleFunc("/", serveHTTP)
	go func() {
		log.Printf("[INFO] Dashboard on port %s", dashboardPort)
		if err := http.ListenAndServe(":"+dashboardPort, nil); err != nil {
			log.Fatalf("Dashboard server error: %v", err)
		}
	}()

	generateConfigs(ctx, cli)
	watchEvents(ctx, cli)
}
