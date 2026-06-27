---
description: Create a scripts/ folder for projects with FastAPI + Workers + Web + Overmind + PostgreSQL + RabbitMQ structure. Use when setting up dev scripts, creating scripts folder, or setting up project automation.
---

# Dev Scripts Setup Skill

สร้าง `scripts/` folder สำหรับโปรเจ็คที่มีโครงสร้างแบบ:
- FastAPI backend (`producers/`) + workers (`consumers/`) + web frontend (`web/`)
- Overmind + Procfile สำหรับ process management
- PostgreSQL + RabbitMQ via Docker
- Python package manager: `uv` | Node package manager: `pnpm`

## ขั้นตอน

### 1. Detect โครงสร้างโปรเจ็ค

อ่านไฟล์เหล่านี้เพื่อเก็บข้อมูล:
- `Procfile` — ชื่อ services, ports
- `docker-compose*.yml` — infra services, credentials, healthchecks
- `.overmind.env` หรือ `.env.example` — environment variables
- `producers/` — หา migration tool (Alembic vs raw SQL)
- SQLModel models — หา table names สำหรับ clear-data.sh

### 2. รวบรวมข้อมูลที่จำเป็น

ถามผู้ใช้หรือหาจากไฟล์:
- **DB credentials**: user, password, database name
- **RabbitMQ credentials**: user, password
- **Queue name**
- **Compose file สำหรับ infra** (dev): ชื่อไฟล์ docker-compose
- **Migration tool**: Alembic (`alembic upgrade head`) หรือ raw SQL files
- **Table names** สำหรับ `clear-data.sh` (จาก SQLModel `table=True` หรือ `__tablename__`)
- **Env file**: `.overmind.env` หรือ `.env.example`

### 3. สร้างไฟล์ทั้ง 5 ใน `scripts/`

#### `scripts/dev.sh` (หลัก — แทน overmind.sh)
```bash
#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE="docker compose -f $ROOT/<compose-file>"

log()  { echo "==> $*"; }
warn() { echo "[warn] $*"; }

wait_healthy() {
  local service="$1" max=30 i=0
  log "Waiting for $service..."
  until [ "$(docker inspect --format='{{.State.Health.Status}}' "$($COMPOSE ps -q "$service" 2>/dev/null)" 2>/dev/null)" = "healthy" ]; do
    i=$((i+1)); [ "$i" -ge "$max" ] && { echo "[error] $service timeout."; exit 1; }
    sleep 2
  done
  log "$service is ready."
}

# 1. Ensure data dirs
mkdir -p "$ROOT/data/postgres_data" "$ROOT/data/rabbitmq_data" "$ROOT/shared_data"

# 2. Kill port conflicts
for port in 5432 5672 15672; do
  cid=$(docker ps --filter "publish=$port" -q 2>/dev/null)
  [ -n "$cid" ] && { warn "Port $port in use — stopping $cid..."; docker stop "$cid" >/dev/null; }
done

# 3. Tear down + restart infra
$COMPOSE down --remove-orphans 2>/dev/null || true
log "Starting postgres + rabbitmq..."
$COMPOSE up -d postgres rabbitmq
wait_healthy postgres; wait_healthy rabbitmq

# 4. Sync deps
cd "$ROOT/producers" && uv sync --quiet
cd "$ROOT/consumers" && uv sync --quiet
cd "$ROOT/web" && pnpm install --silent

# 5. [Alembic only] Run migrations
# cd "$ROOT/producers" && uv run alembic upgrade head

# 6. Start all services
cd "$ROOT"
export $(grep -v '^#' <env-file> | xargs)
export SHARED_FOLDER="$ROOT/shared_data"
exec overmind start -f Procfile "$@"
```

#### `scripts/setup.sh`
```bash
#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
echo "==> Setting up producers..."; cd "$ROOT/producers" && uv sync
echo "==> Setting up consumers..."; cd "$ROOT/consumers" && uv sync
echo "==> Setting up web...";       cd "$ROOT/web" && pnpm install
echo ""; echo "Done! Run './scripts/dev.sh' to start all services."
```

#### `scripts/migrate.sh`
- **Alembic**: `cd producers && uv run alembic upgrade head`
- **Raw SQL**: loop ผ่าน `producers/migrations/*.sql` แล้ว `docker exec psql < file`

#### `scripts/reset-db.sh`
```
confirm prompt → stop postgres → rm container → rm volume/data dir → recreate → wait_healthy
```

#### `scripts/clear-data.sh`
```
confirm prompt → docker exec psql TRUNCATE TABLE <tables> RESTART IDENTITY CASCADE
```
> ลำดับ TRUNCATE: ต้องขึ้นก่อน — child tables ก่อน parent (FK order)
> `"user"` ต้องใส่ quotes เพราะเป็น reserved word ใน PostgreSQL

### 4. chmod และ cleanup

```bash
chmod +x scripts/*.sh
# ถ้ามี overmind.sh อยู่ก่อน — ถามผู้ใช้ว่าจะลบหรือเก็บไว้
```

### 5. ตรวจสอบ Procfile

ถ้า Procfile ยังใช้ `nc -z localhost 5432` wait pattern (busy-loop) → แนะนำให้เปลี่ยนเป็น healthcheck ใน docker-compose แทน แต่ไม่บังคับแก้ถ้าผู้ใช้ไม่ขอ

---

## ตัวอย่างการใช้งาน

```
# ใน project ใหม่ที่มี Procfile + producers/ + consumers/ + web/
invoke skill: dev-scripts
→ Claude อ่านโครงสร้าง, ถาม credentials ที่หาไม่ได้, สร้าง scripts/ ทั้งหมด
```

## หมายเหตุ

- `user` table ต้องใส่ `"user"` (double quotes) ใน SQL เพราะเป็น PostgreSQL reserved keyword
- ถ้าโปรเจ็คใช้ Alembic: ใส่ `uv run alembic upgrade head` ใน `dev.sh` step 5 และสร้าง `migrate.sh` แบบ Alembic
- ถ้าโปรเจ็คไม่มี consumers/: ตัด uv sync consumers ออก
- ถ้าใช้ `npm` แทน `pnpm`: เปลี่ยน `pnpm install` → `npm install`
