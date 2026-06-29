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

2. **Create the compose file** with proxy labels:
   ```yaml
   services:
     my-service:
       image: myimage
       container_name: my-service
       labels:
         proxy.enable: "true"           # required — opt-in to reverse proxy
         proxy.port: "8000"             # optional — internal port (default: 80)
         proxy.host: "myapp.sir-labs.com"  # optional — custom hostname (default: <container_name>.sir-labs.com)
       restart: unless-stopped
   ```
   - `proxy.enable` is the only required label.
   - `proxy.port` must match the port the container listens on internally — **not** a host-published port.
   - `proxy.host` supports any hostname including apex `sir-labs.com`.
   - Do **not** add `ports:` mapping — the reverse proxy reaches it via Docker network.
   - Do **not** add `networks:` — the watcher auto-connects the container.

3. **Register in root compose.yaml** by adding an `include` entry:
   ```yaml
   include:
     - gateway/nginx/compose.yaml
     - gateway/watcher/compose.yaml
     - gateway/cloudflared/compose.yaml
     - <group>/<service>/compose.yaml   # ← add here
   ```

4. **Set a meaningful `container_name`** — it becomes the default subdomain if `proxy.host` is not set.
   Use lowercase, hyphens only (e.g. `my-api` → `my-api.sir-labs.com`).

5. **Ensure sir-server is running**:
   ```bash
   cd ~/Desktop/sir-server && docker compose up -d
   ```

6. **Verify**:
   ```bash
   curl -H "Host: myapp.sir-labs.com" http://localhost
   curl https://myapp.sir-labs.com
   ```

7. Check active routes at `https://proxy.sir-labs.com`.

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
