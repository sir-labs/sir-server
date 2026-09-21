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
- **Token (ไม่ใช้ browser):** ส่ง `Authorization: Bearer sirpat_…` (สร้างที่ `https://{AUTH_HOST}/account/tokens`)
  - token ผิด/หมดอายุ/ถูก revoke → **401 JSON** `{"error":"invalid_token"}` + `WWW-Authenticate: Bearer realm="sir-labs"`
    (ไม่ใช่ 302 ไปหน้า login) — ถ้าส่ง sirpat มา sir-auth จะไม่ fallback ไปใช้ cookie
  - nginx **ลบ** `Authorization` ที่เป็น `Bearer sirpat_…` ก่อนส่งให้ backend ทุก route (gated, public, internal) —
    token ไม่รั่วไปถึง app; `Authorization` แบบอื่น (เช่น JWT ของ app เอง) ผ่านไปตามเดิม
- verify subrequest ส่ง `Cookie`, `Authorization`, `X-Original-URI`, `X-Original-Host`, `X-Original-Method` และ
  `X-Real-IP` (= `CF-Connecting-IP` ถ้ามี ไม่งั้น `$remote_addr`; spoof ได้จาก LAN — ใช้เป็นข้อมูลเท่านั้น) ให้ sir-auth
- `https://{AUTH_HOST}/session/verify` จากภายนอกได้ **404** — nginx เรียก verify ตรงที่ `AUTH_UPSTREAM` จึงไม่กระทบ
- `map` ของ `$sir_pat` / `$sir_backend_auth` / `$sir_client_ip` อยู่ใน `conf.d/00-sir-auth.conf` (http context)
  เขียนใหม่ทุกครั้งที่ regenerate แม้ไม่มี route ที่ gate หรือ `AUTH_ENABLED=false`
- log การใช้งานบันทึกเฉพาะ request บน route ที่ gate และไม่รู้ status ที่ backend ตอบ (auth_request เห็นแค่ขาเข้า)
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


## Live system status

The default dashboard at `https://proxy.sir-labs.com/` shows the service dependency
map, Docker health and resource samples, Netdata host CPU/RAM/root disk, and
authenticated application path checks. Existing routes remain at `/routes/view`
(HTML) and `/routes` (JSON); `/api/status` is the cached monitoring snapshot.
The existing sir-auth gateway still protects all these public paths.

- Collects read-only Docker inventory, inspection and one-shot stats about every
  10–20 seconds, with a 25-second collection deadline and six concurrent Docker calls.
- `running` without a healthcheck is **not verified**, not healthy. Stopped
  containers remain visible (including intentionally stopped older projects).
  Deleted containers previously seen are retained as missing until watcher restart.
- CPU is percent of one core per container; RAM excludes inactive filesystem cache;
  network rates use differences between consecutive samples. First samples and
  reset counters are unknown, not zero. Netdata host metrics are separate and
  rejected when more than 60 seconds old.
- Browser marks the entire view stale after 45 seconds without fresh data or on
  API failure. A failed Docker inventory fetch never refreshes the old timestamp.
- Graph lines describe configured dependencies, not observed per-request traces.
  Core mappings cover ingress/auth/PostgreSQL/MCP/OCR/RabbitMQ/worker; other HTTP
  routes are discovered from Docker labels. Select all services for the full map.
- Independent path checks cover auth HTTP health, authenticated gateway→OCR health,
  gateway→MCP initialize/tools-list, and public HTTPS→MCP initialization. These
  checks never submit OCR work. They do not assert OCR inference correctness or
  replace queue-depth/job-progress telemetry.

For authenticated probes provision `~/.config/sir-server/monitor/token` on the
Docker host, mode 0600 (parent directory 0700), containing a valid sirpat token.
The directory is mounted read-only; the credential is read each cycle, so rotation
needs no restart. Never commit it. Missing credentials show unknown probes; expired
credentials show failures. Probe results contain no token, response bodies, env
values or healthcheck logs. Set `monitor.exclude=true` to omit temporary containers.

For a read-only preview, start the watcher with `MONITOR_ONLY=true` and the Docker
socket mounted read-only. This mode does not generate nginx configs or connect
containers to networks. Bind the preview to loopback and keep it off public routes.
Tests: `cd gateway/watcher && go test -race ./... && go vet ./...`. The Docker
build also runs the Go tests before producing the runtime image.

Browser verification: install Playwright, then run `python tests/browser_check.py`.
It defaults to `http://127.0.0.1:8080`; `STATUS_URL` selects another target,
`SIR_PAT` authenticates HTTPS checks, and `STATUS_EVIDENCE_DIR` selects the screenshot
folder. `PLAYWRIGHT_CHROMIUM_EXECUTABLE` optionally selects a local Chromium binary.
The check covers desktop/mobile, node selection, filtering, all-service flow,
and simulated stale/failed data without stopping production services.

CI deploys only sir-watcher when a push changes only watcher files and this
workflow. Other changes use the full Compose deployment without forced recreation.
Both paths use the shared deployment lock and verify that the status API collects
a nonempty snapshot.
