# sir-watcher

Docker event watcher ที่ auto-generate nginx reverse proxy config และ reload nginx เมื่อ container start/stop

## Overview

watcher **ไม่ได้อยู่ใน traffic path** — ทำหน้าที่ control plane อย่างเดียว:

```
Docker events ──► watcher ──► nginx conf files ──► SIGHUP nginx
```

traffic จริงๆ ไหล: `cloudflared → nginx → backend container`

## How it works

1. ตอน startup — scan หา container ที่มี label `proxy.enable=true` ทั้งหมด แล้ว generate config
2. ฟัง Docker event `start` / `die` แล้ว regenerate config ใหม่ทุกครั้ง
3. เขียน `.conf` ลง shared volume `/etc/nginx/conf.d`
4. รัน `nginx -t` เช็ก syntax ก่อน แล้วค่อยส่ง SIGHUP

## Container labels

| Label | Default | Description |
|---|---|---|
| `proxy.enable=true` | required | opt-in สำหรับ container นี้ |
| `proxy.port=8080` | `80` | port ของ container |
| `proxy.host=foo.example.com` | `{name}.{DOMAIN}` | custom hostname |

ตัวอย่าง:

```yaml
services:
  myapp:
    image: myapp
    labels:
      proxy.enable: "true"
      proxy.port: "3000"
      proxy.host: "myapp.sir-labs.com"  # optional
```

### Shared path-based routing (`internal.<DOMAIN>`)

Set `proxy.host` to the reserved internal host (default `internal.{DOMAIN}`,
override via `INTERNAL_HOST`) to skip getting your own subdomain and instead
be exposed at `internal.{DOMAIN}/{proxy.port}/`. `proxy.port` is required in
this mode — it doubles as both the URL path segment and the port number the
watcher forwards to, so it must be unique among internal services.

**This mode routes to the Docker host's own network, not sir-net** — the
container must **publish** the port with `ports:`, not just listen on it
internally. That's what makes it work for anything on the host, not only
containers wired into sir-net:

```yaml
services:
  admin-tool:
    image: admin-tool
    ports:
      - "3001:3001"         # required — internal routing reaches the host, not this container's IP
    labels:
      proxy.enable: "true"
      proxy.host: "internal.sir-labs.com"
      proxy.port: "3001"    # → http://internal.sir-labs.com/3001/
```

The label is still what makes it discoverable (for the index page and to
opt in explicitly) — but nginx proxies to `{INTERNAL_TARGET_HOST}:{port}`
(default `host.docker.internal`, resolved via
`extra_hosts: ["host.docker.internal:host-gateway"]` on the `sir-nginx`
service), which is the same as hitting `localhost:{port}` on the host
machine itself. The service on that port must bind to more than just
`127.0.0.1` (e.g. `0.0.0.0`) — loopback-only binds aren't reachable from a
container even via host-gateway.

Visiting `internal.{DOMAIN}/` serves an auto-generated index linking every
active internal service. `/{port}` (no trailing slash) 301s to `/{port}/`.
No numeric range is enforced — pick any free port; sir-server's convention is
3000–3999 for internal-only tools, to keep them visually distinct from public
service ports.

Caveats:
- Prefix-stripped path routing (the backend sees `/`, not `/{port}/`). Apps
  whose HTML/JS assumes it's served from `/` (absolute asset paths,
  client-side routers) may break under a path prefix — use a normal
  `proxy.host` subdomain for those instead.
- Anything reachable at `{proxy.port}` on the host becomes reachable at
  `internal.{DOMAIN}/{port}/` the moment the label is applied — since
  `*.sir-labs.com` is wildcard-routed through cloudflared, that's public
  internet exposure, not just LAN-local. Treat the label as the opt-in, not
  the port binding itself.

### Health-aware routing

If a container defines a Docker `healthcheck:` and it reports `unhealthy`,
the watcher excludes it from both normal and internal routing on the next
regeneration (triggered by the container's `start`/`die` events and by
`health_status` transitions). Containers without a healthcheck are always
considered healthy, same as before this existed.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `DOMAIN` | `sir-labs.com` | base domain สำหรับ auto hostname |
| `NGINX_CONTAINER` | `sir-nginx` | container name ของ nginx |
| `PROXY_NETWORK` | `sir-server_sir-net` | Docker network ที่ใช้ connect container |
| `CONF_DIR` | `/etc/nginx/conf.d` | path สำหรับเขียน nginx config |
| `DASHBOARD_PORT` | `8080` | port ของ dashboard HTTP server |
| `INTERNAL_HOST` | `internal.{DOMAIN}` | reserved hostname สำหรับ shared path-based routing |
| `INTERNAL_TARGET_HOST` | `host.docker.internal` | proxy target สำหรับ internal routing — ต้อง resolve ไปที่ Docker host จริง (ดู `extra_hosts` บน `sir-nginx`) |

## Dashboard

watcher serve dashboard ที่ port 8080:

- `GET /` — HTML table แสดง active routes
- `GET /routes` — JSON list ของ routes ทั้งหมด

## Build

```bash
# local
go build -o watcher .

# Docker (multi-stage, final image ~36MB)
docker compose build sir-watcher
```

## Stack

- Go 1.26
- [Docker SDK v28](https://pkg.go.dev/github.com/docker/docker)
- single binary, no runtime dependencies
