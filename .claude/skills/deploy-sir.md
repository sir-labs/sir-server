---
name: deploy-sir
description: Rules for deploying a project to sir-labs.com via the sir-server reverse proxy. Use whenever the user says "deploy to sir-labs", "deploy sir", or asks to expose a service at *.sir-labs.com.
triggers:
  - deploy sir
  - deploy to sir-labs
  - deploy sir-labs
  - sir-labs.com
---

# Deploy to sir-labs.com

When asked to deploy a project to sir-labs.com, do this:

## sir-server structure

Services live under `~/Desktop/sir-server/` with this layout:

```
sir-server/
├── compose.yaml              # root — include only, do not add services here
└── <group>/
    └── <service>/
        └── compose.yaml      # service definition lives here
```

Current groups:
- `gateway/` — core proxy infrastructure (nginx, watcher, cloudflared)
- `service/` — external/user services deployed via sir-server

New services should follow the same pattern: `<group>/<service>/compose.yaml`.

---

## Steps

1. **Pick a location** for the new service's compose file:
   - If it belongs to an existing group, put it there: `<group>/<service>/compose.yaml`
   - If it's an external/user service, put it under `service/<name>/compose.yaml`

2. **Ask which routing mode** if not already specified:
   - **Own subdomain** (default) — `myapp.sir-labs.com`, one hostname per service.
   - **Shared internal path routing** — `internal.sir-labs.com/{port}/`, for internal-only tools that don't need their own subdomain. Visiting `internal.sir-labs.com/` shows an auto-generated index linking every active internal service.

3. **Create the compose file** with proxy labels:

   Own subdomain:
   ```yaml
   services:
     my-service:
       image: myimage
       container_name: my-service
       labels:
         proxy.enable: "true"           # required — opt-in to reverse proxy
         proxy.port: "8000"             # optional — internal port (default: 80)
         proxy.host: "myapp.sir-labs.com"  # optional — custom hostname (default: <container_name>.sir-labs.com)
       healthcheck:                     # optional but recommended — unhealthy containers are pulled from rotation automatically
         test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:8000/"]
         interval: 10s
         timeout: 3s
         retries: 3
       restart: unless-stopped
   ```

   Shared internal path routing:
   ```yaml
   services:
     admin-tool:
       image: myimage
       container_name: admin-tool
       ports:
         - "3001:3001"                  # required in this mode — routing reaches the host, not this container's docker-network IP
       labels:
         proxy.enable: "true"
         proxy.host: "internal.sir-labs.com"
         proxy.port: "3001"             # required — also becomes the URL path: /3001/
       healthcheck:
         test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:3001/"]
         interval: 10s
         timeout: 3s
         retries: 3
       restart: unless-stopped
   ```
   → serves at `https://internal.sir-labs.com/3001/`. Pick any free port; convention here is 3000–3999 for internal-only tools (not enforced, just keeps them visually distinct from public service ports). `proxy.port` must be unique among internal services — it's the path, not just the port. The label is the opt-in and what makes the service appear on the index page — nginx actually proxies to the Docker **host's** `localhost:{port}` (via `host.docker.internal`), not this container's network IP, so the port must be published and the container must bind to more than `127.0.0.1`.

   Own subdomain:
   - `proxy.enable` is the only required label.
   - `proxy.port` must match the port the container listens on internally — **not** a host-published port.
   - Do **not** add `ports:` mapping — the reverse proxy reaches it via the Docker network.
   - Do **not** add `networks:` — the watcher auto-connects the container.

   Shared internal path routing:
   - `proxy.enable`, `proxy.host`, and `proxy.port` are all required.
   - **Do** add a `ports:` mapping publishing `proxy.port` to the same host port.
   - Prefix-stripped (backend sees `/`, not `/{port}/`) — apps with absolute asset paths or client-side routing may break under a prefix. Use a subdomain for those instead.
   - Because `*.sir-labs.com` is wildcard-routed through cloudflared, publishing a port with this label makes it reachable from the public internet, not just the host/LAN — the label is the opt-in.

4. **Register in root compose.yaml** by adding an `include` entry:
   ```yaml
   include:
     - gateway/nginx/compose.yaml
     - gateway/watcher/compose.yaml
     - gateway/cloudflared/compose.yaml
     - <group>/<service>/compose.yaml   # ← add here
   ```

5. **Set a meaningful `container_name`** — it becomes the default subdomain if `proxy.host` is not set (own-subdomain mode only; irrelevant for internal path routing, where the path is `proxy.port`, not the name).
   Use lowercase, hyphens only (e.g. `my-api` → `my-api.sir-labs.com`).

6. **Ensure sir-server is running**:
   ```bash
   cd ~/Desktop/sir-server && docker compose up -d
   ```

7. **Verify**:
   ```bash
   # own subdomain
   curl -H "Host: myapp.sir-labs.com" http://localhost
   curl https://myapp.sir-labs.com

   # shared internal path routing
   curl -H "Host: internal.sir-labs.com" http://localhost/3001/
   curl https://internal.sir-labs.com/          # index page listing active internal services
   ```

8. Check active routes at `https://proxy.sir-labs.com`.

---

## Example

New service `service/httpbin/compose.yaml`:

```yaml
services:
  httpbin:
    image: mccutchen/go-httpbin
    container_name: httpbin
    labels:
      proxy.enable: "true"
      proxy.port: "8080"
    restart: unless-stopped
```

Then in root `compose.yaml`:
```yaml
include:
  - gateway/nginx/compose.yaml
  - gateway/watcher/compose.yaml
  - gateway/cloudflared/compose.yaml
  - service/httpbin/compose.yaml    # ← added
```

→ serves at `https://httpbin.sir-labs.com`
