---
description: Load Tauri v2 reference for answering questions about IPC commands, events, windows, plugins, permissions, state management, system tray, mobile, and build/distribution. Use when working on Tauri desktop or mobile apps.
---

You are now operating with the Tauri v2 reference loaded. Use the documentation below to answer Tauri questions accurately without fetching external docs.

$ARGUMENTS

---

# Tauri v2 Reference (v2.tauri.app)

## Project Setup

```bash
pnpm tauri init
```

### Directory Structure
```
tauri-app/
├── index.html
├── package.json
├── src/
└── src-tauri/
    ├── Cargo.toml
    ├── capabilities/
    │   └── <identifier>.json
    ├── src/
    └── tauri.conf.json
```

Config files: `tauri.conf.json` (app/window/build), `Cargo.toml` (Rust deps), `capabilities/*.json` (ACL permissions).

---

## Commands (Rust ↔ JS IPC)

### Define in Rust (`src-tauri/src/lib.rs`)
```rust
#[tauri::command]
fn my_command() { println!("invoked!"); }

#[tauri::command]
fn greet(name: String) -> String { format!("Hello, {}!", name) }

pub fn run() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![my_command, greet])
        .run(tauri::generate_context!())
        .expect("error");
}
```
> Commands in `lib.rs` cannot be `pub` (glue code limitation).

### Invoke from JavaScript
```typescript
import { invoke } from '@tauri-apps/api/core';
invoke('my_command');
const result = await invoke<string>('greet', { name: 'World' });
```
All args/return values must be JSON-serializable (uses JSON-RPC under the hood).

---

## Events

### JS side
```typescript
import { listen, emit } from '@tauri-apps/api/event';

const unlisten = await listen<string>('error', (event) => {
    console.log(event.payload);
});
unlisten(); // call on cleanup

emit('download-started', { url: '...', contentLength: 1024 });
```

### Rust side
```rust
use tauri::Listener;

app.listen("download-started", |event| {
    if let Ok(payload) = serde_json::from_str::<DownloadStarted>(&event.payload()) {
        println!("{}", payload.url);
    }
});

app.once("ready", |_| { println!("ready"); });

let id = app.listen("status", |_| {});
app.unlisten(id);
```

### Window-scoped events
```rust
use tauri::{Listener, Manager};
let webview = app.get_webview_window("main").unwrap();
webview.listen("logged-in", |event| { let token = event.data; });
```
```typescript
import { getCurrentWindow } from "@tauri-apps/api/window";
getCurrentWindow().listen("my-event", ({ event, payload }) => {});
```

---

## Windows & Webviews

### tauri.conf.json
```json
{ "app": { "windows": [
  { "label": "first",  "title": "First",  "width": 800, "height": 600 },
  { "label": "second", "title": "Second", "width": 800, "height": 600 }
]}}
```

### Runtime (JS)
```typescript
import { WebviewWindow } from '@tauri-apps/api/webviewWindow';
const win = new WebviewWindow('my-label', { url: 'https://...' });
win.once('tauri://created', () => {});
win.once('tauri://error', (e) => {});
await win.emit("event", "data");
const unlisten = await win.listen("event", e => {});

import { getAllWebviewWindows } from '@tauri-apps/api/webviewWindow';
const all = await getAllWebviewWindows();
```

### Separate Window + Webview
```typescript
import { Window } from "@tauri-apps/api/window";
import { Webview } from "@tauri-apps/api/webview";

const appWindow = new Window('uniqueLabel');
appWindow.once('tauri://created', async () => {
    const webview = new Webview(appWindow, 'theUniqueLabel', {
        url: 'path/to/page.html',
        x: 0, y: 0, width: 800, height: 600,
    });
});
```

---

## Plugins

### Add plugin
```bash
pnpm tauri add <plugin>   # e.g. fs, dialog, notification, updater, shell
```

### Common plugins
| Plugin | Package |
|--------|---------|
| File System | `@tauri-apps/plugin-fs` |
| Dialog | `@tauri-apps/plugin-dialog` |
| Notification | `@tauri-apps/plugin-notification` |
| Updater | `@tauri-apps/plugin-updater` |
| Shell | `@tauri-apps/plugin-shell` |
| Process | `@tauri-apps/plugin-process` |

### Updater
```rust
// Cargo.toml: tauri-plugin-updater = "2"
tauri::Builder::default()
    .plugin(tauri_plugin_updater::Builder::new().build())
```
```typescript
import { check } from '@tauri-apps/plugin-updater';
import { relaunch } from '@tauri-apps/plugin-process';
const update = await check();
if (update?.available) {
    await update.downloadAndInstall();
    await relaunch();
}
```

### Custom plugin
```bash
cargo tauri plugin init my-plugin
cargo tauri plugin init my-plugin --no-api    # no TS bindings
cargo tauri plugin init my-plugin --mobile    # Android + iOS
```

---

## Permissions & Capabilities

Replaces v1 allowlist. Three concepts: **permissions** (on/off), **scopes** (param validation), **capabilities** (bind to windows).
All dangerous commands are **blocked by default**.

### `src-tauri/capabilities/main.json`
```json
{
  "$schema": "../gen/schemas/desktop-schema.json",
  "identifier": "main-capability",
  "description": "Capability for the main window",
  "windows": ["main"],
  "permissions": [
    "core:window:default",
    "core:window:allow-start-dragging",
    "fs:allow-read-text-file",
    "dialog:allow-open"
  ]
}
```

---

## State Management

### Register (Rust)
```rust
use tauri::{Builder, Manager};
struct AppData { message: &'static str }

Builder::default()
    .setup(|app| {
        app.manage(AppData { message: "Hello" });
        Ok(())
    })
    .run(tauri::generate_context!())
    .unwrap();
```

### Access in commands
```rust
struct MyState(String);

#[tauri::command]
fn my_cmd(state: tauri::State<MyState>) { println!("{}", state.0); }

// Register: .manage(MyState("value".into()))
```

### Access via Manager (event handlers / threads)
```rust
use std::sync::Mutex;
use tauri::{Manager, Window, WindowEvent};

#[derive(Default)]
struct AppState { counter: u32 }

fn on_window_event(window: &Window, _: &WindowEvent) {
    let state = window.app_handle().state::<Mutex<AppState>>();
    state.lock().unwrap().counter += 1;
}
```

---

## System Tray

### Rust
```rust
use tauri::{ menu::{Menu, MenuItem}, tray::TrayIconBuilder };

let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
let menu = Menu::with_items(app, &[&quit])?;
let tray = TrayIconBuilder::new()
    .menu(&menu)
    .show_menu_on_left_click(true)
    .build(app)?;
```

### JavaScript
```typescript
import { TrayIcon } from '@tauri-apps/api/tray';
import { Menu } from '@tauri-apps/api/menu';

const menu = await Menu.new({ items: [{ id: 'quit', text: 'Quit' }] });
const tray = await TrayIcon.new({ menu, menuOnLeftClick: true });
```

Key properties: `icon`, `tooltip`, `title` (Linux), `iconAsTemplate` (macOS), `showMenuOnLeftClick`.
Enable `image-png` Cargo feature for PNG icons.

---

## Mobile (iOS / Android)

```bash
cargo tauri android init / ios init
cargo tauri android dev  / ios dev      # runs on device/emulator
cargo tauri android build --apk         # APK
cargo tauri android build --aab         # AAB
cargo tauri ios build --target aarch64-sim --open
```

Android targets: `aarch64`, `armv7`, `i686`, `x86_64`. Use `--split-per-abi` for per-ABI APKs.
iOS targets: `aarch64`, `aarch64-sim`, `x86_64`. `--export-method`: `app-store-connect`, `release-testing`, `debugging`.
First build is slow (Rust compile); subsequent builds use cache.

---

## Build & Distribute

```bash
pnpm tauri build                              # release build + bundle
pnpm tauri build -- --debug                  # debug
pnpm tauri build -- --bundles deb,appimage   # Linux
pnpm tauri build -- --bundles app,dmg        # macOS

# Split build + bundle
pnpm tauri build -- --no-bundle
pnpm tauri bundle -- --bundles app,dmg
pnpm tauri bundle -- --bundles app --config src-tauri/tauri.appstore.conf.json
```

Key options:
- `-t <TARGET>` — cross-compile (e.g. `universal-apple-darwin`)
- `--no-sign` — skip signing
- `--skip-stapling` — skip notarization stapling (macOS)
- `-c <CONFIG>` — merge extra config (platform-specific: `tauri.macos.conf.json`, `tauri.windows.conf.json`, `tauri.linux.conf.json`)

macOS CI signing: use `tauri-apps/tauri-action@v0` with secrets `APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_ID`, `APPLE_PASSWORD`, `KEYCHAIN_PASSWORD`.

---

## CLI Quick Reference

| Command | Description |
|---------|-------------|
| `tauri init` | Init Tauri in existing dir |
| `tauri dev` | Start dev server |
| `tauri build` | Release build + bundle |
| `tauri bundle` | Bundle only (after build) |
| `tauri add <plugin>` | Add a plugin |
| `tauri remove <plugin>` | Remove a plugin |
| `tauri plugin new <name>` | New plugin project |
| `tauri plugin init <name>` | Init plugin in existing dir |
| `tauri android init/dev/build/run` | Android lifecycle |
| `tauri ios init/dev/build/run` | iOS lifecycle |
