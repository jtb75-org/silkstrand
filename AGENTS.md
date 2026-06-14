# AGENTS.md — multi-agent coordination contract

How the human and AI agents collaborate on SilkStrand. This is the **durable**
contract (roles, process, rules that rarely change). For the rest:

- **CLAUDE.md** — project, architecture, tech stack, deploy/branching details.
- **RESUME.md** — the *point-in-time* handoff (current state, open PRs, immediate
  next steps). Rewritten each session / before a reboot. Read it first on restart.

---

## Roles (persist across sessions; hcom names rotate)

| Role | Responsibility | Tree |
|---|---|---|
| **Orchestrator / Leader** | Plans, gates, **lands** PRs, talks to the human. | main / orchestrator tree |
| **Coder / Implementer** | Writes code on a branch. Does **not** merge. | its own worktree |
| **Reviewer** | Reviews before land. Does **not** merge. | its own /tmp worktree |

Names are ephemeral 4-letter `hcom` handles assigned at spawn — the human refers
to agents by handle. Roles persist; the handle filling a role changes between
sessions (orchestrator has been `kimi` then `dino`; reviewer `hero` then `navi`;
coder `dosa`). **Authority: `@bigboss` (the human) outranks all agents.**

## The loop

**plan / contract-first → coder implements → reviewer reviews → orchestrator gates + lands.**
Agree the contract before coding anything non-trivial. One page/unit of work per
PR keeps review tractable.

## Hard rules

- **No direct commits to `main`** — every change via a `feature/` or `fix/` branch + PR.
- **Branch from FRESH `origin/main`** every time (fetch first).
- **Each coding agent works ONLY in its OWN git worktree — never the orchestrator/main tree.**
  - `dosa`'s persistent worktree: `/Users/joe/repo/silkstrand-dosa` (survives reboots; a
    re-spawned `dosa` reuses it). Reviewers use throwaway `/tmp` worktrees.
- **Only the orchestrator lands.** Coders/reviewers report; they don't merge.
- **Gates before a PR is "ready":** web → `typecheck` + `lint` + `build` + `test` green;
  Go → `gofmt` + `go vet` + `go test` + `go build` clean. State the results in the report.

## hcom gotchas

- **Backticks / code in `hcom send -- "..."` get shell-evaluated.** For any message
  containing code, backticks, or `$`, use `--file <path>` or a heredoc instead.
- Use `--intent` (`request` / `inform` / `ack`). **End your turn to receive messages.**
- Resolve "the gemini/claude/codex agent" with `hcom list` — never guess a handle.

## Environment gotchas (homelab)

- **`kubectl` "no route to host 192.168.0.210:6443" — usually macOS Local Network
  Privacy** (confirmed 2026-06-14), not a route hijack. Homebrew/third-party binaries
  (kubectl, brew `python3`, helm…) get `EHOSTUNREACH` to the homelab LAN; Apple-signed
  `ping`/`nc`/`curl` are exempt, so **ping/nc work while kubectl doesn't**. **The axis is
  GUI session vs SSH session:**
    - **RDP / console** (Ghostty granted) → kubectl WORKS.
    - **SSH session** (`sshd` → zellij → agent → kubectl — i.e. an agent running remotely)
      → no GUI app to carry the grant → BLOCKED. Granting Ghostty fixes only the GUI
      session, **not** SSH.
  **Diagnose:** `echo $SSH_CONNECTION` set + `nc -z 192.168.0.210 6443` succeeds but
  `kubectl` says no-route → it's this. **Fix (SSH session):** `sudo launchctl asuser
  $(id -u) kubectl …` to re-parent into the logged-in GUI session (needs root; may pop a
  one-time allow prompt in the RDP session → Allow) · OR run cluster ops from the RDP/console
  terminal · OR use the public ingress (`app.silkstrand.io`/`api.silkstrand.io`) for HTTP-level
  checks (internet, never blocked). **Fix (GUI session):** System Settings → Privacy &
  Security → Local Network → enable **Ghostty**. A reboot "fixes" the GUI case by re-triggering
  the prompt.
- **Less common: a real colima/Docker route hijack** — if colima is actually running
  (esp. `--network-mode bridged`) it can shadow the LAN route. Then `nc`/`ping` also fail.
  Do kubectl work before starting Docker; reboot or stop colima clears it.
- **Argo CD git cache can go stale** and report `Synced/Healthy` against an old
  revision. If a green build didn't roll out, **hard-refresh** the Argo app rather
  than assuming the deploy failed. (Also noted in CLAUDE.md.)

## On restart (orchestrator checklist)

1. Read **RESUME.md** — the latest handoff.
2. `hcom list -v` + `hcom events --last 30` — roster + recent activity.
3. Verify the `dc-us` deploy rolled (RESUME.md carries the `kubectl get deploy` command
   and the expected SHAs).
4. Re-spawn coding/review agents as needed (a reboot kills them); confirm their worktrees —
   see **Spinning up the team** below.
5. Surface open decisions to `@bigboss` before dispatching work.

## Spinning up the team

When `@bigboss` says **"spin up the team"** (or similar), the orchestrator spawns the
coder + reviewer via hcom — `@bigboss` does not start them by hand. A reboot kills all
agents, so this is the standard post-restart move.

```bash
# Coder — Claude, in its persistent worktree (reused across reboots)
hcom 1 claude --tag coder --dir /Users/joe/repo/silkstrand-dosa \
  --hcom-prompt "You are the CODER on SilkStrand. Read AGENTS.md and CLAUDE.md first. Work ONLY in this worktree, branch from FRESH origin/main, never the main tree. Await tasks from the orchestrator over hcom; report PR# + branch when a PR is ready (gates green). You do not merge."

# Reviewer — Codex (auto-reads AGENTS.md), uses its own /tmp worktrees
hcom 1 codex --tag reviewer \
  --hcom-prompt "You are the REVIEWER on SilkStrand. Read AGENTS.md and CLAUDE.md. Review PRs the orchestrator routes to you in your own /tmp worktree; report findings and an approve/hold, run the web/Go gates, and do NOT merge."
```

Notes:
- hcom assigns fresh random 4-letter handles on each spawn — **report the assigned
  handles back to `@bigboss`** so they can refer to the agents by name.
- If a stopped agent's session still exists, prefer `hcom r <name>` (resume) over a
  fresh spawn to keep its context.
- Set a longer keep-alive for long rollouts so an agent doesn't time out mid-task:
  `hcom config -i self subagent_timeout <SEC>`.
