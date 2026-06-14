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

- **kubectl LAN routing drops** when colima/Docker hijacks the route to the homelab
  (`no route to host 192.168.0.210:6443`). A reboot clears it. Do kubectl work
  **before** starting Docker; don't restart colima in `--network-mode bridged`.
- **Argo CD git cache can go stale** and report `Synced/Healthy` against an old
  revision. If a green build didn't roll out, **hard-refresh** the Argo app rather
  than assuming the deploy failed. (Also noted in CLAUDE.md.)

## On restart (orchestrator checklist)

1. Read **RESUME.md** — the latest handoff.
2. `hcom list -v` + `hcom events --last 30` — roster + recent activity.
3. Verify the `dc-us` deploy rolled (RESUME.md carries the `kubectl get deploy` command
   and the expected SHAs).
4. Re-spawn coding/review agents as needed (a reboot kills them); confirm their worktrees.
5. Surface open decisions to `@bigboss` before dispatching work.
