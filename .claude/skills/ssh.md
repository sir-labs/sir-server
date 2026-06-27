---
description: Load SSH server reference and known host aliases. Use when connecting to remote servers, running SSH commands, or needing host/user/key information.
---

Load the SSH server reference for this user, then help with the following task:

$ARGUMENTS

---

## Known SSH Hosts (~/.ssh/config)

| Alias | Host | User | Notes |
|-------|------|------|-------|
| `sorn` | 203.185.144.35 | sorn | |
| `sl` | lanta.nstda.or.th | teiamarj | LANTA login node, key: `~/.ssh/backup_key/id_rsa` |
| `tl` | transfer.lanta.nstda.or.th | teiamarj | LANTA transfer node, key: `~/.ssh/backup_key/id_rsa` |
| `ipu` | 10.222.44.224 | ipu | |
| `deploy` | 10.222.44.224 | deploy | |
| `admin` | 10.222.44.224 | admin | |
| `home` | h.puem.me | puem | |
| `pv` | 157.85.98.168 | root | |
| `box1` | 192.111.0.102 | www | |
| `box2` | 192.111.0.103 | www | |
| `meow` | 10.222.44.73 | meow-ipu | |

## Usage

```bash
ssh <alias>                            # connect
ssh <alias> <command>                  # run command
ssh -o ConnectTimeout=10 <alias> echo "ok"  # test connectivity
```

For LANTA (tl/sl), always use `/puem-skills:tl` skill for full context on tmux, z navigation, and SLURM.
