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

1. Check if the project has a compose file (`compose.yaml` / `docker-compose.yaml`). If yes, use it as the starting point.

2. Add proxy labels to the service that should be publicly accessible:
   ```yaml
   labels:
     proxy.enable: "true"           # required — opt-in to reverse proxy
     proxy.port: "8000"             # optional — container's internal port (default: 80)
     proxy.host: "myapp.sir-labs.com"  # optional — custom hostname (default: <container_name>.sir-labs.com)
   ```
   - `proxy.enable` is the only required label.
   - `proxy.port` must match the port the container listens on internally — **not** a host-published port.
   - `proxy.host` supports any hostname including apex `sir-labs.com` itself.
   - Do **not** add `ports:` mapping for the proxied service — the reverse proxy reaches it via Docker network.

3. Set a meaningful `container_name` — it becomes the default subdomain if `proxy.host` is not set.
   - Use lowercase, hyphens only (e.g. `my-api` → `my-api.sir-labs.com`).

4. The sir-server watcher auto-connects the container to its network — **no manual network config needed** in the project's compose file.

5. Ensure sir-server is running on the host first:
   ```bash
   cd ~/Desktop/sir-server && docker compose up -d
   ```

6. Start the project:
   ```bash
   docker compose up -d
   ```

7. Verify:
   ```bash
   # Local check
   curl -H "Host: myapp.sir-labs.com" http://localhost

   # Real domain (after a few seconds)
   curl https://myapp.sir-labs.com
   ```

8. Check active routes anytime at `http://localhost:8080` or `https://proxy.sir-labs.com`.

---

## Example

```yaml
services:
  api:
    build: .
    container_name: my-api
    labels:
      proxy.enable: "true"
      proxy.port: "8000"
    restart: unless-stopped
```
→ serves at `https://my-api.sir-labs.com`

```yaml
services:
  web:
    image: nginx:alpine
    container_name: web
    labels:
      proxy.enable: "true"
      proxy.host: "sir-labs.com"   # apex domain
    restart: unless-stopped
```
→ serves at `https://sir-labs.com`
