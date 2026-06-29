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

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `DOMAIN` | `sir-labs.com` | base domain สำหรับ auto hostname |
| `NGINX_CONTAINER` | `sir-nginx` | container name ของ nginx |
| `PROXY_NETWORK` | `sir-server_sir-net` | Docker network ที่ใช้ connect container |
| `CONF_DIR` | `/etc/nginx/conf.d` | path สำหรับเขียน nginx config |
| `DASHBOARD_PORT` | `8080` | port ของ dashboard HTTP server |

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
