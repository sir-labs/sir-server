---
description: Launch a 4-pane tmux split-screen development layout with Gemini, GitHub Copilot, Codex, and a free shell. Use when setting up a multi-AI development workspace.
---

# AI Dev Split Screen (4 Panes)

Launch a 4-pane tmux layout:
- Top-left: Gemini `--yolo`
- Top-right: GitHub Copilot
- Bottom-left: Codex `--approval-mode full-auto`
- Bottom-right: Shell (free terminal)

```
┌──────────────────┬──────────────────┐
│  gemini --yolo   │  gh copilot      │
│  (top-left)      │  suggest         │
├──────────────────┼──────────────────┤
│  codex           │  shell           │
│  --approval full │  (free)          │
└──────────────────┴──────────────────┘
```

## Execution

```bash
PROJECT_DIR=$(pwd)
WINDOW_NAME="aidev-$(date +%s)"

# Create new window — pane 0 (top-left)
tmux new-window -n "$WINDOW_NAME" -c "$PROJECT_DIR"

# Pane 0: Gemini yolo (top-left)
tmux send-keys -t "$WINDOW_NAME" "gemini --yolo" Enter

# Split right → pane 1 (top-right): Copilot
tmux split-window -h -t "$WINDOW_NAME" -c "$PROJECT_DIR"
tmux send-keys -t "$WINDOW_NAME" "gh copilot suggest" Enter

# Split pane 0 down → pane 2 (bottom-left): Codex
tmux select-pane -t "$WINDOW_NAME.0"
tmux split-window -v -t "$WINDOW_NAME.0" -c "$PROJECT_DIR"
tmux send-keys -t "$WINDOW_NAME" "codex --approval-mode full-auto" Enter

# Split pane 1 down → pane 3 (bottom-right): free shell
tmux select-pane -t "$WINDOW_NAME.1"
tmux split-window -v -t "$WINDOW_NAME.1" -c "$PROJECT_DIR"

# Even out pane sizes
tmux select-layout -t "$WINDOW_NAME" tiled

# Focus Gemini (top-left)
tmux select-pane -t "$WINDOW_NAME.0"

echo "4-pane AI dev screen ready: $WINDOW_NAME"
```

If `$ARGUMENTS` is provided, wait 3 seconds then send the prompt to Gemini:
```bash
sleep 3
tmux send-keys -t "$WINDOW_NAME.0" "$ARGUMENTS" Enter
```
