---
description: Load context for delegating tasks to other AI CLI tools (Gemini, Codex, GitHub Copilot) via tmux. Use when you want to delegate work to another AI tool or run multi-agent workflows.
---

Load context for delegating to other AI CLI tools, then help with the following task:

$ARGUMENTS

---

## Available AI CLI Tools

| Tool | Binary | Mode |
|------|--------|------|
| Gemini CLI | `/opt/homebrew/bin/gemini` | `--yolo` (auto-accept all edits) |
| Codex CLI | `codex` | `--approval-mode full-auto` |
| GitHub Copilot CLI | `gh copilot suggest` | interactive |

**Priority order:** Gemini → Copilot → Codex

---

## Launch Commands (always use these exact flags)

```bash
# Gemini — YOLO mode
tmux new-window -n "gemini-<slug>" -c "<project-dir>" "gemini --yolo"

# Codex — full-auto approval
tmux new-window -n "codex-<slug>" -c "<project-dir>" "codex --approval-mode full-auto"

# Copilot — interactive
tmux new-window -n "copilot-<slug>" -c "<project-dir>" "gh copilot suggest"
```

---

## Delegation Workflow

```bash
# 1. Open new tmux window with launch command
# 2. Wait ~3s, then confirm ready:
tmux capture-pane -t <window> -p

# 3. Send prompt:
tmux send-keys -t <window> "<prompt>" Enter

# 4. Monitor every 10-20s until done:
tmux capture-pane -t <window> -p
```

**Why YOLO/full-auto:** avoids repeated permission prompts mid-task. New window per tool keeps workspace clean.
