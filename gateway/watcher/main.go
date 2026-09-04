package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net"
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
	internalHost   = getenv("INTERNAL_HOST", "internal."+domain)
	// internalTarget is where internal-path requests are proxied: the Docker
	// HOST's own network, not the sir-net bridge. This lets internal services
	// be anything that publishes a port on the host — a sir-net container, a
	// container from an unrelated compose project, or a bare host process —
	// as long as it's bound to more than just 127.0.0.1. Requires the nginx
	// container to have `extra_hosts: ["host.docker.internal:host-gateway"]`.
	internalTarget = getenv("INTERNAL_TARGET_HOST", "host.docker.internal")
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
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
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

    location / {
        return 404;
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

type internalSvc struct {
	Name string
	Port string
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

// inspectContainer returns the container's IP on networkName and whether it's
// safe to route to. healthy is true unless the container defines a healthcheck
// that is currently reporting "unhealthy" — containers with no healthcheck are
// always considered healthy, matching pre-healthcheck behavior.
func inspectContainer(ctx context.Context, cli *client.Client, containerID, networkName string) (ip string, healthy bool) {
	healthy = true
	checkHealth := func(info containertypes.InspectResponse) {
		if info.State != nil && info.State.Health != nil && info.State.Health.Status == "unhealthy" {
			healthy = false
		}
	}
	for range 4 {
		info, err := cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return "", true
		}
		checkHealth(info)
		if nets := info.NetworkSettings.Networks; nets != nil {
			if n, ok := nets[networkName]; ok && n.IPAddress != "" {
				return n.IPAddress, healthy
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// fallback: any network
	info, err := cli.ContainerInspect(ctx, containerID)
	if err == nil {
		checkHealth(info)
		for _, n := range info.NetworkSettings.Networks {
			if n.IPAddress != "" {
				log.Printf("[WARNING] %s not on %s, using fallback IP — upstream may be unreachable", containerID, networkName)
				return n.IPAddress, healthy
			}
		}
	}
	return "", healthy
}

// isHealthy reports whether a container is safe to route to, without needing
// its network IP. Same semantics as inspectContainer's health check: only
// "unhealthy" excludes it, no healthcheck defined always passes.
func isHealthy(ctx context.Context, cli *client.Client, containerID string) bool {
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return true
	}
	return !(info.State != nil && info.State.Health != nil && info.State.Health.Status == "unhealthy")
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

// renderInternalConf builds a single nginx server block for internalHost that
// path-routes to each internal service by port: /{port}/ -> that service.
func renderInternalConf(hostname string, services []internalSvc) string {
	sort.Slice(services, func(i, j int) bool { return services[i].Port < services[j].Port })

	var b strings.Builder
	fmt.Fprintf(&b, "server {\n    listen 80;\n    server_name %s;\n\n", hostname)
	b.WriteString("    location = / {\n        root /etc/nginx/conf.d;\n        try_files /internal-index.html =404;\n        default_type text/html;\n        charset utf-8;\n    }\n\n")
	for _, s := range services {
		fmt.Fprintf(&b, "    location = /%s {\n        return 301 /%s/;\n    }\n\n", s.Port, s.Port)
		fmt.Fprintf(&b, "    location /%s/ {\n        proxy_pass http://%s:%s/;\n        proxy_http_version 1.1;\n        proxy_set_header Upgrade $http_upgrade;\n        proxy_set_header Connection \"upgrade\";\n        proxy_set_header Host $host;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n    }\n\n", s.Port, internalTarget, s.Port)
	}
	b.WriteString("}\n")
	return b.String()
}

// internalIndexHTML renders the page served at internalHost/, linking to
// every currently registered internal service.
func internalIndexHTML(services []internalSvc) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>internal services</title>
  <style>
    body { font-family: monospace; padding: 2rem; background: #111; color: #eee; }
    h1 { color: #0cf; margin-bottom: 1.5rem; }
    ul { list-style: none; padding: 0; }
    li { padding: .5rem 0; border-bottom: 1px solid #222; }
    a { color: #0cf; text-decoration: none; font-size: 1.1rem; }
    a:hover { text-decoration: underline; }
    .empty { color: #555; }
  </style>
</head>
<body>
  <h1>internal services</h1>
`)
	if len(services) == 0 {
		b.WriteString(`  <p class="empty">No active internal services.</p>` + "\n")
	} else {
		b.WriteString("  <ul>\n")
		for _, s := range services {
			path := "/" + s.Port + "/"
			fmt.Fprintf(&b, "    <li><a href=\"%s\">%s</a> — %s</li>\n", path, path, html.EscapeString(s.Name))
		}
		b.WriteString("  </ul>\n")
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
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
	usedInternalPorts := map[string]string{}
	var internalServices []internalSvc

	for _, c := range containers {
		name := strings.TrimPrefix(c.Names[0], "/")
		hostname := c.Labels["proxy.host"]
		if hostname == "" {
			hostname = fmt.Sprintf("%s.%s", name, domain)
		}

		// Shared path-based routing: every container that opts into internalHost
		// is exposed at internalHost/{proxy.port}/ instead of getting its own
		// hostname. The port doubles as the URL path segment, so it must be
		// explicit and unique among internal services.
		if hostname == internalHost {
			port := c.Labels["proxy.port"]
			if port == "" {
				log.Printf("[WARNING] %s: proxy.host=%s requires an explicit proxy.port, skipping", name, internalHost)
				continue
			}
			if owner, dup := usedInternalPorts[port]; dup {
				log.Printf("[WARNING] Duplicate internal proxy.port %s: %s already claimed by %s, skipping", port, name, owner)
				continue
			}
			if !isHealthy(ctx, cli, c.ID) {
				log.Printf("[WARNING] %s is unhealthy, removing from internal routing", name)
				continue
			}

			usedInternalPorts[port] = name
			internalServices = append(internalServices, internalSvc{Name: name, Port: port})
			newRoutes[name] = route{
				Name:         name,
				Hostname:     internalHost,
				URL:          fmt.Sprintf("http://%s/%s/", internalHost, port),
				IP:           internalTarget,
				Port:         port,
				RegisteredAt: time.Now().Format("15:04:05"),
			}
			log.Printf("[INFO] Internal proxy: %s/%s/ -> %s:%s (host port, must be published)", internalHost, port, internalTarget, port)
			continue
		}

		port := c.Labels["proxy.port"]
		if port == "" {
			port = "80"
		}

		if owner, dup := usedHostnames[hostname]; dup {
			log.Printf("[WARNING] Duplicate proxy.host %s: %s already claimed by %s, skipping", hostname, name, owner)
			continue
		}
		usedHostnames[hostname] = name

		ensureOnNetwork(ctx, cli, c.ID, proxyNetwork, connectedIDs)

		ip, healthy := inspectContainer(ctx, cli, c.ID, proxyNetwork)
		if ip == "" {
			log.Printf("[WARNING] No IP found for %s, skipping", name)
			continue
		}
		if !healthy {
			log.Printf("[WARNING] %s is unhealthy, removing from rotation", name)
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
	os.WriteFile(filepath.Join(confDir, "default.conf"), []byte(defaultConf), 0644) //nolint:errcheck
	for name, conf := range confContents {
		os.WriteFile(filepath.Join(confDir, name+".conf"), []byte(conf), 0644) //nolint:errcheck
	}
	// Only claim internalHost once something has actually registered for it —
	// otherwise every fresh watcher start silently starts responding on a
	// hostname that previously 404'd through to the default fallback page.
	//
	// nginx resolves a static proxy_pass hostname at config-load time, so if
	// internalTarget doesn't resolve, `nginx -t` fails and reloadNginx skips
	// the reload entirely — freezing *every* route, not just internal ones.
	// Check resolvability here first so a broken internalTarget only disables
	// this one feature instead of the whole proxy.
	internalConfPath := filepath.Join(confDir, "internal.conf")
	internalIndexPath := filepath.Join(confDir, "internal-index.html")
	if len(internalServices) > 0 {
		if _, err := net.LookupHost(internalTarget); err != nil {
			log.Printf("[ERROR] internalTarget %q does not resolve (%v) — skipping internal.conf to avoid breaking nginx reload for all routes", internalTarget, err)
			for _, s := range internalServices {
				delete(newRoutes, s.Name)
			}
			internalServices = nil
		}
	}
	if len(internalServices) > 0 {
		os.WriteFile(internalConfPath, []byte(renderInternalConf(internalHost, internalServices)), 0644) //nolint:errcheck
		os.WriteFile(internalIndexPath, []byte(internalIndexHTML(internalServices)), 0644)               //nolint:errcheck
	} else {
		os.Remove(internalConfPath)  //nolint:errcheck
		os.Remove(internalIndexPath) //nolint:errcheck
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
				filters.Arg("label", "proxy.enable=true"),
				filters.Arg("event", "start"),
				filters.Arg("event", "die"),
				filters.Arg("event", "health_status"),
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
