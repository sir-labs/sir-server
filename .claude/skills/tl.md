---
description: Load full context for working with the LANTA HPC cluster (tl/sl nodes): SSH connection, directory navigation, tmux rules, GPU specs, SLURM partitions, conda environments, and SBATCH templates.
---

Load full context for working with the tl SSH server and LANTA HPC cluster, then help with the following task:

$ARGUMENTS

---

## SSH Connection

- **Host alias:** `tl` → `transfer.lanta.nstda.or.th`, user: `teiamarj`
- **Key:** `~/.ssh/backup_key/id_rsa`
- **Login node:** `sl` → `lanta.nstda.or.th` (for sbatch/squeue)

---

## Navigation on tl

**Main working folder:** `/lustrefs/disk/project/lt200203-aimedi/puem/tmp`

Use `z` (sourced from `~/z.sh`) to jump directories:
```bash
source ~/z.sh
z <keyword>       # cd to most frecent match
z -l <keyword>    # list matches without cd-ing
```

Top frecent paths:
| Path | Notes |
|------|-------|
| `/lustrefs/disk/home/teiamarj` | home |
| `/lustrefs/disk/project/lt200203-aimedi/puem/dms` | DMS project |
| `/lustrefs/disk/project/lt200203-aimedi/puem` | main puem folder |
| `/lustrefs/disk/project/lt200384-ff_bio/puem/ocr/ocr_dicom/cli-ocr-models` | OCR models |
| `/lustrefs/flash/scratch/lt200384-ff_bio/puem` | fast scratch |

Storage layout:
- `/lustrefs/disk/project/lt200203-aimedi/` — AI medical imaging (aimedi)
- `/lustrefs/disk/project/lt200384-ff_bio/` — bioinformatics / face recognition
- `/lustrefs/flash/scratch/lt200203-aimedi/` — fast scratch (temporary)

---

## tmux Rule (REQUIRED before send-keys)

**NEVER** run `ssh tl 'tmux send-keys ...'` without checking for an active session first.

```bash
# Step 1: check sessions
ssh tl 'tmux ls 2>/dev/null'

# Step 2a: session exists → send-keys
ssh tl 'tmux send-keys -t 0 "command" Enter'

# Step 2b: no session → create then send-keys
ssh tl 'tmux new-session -d -s main'
ssh tl 'tmux send-keys -t main "command" Enter'
```

- Default target: `0` (attached session), fallback: `main`
- Use plain `ssh tl '...'` for read-only queries (`ls`, `cat`, `tmux ls`, `squeue`) where output comes back to Claude
- Use send-keys only for commands the user wants to see interactively

---

## LANTA GPU Hardware

- **GPU:** NVIDIA A100-SXM4-40GB (40 GB VRAM)
- **CUDA:** 12.7, Driver: 565.57.01
- **Test node:** `lanta-g-175`

---

## SLURM Account & Partitions

- **Account:** `lt200203` (always pass `-A lt200203`)

| Partition | Time limit | Notes |
|-----------|-----------|-------|
| `gpu-devel` | 2h | quick tests, usually idle nodes |
| `gpu` | 5d | production |
| `gpu-limited` | 1d | limited access |
| `compute` | 5d | CPU only |
| `compute-devel` | 2h | CPU testing |
| `compute-long` | 10d | long CPU jobs |

Aliases on tl:
```bash
q    # squeue -u $USER
qo   # myquota
```

---

## SBATCH Templates

### gpu-devel (quick test)
```bash
#!/bin/bash
#SBATCH -p gpu-devel
#SBATCH -N 1 -c 4
#SBATCH --gpus=1
#SBATCH --ntasks-per-node=1
#SBATCH -t 0:10:00
#SBATCH -A lt200203
#SBATCH -J <job-name>
#SBATCH --output=/lustrefs/disk/project/lt200203-aimedi/puem/tmp/<name>-%j.out
```

### gpu (production)
```bash
#!/bin/bash
#SBATCH -p gpu
#SBATCH -N 1 -c 16
#SBATCH --gpus=1
#SBATCH --ntasks-per-node=1
#SBATCH -t 8:00:00
#SBATCH -A lt200203
#SBATCH -J <job-name>
#SBATCH --output=./logs-gpu-%j.out
```

### Jupyter on gpu-devel
Script at: `/lustrefs/disk/project/lt200203-aimedi/puem/tmp/run-jupyter.sh`
After submit, check log for SSH tunnel:
```
ssh -L <port>:<node>:<port> teiamarj@transfer.lanta.nstda.or.th -i id_rsa
```

---

## Tools & Environments

```bash
# Load modules before conda
module purge && module load Mamba FFmpeg cuda

# Tools
uv    → /home/teiamarj/.local/bin/uv
mise  → /home/teiamarj/.local/bin/mise
```

| Conda env path | Notes |
|----------------|-------|
| `/lustrefs/disk/project/lt200203-aimedi/puem/envs/env_test` | general test |
| `/lustrefs/disk/project/lt200203-aimedi/puem/envs/env_yolo` | YOLO |
| `/lustrefs/disk/modules/easybuild/software/Mamba/23.11.0-0/envs/pytorch-2.2.2` | system PyTorch |
| `/lustrefs/disk/modules/easybuild/software/Mamba/23.11.0-0/envs/lightning-2.2.5` | system Lightning |

Reference scripts: `/lustrefs/disk/project/lt200203-aimedi/pung/run-gpu.sh`
