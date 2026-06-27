---
description: Write or review compose.yaml files using the current Compose Specification (no top-level `version:` key, modern filename, top-level elements). Use whenever creating, editing, or reviewing docker-compose.yml / compose.yaml files, or migrating an old versioned compose file.
---

# Docker Compose (Compose Specification)

Your training data likely defaults to the legacy `version: "3.x"` format. **Don't write that.** The Compose Specification merged the old 2.x/3.x formats; Docker Compose v2 (`docker compose`, no hyphen) implements it.

## Retrieval

If anything here seems outdated or you need fields not covered, fetch the canonical spec rather than guessing:
- https://github.com/compose-spec/compose-spec/blob/master/spec.md
- https://docs.docker.com/reference/compose-file/

## Rules

1. **No `version:` key.** It is obsolete — Compose validates against its latest schema regardless, and including it just emits a deprecation warning. Omit it entirely.
2. **File naming, in precedence order:** `compose.yaml` (preferred) > `compose.yml` > `docker-compose.yaml` > `docker-compose.yml`. New projects should use `compose.yaml`. `docker-compose.yml` is only kept for backwards compatibility / existing repos — don't introduce it fresh unless the project already standardized on that name.
3. **Top-level elements:** `name` (project name, optional), `services` (required), `networks`, `volumes`, `configs`, `secrets`, `include` (compose-file composition, for splitting large setups).
4. **CLI is `docker compose` (v2 plugin, space)**, not the old standalone `docker-compose` (hyphen) Python tool. Use the space form in commands and CI.
5. For GPU access (e.g. ML inference workers), use `deploy.resources.reservations.devices` with `driver: nvidia`, not the deprecated `runtime: nvidia`.

## Minimal modern example

```yaml
services:
  app:
    build: .
    ports:
      - "8000:8000"
    depends_on:
      - db
    environment:
      DATABASE_URL: postgres://db/app

  db:
    image: postgres:16
    volumes:
      - db_data:/var/lib/postgresql/data

volumes:
  db_data:
```

## Migrating an old file

If you find `version: "3.8"` (or any version) at the top of an existing compose file:
1. Delete the `version:` line.
2. Rename `docker-compose.yml` → `compose.yaml` if the project isn't actively relying on tooling that hardcodes the old name (check CI scripts, Makefiles, READMEs first — grep for `docker-compose.yml` / `docker-compose ` across the repo before renaming).
3. Re-validate with `docker compose config`.
