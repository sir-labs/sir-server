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
| `proxy.auth=false` | gated | ปิด login gate — route นี้เป็น public (ดู [Login gate](#login-gate)) |

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

### Login gate

ทุก route **ต้อง login ก่อนโดย default** (nginx `auth_request` ไปที่ `sir-auth`):

- ถ้า request ไม่มี `sir_session` cookie ที่ valid → `GET {AUTH_UPSTREAM}/session/verify` ตอบ 401 → nginx redirect 302 ไป
  `https://{AUTH_HOST}/login?rd=https://$host$request_uri`
- ถ้า valid (200) → nginx ส่ง `X-Auth-User-Id`, `X-Auth-Email`, `X-Auth-Role` (ค่าที่ sir-auth ตอบกลับมา) ไปให้ backend
  app อ่าน `X-Auth-Email` ได้เลย ไม่ต้องทำ auth เอง
- `proxy.auth: "false"` → public; nginx **ลบ** `X-Auth-*` ที่ client ส่งมาทิ้ง (spoof ไม่ได้ทั้ง route gated และ public)
- `AUTH_HOST` (`auth.{DOMAIN}`) ไม่ถูก gate เสมอ ไม่ว่า label จะเป็นอะไร; `AUTH_ENABLED=false` ปิด gate ทั้งระบบ (kill switch)
- `internal.{DOMAIN}`: gate ทีละ location ตาม label ของแต่ละ service; หน้า index `/` gate เสมอ
  (`/{port}` ไม่มี slash ที่ 301 ไป `/{port}/` ไม่ถูก gate โดยตั้งใจ — ปลายทาง gate อยู่แล้ว รั่วแค่ว่ามี port นี้)
- nginx ไม่ intercept 401 ของ backend เอง (`proxy_intercept_errors` off) — redirect ไป login เกิดเฉพาะ 401 จาก `/session/verify`

**รูปแบบ `rd`:** stock nginx urlencode ไม่ได้ `rd` จึงเป็น URL ดิบ (`$request_uri` ตามที่ client ส่งมา) และเป็น
query parameter **ตัวสุดท้ายเสมอ** — sir-auth ต้องเอา **ทุกอย่างหลัง `rd=`** เป็นค่า redirect (อย่า parse ด้วย
`&` ปกติ เพราะ `?a=1&b=2` ของ URL เดิมจะหลุด) และต้อง validate ว่า host เป็น `*.{DOMAIN}` ก่อน redirect (กัน open redirect)

**ถ้า sir-auth ล่ม/ไม่มี container:** `proxy_pass` ไป sir-auth ใช้ตัวแปร + `resolver 127.0.0.11` (Docker DNS) จึง
resolve ตอน request ไม่ใช่ตอน load config → `nginx -t` ยังผ่าน route public ยังใช้ได้ ส่วน route ที่ gate จะ fail
closed (500)

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
| `AUTH_ENABLED` | `true` | `false` = ปิด login gate ทุก route (kill switch) |
| `AUTH_HOST` | `auth.{DOMAIN}` | host ของหน้า login — ไม่ถูก gate เสมอ |
| `AUTH_UPSTREAM` | `http://sir-auth:8080` | sir-auth บน sir-net ที่ nginx เรียก `/session/verify` |

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

### Per-service upload limit

Set `proxy.max_body_size: "51m"` to allow a 50 MiB file plus multipart framing.
This only changes that virtual host. Without the label nginx keeps its default.
Only positive integer sizes with an optional `k` or `m` suffix are accepted;
invalid values are ignored. Application-level limits still apply.
