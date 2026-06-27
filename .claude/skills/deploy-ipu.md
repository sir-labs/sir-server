---
name: deploy-ipu
description: Rules for deploying a project to the ipu server. Use whenever the user says "deploy to ipu server", "deploy ipu", or asks to create/update a compose file for the ipu deploy server.
triggers:
  - deploy ipu
  - deploy to ipu server
  - compose.ipu.yaml
---

# Deploy to ipu server

When asked to deploy a project to the ipu server, do this:

1. Create `compose.ipu.yaml` in the project root.
2. If the project already has a main compose file (`compose.yaml` / `docker-compose.yaml`), copy its services into `compose.ipu.yaml` as the starting point (as an override file, or a full copy — match whatever the project's existing compose layering convention is, e.g. `compose.gpu.yaml`/`compose.cpu.yaml` pattern).
3. For every service, add `container_name`. For services on `ipuserver_webserver` (user-accessible), keep the name short and descriptive (e.g. `sme-image-search` instead of `sme-image-search-api-producer-prod`) — it's what reverse-proxy configs and humans will reference.
4. For every service, attach it to a network based on reachability:
   - Internal-only communication (e.g. db, queue, internal workers) → `ipuserver_internal`
   - Externally user-accessible (e.g. web/API entrypoints) → `ipuserver_webserver`
   - A service can be on both networks if it needs to talk internally and also be reached externally.
5. Declare both networks at the bottom of `compose.ipu.yaml`:
   ```yaml
   networks:
     ipuserver_internal:
       external: true
     ipuserver_webserver:
       external: true
   ```
   (assume these networks already exist on the ipu server unless told otherwise).
6. If the consumer (or any service) needs a GPU, use `device_ids: ["0"]` on the ipu server. Note that compose merges `deploy.resources.reservations.devices` lists by appending across files rather than replacing — so make `compose.ipu.yaml`'s GPU service self-contained (include its own `build` + `deploy.resources`) instead of layering on top of another GPU override file (e.g. `compose.gpu.yaml`), or the device list will end up with both GPU ids.
7. Check whether the project bakes large model/weight files into the image with `COPY` (look for `*.pth`, `*.pt`, `*.safetensors`, `*.bin`, or a `models/`-style dir referenced in a Dockerfile `COPY` and in app code, e.g. `Path(__file__).parents[n] / "models" / "..."`). If found:
   - Add a `.dockerignore` excluding that model directory so it's not baked into the image.
   - Add a bind-mount `volumes:` entry for that directory in `compose.yaml` (or `compose.ipu.yaml`) instead, e.g. `./consumer/models:/app/consumer/models:ro`.
   - This keeps images small and lets the model be swapped without a rebuild.

See `commands/ssh.md` for ipu server SSH connection details (host alias `ipu`, `10.222.44.224`).
