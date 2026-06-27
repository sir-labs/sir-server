---
description: Load full context for submitting and managing SLURM jobs on LANTA HPC. Covers network constraints, directory conventions, step-by-step submission workflow, monitoring, log retrieval, and conda environments.
---

Load full context for submitting and managing SLURM jobs on LANTA, then help with the following task:

$ARGUMENTS

---

## CRITICAL: Network Constraints

**Compute nodes have NO internet access.** This affects all installation work:

| Task | Where to run | Why |
|------|-------------|-----|
| `conda create` | `tl` (transfer node) | needs internet for packages |
| `pip install` | `tl` (transfer node) | needs internet for PyPI |
| `git clone` | `tl` (transfer node) | needs internet for GitHub |
| `pip install <local.whl>` | sbatch job | wheel already on disk, no internet needed |
| Training / inference | sbatch job | needs GPU |

**Rule:** Never put `pip install`, `conda install`, or `git clone` inside an sbatch script unless the packages/repos are already cached locally on Lustre.

### Installing on tl via tmux

```bash
# Step 1: check session
ssh tl "tmux ls 2>/dev/null"

# Step 2: send install commands (module + conda + pip)
ssh tl "tmux send-keys -t 0 'module purge && module load Mamba && conda create -p /path/to/env python=3.10 -y' Enter"
ssh tl "tmux send-keys -t 0 'conda activate /path/to/env && pip install torch ...' Enter"
```

### flash-attn special case (needs CUDA to compile)

Download wheel on tl, compile/install in sbatch job:

```bash
# On tl: download source (no compile yet)
pip download "flash-attn==2.5.5" --no-deps -d /project/.../wheels/

# In sbatch script (CUDA available, no internet needed):
module load Mamba FFmpeg cuda
conda run -p $ENV pip install /project/.../wheels/flash_attn*.tar.gz --no-build-isolation
```

---

## Directory Convention

All jobs use a timestamped folder. Generate the timestamp at submission time (local machine):

```bash
JOB_NAME="<job_name>"
DATETIME=$(date +%Y%m%d-%H%M%S)
REMOTE_BASE="/project/lt200203-aimedi/puem/tmp/launching"
JOB_DIR="${REMOTE_BASE}/${JOB_NAME}-${DATETIME}"
LOG_DIR="${REMOTE_BASE}/logs/${JOB_NAME}-${DATETIME}"
```

- **Job folder:** `/project/lt200203-aimedi/puem/tmp/launching/<job_name>-<YYYYMMDD-HHMMSS>/`
- **Logs folder:** `/project/lt200203-aimedi/puem/tmp/launching/logs/<job_name>-<YYYYMMDD-HHMMSS>/`

---

## Submission Workflow (step by step)

### Step 1 — Create remote directories

```bash
ssh tl "mkdir -p ${JOB_DIR} ${LOG_DIR}"
```

### Step 2 — Upload script and supporting files

```bash
# Single script
scp /local/path/to/script.sh tl:${JOB_DIR}/

# Entire local folder (if needed)
scp -r /local/folder/ tl:${JOB_DIR}/
```

### Step 3 — Verify the script has correct SBATCH headers

The script must include these lines (replace placeholders):

```bash
#!/bin/bash
#SBATCH -p gpu-devel              # or gpu, gpu-limited, compute, etc.
#SBATCH -N 1 -c 4
#SBATCH --gpus=1
#SBATCH --ntasks-per-node=1
#SBATCH -t 0:10:00
#SBATCH -A lt200203               # ALWAYS required
#SBATCH -J <job_name>
#SBATCH --output=${LOG_DIR}/<job_name>-%j.out
#SBATCH --error=${LOG_DIR}/<job_name>-%j.err

module purge
module load Mamba

conda run -p /path/to/conda/env python script.py
# OR: conda activate then run (see Environments section)
```

> `--output` and `--error` must use the `${LOG_DIR}` path. Use `%j` for job ID substitution.

### Step 4 — Submit via login node (sl)

**IMPORTANT:** `sbatch` must run on `sl` (login node), not `tl` (transfer node).

```bash
JOB_ID=$(ssh sl "sbatch ${JOB_DIR}/script.sh" | awk '{print $NF}')
echo "Submitted job ID: ${JOB_ID}"
```

Record the `JOB_DIR`, `LOG_DIR`, and `JOB_ID` — needed for monitoring and log retrieval.

---

## Monitor Job Status

```bash
# Running jobs
ssh sl "squeue -u teiamarj -j ${JOB_ID}"

# All user jobs
ssh sl "squeue -u teiamarj"

# Completed job details
ssh sl "sacct -j ${JOB_ID} --format=JobID,JobName,State,ExitCode,Elapsed,Start,End"
```

---

## Retrieve Logs

### Static (job finished or check snapshot)

```bash
ssh tl "cat ${LOG_DIR}/<job_name>-${JOB_ID}.out"
ssh tl "cat ${LOG_DIR}/<job_name>-${JOB_ID}.err"
```

### Live tail (job still running) — tmux required

```bash
# Step 1: check for existing tmux session
ssh tl "tmux ls 2>/dev/null"

# Step 2a: session exists → send tail command
ssh tl "tmux send-keys -t 0 'tail -f ${LOG_DIR}/<job_name>-${JOB_ID}.out' Enter"

# Step 2b: no session → create then send
ssh tl "tmux new-session -d -s main"
ssh tl "tmux send-keys -t main 'tail -f ${LOG_DIR}/<job_name>-${JOB_ID}.out' Enter"
```

---

## Cancel a Job

```bash
ssh sl "scancel ${JOB_ID}"
```

---

## Module & Conda Environments

Always load modules before using conda:

```bash
module purge && module load Mamba
```

### Available conda envs

| Path | Use case |
|------|----------|
| `/project/lt200203-aimedi/puem/envs/env_test` | general test |
| `/project/lt200203-aimedi/puem/envs/env_yolo` | YOLO |
| `/lustrefs/disk/modules/easybuild/software/Mamba/23.11.0-0/envs/pytorch-2.2.2` | system PyTorch |
| `/lustrefs/disk/modules/easybuild/software/Mamba/23.11.0-0/envs/lightning-2.2.5` | system Lightning |

### Usage patterns inside sbatch script

```bash
# Option A: conda run (no activation needed)
module purge && module load Mamba
conda run -p /project/lt200203-aimedi/puem/envs/env_test python train.py

# Option B: source activate
module purge && module load Mamba
source activate /project/lt200203-aimedi/puem/envs/env_test
python train.py
```

---

## SLURM Partitions

| Partition | Time limit | Notes |
|-----------|-----------|-------|
| `gpu-devel` | 2h | quick tests, usually idle |
| `gpu` | 5d | production |
| `gpu-limited` | 1d | limited access |
| `compute` | 5d | CPU only |
| `compute-devel` | 2h | CPU testing |
| `compute-long` | 10d | long CPU |

Always pass `-A lt200203`.

---

## Common Errors

| Symptom | Fix |
|---------|-----|
| `sbatch: command not found` | You're on `tl` — switch to `ssh sl` |
| conda env not found | Run `module load Mamba` first |
| Log file missing after job starts | Check `--output` path is absolute and dir exists |
| Job stuck in PD (pending) | Check partition with `squeue -u teiamarj --start` |
