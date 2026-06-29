import html as html_lib
import json
import os
import signal
import logging
import threading
import time
from datetime import datetime
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import docker
from jinja2 import Template

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger(__name__)

DOMAIN = os.environ.get("DOMAIN", "sir-labs.com")
NGINX_CONTAINER = os.environ.get("NGINX_CONTAINER", "sir-nginx")
PROXY_NETWORK = os.environ.get("PROXY_NETWORK", "sir-server_sir-net")
CONF_DIR = Path(os.environ.get("CONF_DIR", "/etc/nginx/conf.d"))
DASHBOARD_PORT = int(os.environ.get("DASHBOARD_PORT", "8080"))

NGINX_CONF_TEMPLATE = Template("""\
upstream {{ name }} {
    server {{ ip }}:{{ port }};
}

server {
    listen 80;
    server_name {{ hostname }};

    location / {
        proxy_pass http://{{ name }};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
""")

DEFAULT_CONF = """\
server {
    listen 80 default_server;
    server_name _;
    return 404;
}
"""

DASHBOARD_HTML = """\
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>sir-server proxy</title>
  <style>
    body {{ font-family: monospace; padding: 2rem; background: #111; color: #eee; }}
    h1 {{ color: #0cf; margin-bottom: 1.5rem; }}
    table {{ border-collapse: collapse; width: 100%; }}
    th {{ text-align: left; padding: .5rem 1rem; background: #222; color: #888; }}
    td {{ padding: .5rem 1rem; border-bottom: 1px solid #222; }}
    a {{ color: #0cf; text-decoration: none; }}
    a:hover {{ text-decoration: underline; }}
    .empty {{ color: #555; padding: 1rem; }}
  </style>
</head>
<body>
  <h1>sir-server proxy</h1>
  {rows}
</body>
</html>
"""

_lock = threading.Lock()
_routes: dict[str, dict] = {}


class DashboardHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/routes":
            self._serve_json()
        else:
            self._serve_html()

    def _serve_json(self):
        with _lock:
            data = list(_routes.values())
        body = json.dumps(data, indent=2).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def _serve_html(self):
        with _lock:
            routes = list(_routes.values())

        if routes:
            rows = (
                "<table><tr>"
                "<th>Container</th><th>URL</th><th>Upstream</th><th>Registered</th>"
                "</tr>"
            )
            for r in sorted(routes, key=lambda x: x["name"]):
                # Fix #1: escape all user-controlled values to prevent XSS
                name = html_lib.escape(r["name"])
                hostname = html_lib.escape(r["hostname"])
                ip = html_lib.escape(r["ip"])
                port = html_lib.escape(r["port"])
                rows += (
                    f"<tr>"
                    f"<td>{name}</td>"
                    f"<td><a href=\"http://{hostname}\" target=\"_blank\">{hostname}</a></td>"
                    f"<td>{ip}:{port}</td>"
                    f"<td>{r['registered_at']}</td>"
                    f"</tr>"
                )
            rows += "</table>"
        else:
            rows = "<p class=\"empty\">No active routes.</p>"

        body = DASHBOARD_HTML.format(rows=rows).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def _get_connected_ids(client: docker.DockerClient, network_name: str) -> set[str]:
    # Fix #8: fetch once per generate_configs call, not per container (was O(n²))
    try:
        network = client.networks.get(network_name)
        return {c.id for c in network.containers}
    except docker.errors.NotFound:
        return set()


def ensure_on_network(
    client: docker.DockerClient,
    container,
    network_name: str,
    connected_ids: set[str],
) -> None:
    if container.id in connected_ids:
        return
    try:
        network = client.networks.get(network_name)
        network.connect(container)
        container.reload()  # Fix #4: refresh immediately after connect so IP is current
        log.info("Connected %s to %s", container.name, network_name)
    except docker.errors.NotFound:
        log.warning("Network %s not found, skipping connect", network_name)
    except docker.errors.APIError as e:
        # Fix #3 (partial): catch already-connected errors instead of crashing
        log.warning("Could not connect %s to %s: %s", container.name, network_name, e)


def get_container_ip(container, network_name: str) -> str | None:
    # Fix #4: Docker may not assign the IP immediately after network.connect() returns.
    # Retry briefly to handle the assignment race.
    for attempt in range(4):
        container.reload()
        networks = container.attrs["NetworkSettings"]["Networks"]
        if network_name in networks:
            ip = networks[network_name]["IPAddress"]
            if ip:
                return ip
        if attempt < 3:
            time.sleep(0.3)
    # fallback: first available IP from any network (with warning)
    for net in networks.values():
        if net.get("IPAddress"):
            log.warning(
                "%s not on %s, using fallback IP — upstream may be unreachable",
                container.name, network_name,
            )
            return net["IPAddress"]
    return None


def reload_nginx(client: docker.DockerClient) -> None:
    try:
        nginx = client.containers.get(NGINX_CONTAINER)
        result = nginx.exec_run(["nginx", "-t"], stderr=True, stdout=True)
        if result.exit_code != 0:
            log.error("nginx config test failed:\n%s", result.output.decode())
            return
        nginx.kill(signal=signal.SIGHUP)
        log.info("Nginx reloaded")
    except docker.errors.NotFound:
        log.warning("Nginx container %s not found", NGINX_CONTAINER)
    except docker.errors.APIError as e:
        log.warning("Nginx not ready, skipping reload: %s", e)


def generate_configs(client: docker.DockerClient) -> None:
    log.info("Regenerating nginx configs...")

    # Fix #5: Docker API calls (slow — network I/O) run outside _lock so dashboard
    # reads are never blocked by network.connect() or container.reload() latency.
    connected_ids = _get_connected_ids(client, PROXY_NETWORK)
    containers = client.containers.list(filters={"label": "proxy.enable=true"})
    new_routes: dict[str, dict] = {}
    conf_contents: dict[str, str] = {}
    used_hostnames: dict[str, str] = {}

    for container in containers:
        name = container.name.lstrip("/")
        labels = container.labels
        port = labels.get("proxy.port", "80")
        hostname = labels.get("proxy.host", f"{name}.{DOMAIN}")

        # Fix #6: warn and skip duplicate hostnames instead of silently routing wrong
        if hostname in used_hostnames:
            log.warning(
                "Duplicate proxy.host %s: %s already claimed by %s, skipping",
                hostname, name, used_hostnames[hostname],
            )
            continue
        used_hostnames[hostname] = name

        ensure_on_network(client, container, PROXY_NETWORK, connected_ids)

        ip = get_container_ip(container, PROXY_NETWORK)
        if not ip:
            log.warning("No IP found for %s, skipping", name)
            continue

        conf_contents[name] = NGINX_CONF_TEMPLATE.render(
            name=name, ip=ip, port=port, hostname=hostname
        )
        new_routes[name] = {
            "name": name,
            "hostname": hostname,
            "url": f"http://{hostname}",
            "ip": ip,
            "port": port,
            "registered_at": datetime.now().strftime("%H:%M:%S"),
        }
        log.info("Proxy: %s -> %s:%s", hostname, ip, port)

    # Fix #2 + #5: hold _lock only for fast file writes + routes update + reload.
    # reload_nginx is inside the lock to prevent a concurrent generate_configs from
    # deleting conf files between our write phase and our nginx -t call.
    with _lock:
        for f in CONF_DIR.glob("*.conf"):
            f.unlink()
        (CONF_DIR / "default.conf").write_text(DEFAULT_CONF)
        for name, conf in conf_contents.items():
            (CONF_DIR / f"{name}.conf").write_text(conf)
        _routes.clear()
        _routes.update(new_routes)
        reload_nginx(client)


def watch_events(client: docker.DockerClient) -> None:
    log.info("Watching Docker events (domain=%s, network=%s)...", DOMAIN, PROXY_NETWORK)
    # Fix #3: reconnect loop so a Docker socket disconnect doesn't kill the watcher
    while True:
        try:
            for event in client.events(
                filters={"type": "container", "event": ["start", "die"]},
                decode=True,
            ):
                action = event.get("Action")
                cname = event.get("Actor", {}).get("Attributes", {}).get("name", "?")
                log.info("Event: %s %s", action, cname)
                generate_configs(client)
        except Exception as e:
            log.warning("Docker event stream error: %s — reconnecting in 5s", e)
            time.sleep(5)
            generate_configs(client)  # catch up on any missed events


def main() -> None:
    client = docker.from_env()

    server = HTTPServer(("0.0.0.0", DASHBOARD_PORT), DashboardHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    log.info("Dashboard on port %d", DASHBOARD_PORT)

    generate_configs(client)
    watch_events(client)


if __name__ == "__main__":
    main()
