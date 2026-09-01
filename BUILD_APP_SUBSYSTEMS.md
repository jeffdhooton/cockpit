# Build: Hermes, SSH Hosts, and Rooms

A reference for the three subsystems in **Build** (the Electron agent manager at
`~/workspace/build`) that a Cockpit contributor might want to borrow from: how
Hermes connections work, how remote hosts over SSH work, and how Rooms work.

This is written for someone deciding **what Cockpit could plausibly swallow**.
Cockpit is an observatory, not a process runner — Build is the opposite, and
that difference decides which of these mechanisms port cleanly and which do not.
Section 6 grades every mechanism against that boundary. Sections 2–5 are the
reference material behind those grades.

**Provenance.** Everything here was read from the working tree at
`/Users/jeff/workspace/jbuild-sees-everything`, branch `goal/build-sees-everything`,
at commit `1d017de`. Line references are from that tree. Where a design document
disagrees with the code, the code wins and I say so.

---

## 1. The shape of the thing

Build is an Electron app. Its main process owns three kinds of execution
authority and one network peer:

```
                       ┌──────────────────────────────────────────┐
                       │  Build (Electron main process)           │
                       │                                          │
  local machine ◄──────┤  LocalPtyManager    → tmux → claude/codex│
                       │  RemotePtyManager   → ssh2 → tmux → agent│──► SSH host
                       │  HermesTerminalMgr  → JSON-RPC/WS        │──► hermes serve
                       │                                          │
                       │  GatewayConnector   → signed HTTP        │──► Hermes Gateway
                       └──────────────────────────────────────────┘         (Rooms)
```

Three facts explain most of the design:

1. **Build owns processes.** Every local PTY, every SSH PTY, every Hermes
   session Build launched. Nothing else may start, prompt, or kill them.
2. **The Gateway owns organization.** Rooms, membership, the phone-facing view.
   It has no terminals, no paths, no credentials, and cannot synthesize a
   command line.
3. **The two exchange only redacted, bounded, revision-checked documents.**

Those three sentences are the whole security model. Almost every odd-looking
decision below follows from refusing to weaken one of them.

---

## 2. SSH remote hosts

### 2.1 The host record

A host is a row in Build's SQLite registry with `kind` of `local` or `ssh`.
`toSshHostConfig` (`src/main/remote/host-config.ts`) narrows it to what the
transport needs:

```ts
interface SshHostConfig {
  id, label, hostname, user, port           // required
  identityFile?, agentSocket?               // auth
  trustedFingerprint?                       // app-level host key pin
  defaultShell?, remoteHome?                // environment
  claudePath?, codexPath?, hermesPath?      // per-host executables, absolute
}
```

The per-agent executable paths matter more than they look. They are absolute and
per-host, and `hermesPath` in particular is what makes a host *eligible* to be
the Hermes mothership (§3.4). Build never searches `$PATH` on a remote box for
something it is about to execute with authority.

### 2.2 Importing from `~/.ssh/config`

`src/main/remote/ssh-config.ts` reads aliases rather than reimplementing SSH
config parsing:

- `listSshAliases()` scans for `Host` lines, dropping any alias containing `*`,
  `?`, or characters outside `[A-Za-z0-9_.@-]`.
- `resolveSshAlias(alias)` shells out to `/usr/bin/ssh -G <alias>` with a 5s
  timeout and `LC_ALL=C`, then parses the normalized key/value output.

Letting `ssh -G` do the work means `Include`, `Match`, and per-host defaults are
all honoured for free. The parse extracts `hostname`, `user`, `port`,
`identityfile` (skipping the literal `none`), `identityagent`, `proxyjump`, and
`proxycommand`.

`ProxyJump` and `ProxyCommand` are *detected and rejected* rather than silently
ignored. Half-supporting a jump host means connecting somewhere the user did not
intend, so the resolver surfaces both fields and the host-add form refuses the
alias outright:

```ts
// src/renderer/src/components/SettingsPanel.vue:397
if (resolved.proxyJump || resolved.proxyCommand) {
  formError.value = 'This alias uses ProxyJump or ProxyCommand, which Build does not support yet.'
```

Note that the check lives in the renderer, not the transport. `SshManager`
declares an `UnsupportedSshConfigError` for this purpose but nothing currently
throws it, so a host row written by any path other than the alias-import form
would reach `ssh2` unguarded. If you port this, put the refusal in the transport
where it cannot be bypassed.

### 2.3 Host key trust

This is the part worth copying most carefully. `KnownHostsVerifier`
(`src/main/remote/known-hosts.ts`) parses `~/.ssh/known_hosts` and
`/etc/ssh/ssh_known_hosts`, including hashed (`|1|salt|hash`) entries, and
returns one of four verdicts:

| Verdict | Meaning |
|---|---|
| `trusted` | A matching entry has this exact key |
| `revoked` | An `@revoked` marker line matches this exact key |
| `changed` | Entries exist for this host, none has this key |
| `unknown` | No entries for this host at all |

The verifier runs inside ssh2's `hostVerifier` callback
(`ssh-manager.ts:608-623`), and the ordering is deliberate:

```ts
if (trust.status === 'trusted') return true
// A system/user known_hosts revocation always wins over an app-level
// pin. The explicit fingerprint must never resurrect a revoked key.
if (trust.status === 'revoked') { observedTrust = …; return false }
if (explicitHash && safeHashEqual(explicitHash, keyHash)) return true
observedTrust = { status: trust.status, fingerprint: trust.fingerprint }
return false
```

Build's own `trustedFingerprint` pin is checked **after** revocation and can
never override it. Comparison is `timingSafeEqual` over raw hex. `changed` and
`revoked` surface as `ChangedHostKeyError`; `unknown` as `UnknownHostKeyError`.
Both carry the observed fingerprint so the UI can show the user exactly what it
saw and ask once — there is no "continue anyway" that skips the record.

Authentication is private key from `identityFile` (read with `expandHome`, ENOENT
becomes a clear "identity file was not found") plus agent auth from the host's
`agentSocket` or `SSH_AUTH_SOCK`. Both may be present; ssh2 tries in order.

Connection tuning: `keepaliveInterval: 10_000`, `keepaliveCountMax: 3`,
`readyTimeout: 15_000`.

### 2.4 One connection, many multiplexed uses

`SshConnection` (`ssh-manager.ts:117`) wraps one ssh2 `Client` and exposes:

| Method | What it gives you |
|---|---|
| `execute(cmd, timeoutMs = 20_000)` | One-shot command, bounded stdout/stderr |
| `openPty(cmd, cols, rows)` | Interactive channel with `resize()` |
| `sftp()` | An `SFTPWrapper` |
| `createPrivateFile(contents)` | Mode-0700 remote file, self-removing |
| `createReverseTunnel(localPort, remotePort = 0)` | Remote loopback → Mac loopback |
| `createLocalTunnel(remotePort)` | Mac loopback → remote loopback |
| `createLoopbackHttpProxy()` | Authenticated local HTTP proxy over the channel |

`SshManager` (`:736`) keys one `SshConnection` per host id and hands out the
shared instance. Everything above rides that single TCP connection.

**Generations.** `SshConnection` carries a `#generation` counter, checked after
every `await` in the connect path (`#isCurrent(generation, client)`). A
reconnect that races a disconnect cannot install its client over a newer one.
Any late resolution throws `connectionAttemptCancelled()` instead of mutating
state. If you build reconnect logic in Go, this is the bug class to plan for
first: async connect + user-initiated disconnect is where stale clients get
adopted.

On close, bindings are cleared, every loopback proxy is torn down, `#client` is
nulled, and `disconnected` is emitted **only if the connection had reached
ready** — so a failed dial does not look like a dropped session.

### 2.5 Remote sessions are tmux sessions

`RemotePtyManager` (`src/main/remote/remote-pty-manager.ts`) is the piece
Cockpit will find most familiar, because the persistence mechanism is the one
Cockpit already lives in.

```ts
export type RemotePtyPersistence = 'tmux' | 'nonpersistent'
export type RemotePtyState =
  | 'connected' | 'reconnecting' | 'disconnected'
  | 'nonpersistent_lost' | 'exited'
```

A remote agent runs inside a remote tmux session with
`REMOTE_TMUX_HISTORY_LIMIT = 50_000`, matched deliberately to the local manager's
`TMUX_HISTORY_LIMIT`. When the SSH connection drops, the agent keeps running;
Build reconnects (base 250ms, max 10s, 8 attempts) and re-attaches.

When remote tmux is unavailable, the launch still happens but returns
`degradedReason: 'tmux-unavailable'` and persistence `nonpersistent`. Losing that
connection produces the distinct state `nonpersistent_lost` rather than pretending
a reconnect might help. Degradation is always named, never silent.

Revival is gated on evidence. `RemoteTmuxSessionMissingError` is documented as:

> A verified negative `tmux has-session` result. This is intentionally distinct
> from transport, PATH, executable, or permission failures, but it does not by
> itself prove that former pane descendants exited. Callers must keep revival
> locked until process-ownership checks prove absence or the user explicitly
> abandons the unresolved run.

So "tmux says no session" is *not* accepted as "the agent is gone." Build
cross-checks process ownership via `REMOTE_PROCESS_ID`, `REMOTE_PANE_TTY`, and
`REMOTE_PROCESS_TTY` patterns before it will relaunch. Double-starting an agent
is worse than showing an unresolved row.

Environment sent to a remote agent is filtered by `SENSITIVE_ENVIRONMENT_KEY`:

```
/(?:^|_)(?:API_KEY|TOKEN|SECRET|PASSWORD|PASSCODE|CREDENTIALS?)(?:$|_)/iu
```

### 2.6 Status hooks over a reverse tunnel

Build does not scrape terminal output to know what an agent is doing. It runs a
loopback HTTP status server (`src/main/services/status-server.ts`) and passes
each run a URL and a token through `BUILD_STATUS_URL` / `BUILD_STATUS_TOKEN`.
The agent's hooks POST structured events back.

For a remote agent, `createReverseTunnel(localPort, 0)` asks the remote sshd for
an ephemeral loopback port, and `BUILD_STATUS_URL` points at
`127.0.0.1:<remotePort>` on the remote box. Traffic goes remote-loopback → SSH
channel → Mac-loopback. Nothing listens on a routable interface at either end.

Two details worth stealing:

- The remote port is **immutable for the life of the agent process**, because it
  is baked into the running agent's hook configuration.
  `RemoteStatusTunnelRestoreOptions` carries it explicitly so a reconnect
  rebuilds the *same* mapping rather than allocating a new one the agent would
  never call.
- `hook-relay.ts` sanitizes every payload against a fixed `FIELD_SPECS`
  allowlist with per-field length caps, mapping alias spellings
  (`session_id` / `sessionId` / `thread_id`) onto one output name. Unknown
  fields are dropped, not passed through. Claude's `Stop` event may carry the
  entire last assistant message; Build bounds intake so a large private reply
  cannot crowd out the metadata it actually wants.

Receipts for native session ids are HMAC'd with a key at
`~/.build/native-session-receipts/native-session-receipt-key-v1`, capped at 2KB,
mode 0600.

### 2.7 Attaching to a tmux session you did not create

This one names Cockpit directly in its own docstring
(`src/main/services/adopted-attach.ts`), so it is worth reproducing the reasoning.

Build discovers tmux sessions it did not create on every non-Build socket in
`/tmp/tmux-<uid>` and offers to display them. The naive implementation,
`tmux attach-session`, is wrong: tmux sizes a shared window to a client, and a
second client visibly resizes the window the human is working in.

A **grouped session** — `tmux new-session -t <target> -s build-view-<16 hex>` —
gets most of the way. Build gets its own size and its own current window while
sharing the target's windows, and removing the view destroys nothing.

It is not sufficient by itself, and measurement rather than reasoning settled
that: a Cockpit client at 200x49 dropped to 80x23 the instant Build's 80x24 view
attached, because tmux gives a shared window's size to the newest client.

The fix is the client flag **`attach-session -f ignore-size`**, which excludes
Build's client from window sizing entirely. The obvious alternative,
`set-option -w window-size largest`, is a trap:

- it writes a window option belonging to the other person;
- it covers only the one window the view happens to display, so navigating
  re-breaks it;
- it is never restored if the view moves windows, leaking `largest` permanently;
- it *grows* the human's window past their terminal whenever Build's tile is the
  larger client.

(Also: `set-option -w` needs a window target `=name:`, not a session target
`=name`, or it fails silently with "no such window.")

The cost accepted is that Build writes one session into someone else's default
tmux server. That write is bounded by construction: the view name is
`build-view-` + 16 hex of `sha256(server + session + terminalId)`, and the only
session Build will ever kill is one matching `/^build-view-[0-9a-f]{16}$/`.
Including the terminal id is not cosmetic — keying only on the session meant a
view outliving its target got adopted by the *next* session of the same name,
displaying a dead incarnation's panes under a live title.

---

## 3. Hermes

### 3.1 What Hermes is, in Build's terms

Hermes is a separate agent product with a concept Build has to respect rather
than flatten: **profiles**. A profile is an isolated Hermes home with its own
configuration, memory, skills, sessions, projects, and schedules. A Hermes
session is owned by exactly one profile, and its durable identity is the triple
**(host, profile, native session id)**.

That triple is the whole reason for the plumbing below. Two profiles can hold
the same native session id, and treating the id as unique corrupts both.

Profile names must match `^[a-z0-9][a-z0-9_-]{0,63}$` (`isValidHermesProfile`,
`src/shared/domain.ts:21`).

### 3.2 The transport: a private loopback gateway

Build does not drive a Hermes terminal. It spawns Hermes' own structured
gateway and speaks JSON-RPC to it (`src/main/hermes/hermes-backend-service.ts`):

```
hermes serve --host 127.0.0.1 --port 0 --skip-build
```

- `--port 0` lets the OS pick; Hermes announces it on stdout as a line matching
  `^HERMES_BACKEND_READY port=(\d{1,5})$`. Build waits up to 90s for it, bounded
  at 64KB of output.
- A random session token (`^[A-Za-z0-9_-]{32,256}$`) is passed as
  `HERMES_DASHBOARD_SESSION_TOKEN` and required on every REST and WebSocket call.
- Startup is confirmed with `GET /api/status` before the connection is handed out.
- Every stdout/stderr byte after the ready line is **discarded** (`DISCARD_OUTPUT`).
  Build never surfaces Hermes' process output to the UI.

Bounds: 8MB max WebSocket frame, 1MB max REST response, 30s default request
timeout, 1800s prompt timeout, 120s transcription timeout.

RPC surface: `session.create`, `session.resume`, `session.history`,
`session.interrupt`, `session.close`, plus `prompt.submit`. The WebSocket
carries `message.start`, `message.delta`, `message.complete`, `status.update`,
`error`, `session.info`, and four interactive requests: `approval.request`,
`clarify.request`, `sudo.request`, `secret.request`.

Approvals are answered with an explicit `HermesApprovalChoice` of
`'once' | 'session' | 'always' | 'deny'`.

Because status arrives as `status.update` events rather than being inferred from
bytes, a Hermes session's state in Build is authoritative in a way a scraped
terminal's never is.

### 3.3 The same gateway, on another machine

`createRemoteHermesBackend` (`src/main/hermes/remote-hermes-backend.ts`) reuses
`HermesBackendService` wholesale and swaps two injected functions: `spawn` and
`mapReadyPort`. That is the entire remote adaptation.

```
Mac loopback ──► SSH local tunnel ──► remote loopback:<hermes port>
```

- `mapReadyPort` calls `createLocalTunnel(remotePort)`, so the Mac talks to
  `http://127.0.0.1:<localPort>` and never knows the difference.
- `spawn` is the interesting half. The session token must not appear in SSH
  argv (it would be visible in the remote process table), so Build writes a
  **self-deleting mode-0700 launcher** over SFTP and executes only its path.

The launcher is generated by `buildRemoteHermesLauncher` and is worth reading in
full because of what it checks about itself before doing anything:

```sh
#!/bin/sh
set -eu
umask 077
self="$0"
trap 'rm -f -- "$self"' EXIT HUP INT TERM
launcher_mode="$(stat -c '%a' -- "$self" || stat -f '%Lp' "$self" || true)"
launcher_owner="$(stat -c '%u' -- "$self" || stat -f '%u' "$self" || true)"
current_owner="$(id -u)"
if [ "$launcher_mode" != "700" ] || [ "$launcher_owner" != "$current_owner" ]; then
  printf 'Build refused an unsafe remote Hermes launcher.\n' >&2
  exit 126
fi
cd -- '<cwd>'
export 'KEY=value'…
export HERMES_DASHBOARD_SESSION_TOKEN='<token>'
rm -f -- "$self"
trap - EXIT HUP INT TERM
exec '<executable>' serve --host 127.0.0.1 --port 0 --skip-build
```

It verifies its own mode and owner, refuses with exit 126 if either is wrong,
unlinks itself before `exec`, and clears the trap so the successful path does
not double-remove. Both `stat` spellings are tried so it works on GNU and BSD.

The environment it exports is the **remote login environment**, probed over SSH —
never the Mac's process environment. `validatedRemoteEnvironment` caps it at 512
entries and 96KB, requires `^[A-Za-z_][A-Za-z0-9_]*$` keys, rejects NUL, and
drops `PWD`, `OLDPWD`, `SHLVL`, `_`, anything starting with `BUILD_STATUS_`, and
the token variable itself.

### 3.4 The mothership

One SSH host is designated the **Hermes mothership**. Every profile launch,
resume, fork, and voice transcription routes there.

`resolveHermesMothershipHostId` (`src/shared/hermes-mothership.ts`) is 20 lines
and encodes three rules:

```ts
// A mothership is an execution authority, not a transport type. When Build
// runs on the Hermes computer, its local host is the authority and must not
// SSH back into itself.
const eligible = hosts.filter((host) => Boolean(host.hermesPath))
```

1. Eligibility is `hermesPath` being set — **not** `kind === 'ssh'`. The local
   host qualifies when Hermes lives on the same machine, and Build then uses it
   directly instead of looping back through SSH.
2. The explicit setting `hermes.mothershipHostId` wins, but only if it still
   names an eligible host; a stale id resolves to `null` rather than silently
   falling through to some other host.
3. A legacy `voice.hermesHostId` is read as a migration fallback only. With
   exactly one eligible host and no setting, that host is chosen.

### 3.5 Profile preflight, and why it exists

Immediately before any Hermes effect, Build runs a bounded preflight over the
existing SSH connection (`buildRemoteHermesProfilePreflight`):

```sh
set -eu
cd -- '<cwd>'
export 'KEY=value'…
exec '<hermes>' -p '<profile>' --version
```

Unknown profile, missing executable, wrong host, timeout, or non-zero exit all
fail closed. The reason is specific and documented: **current Hermes Gateway
code otherwise falls back to its launch profile when a requested profile home is
missing.** Without the preflight, asking for a profile that does not exist
silently runs work in the wrong identity, with the wrong memory and skills. A
`--version` call is the cheapest question that proves the profile root resolves.

For the same reason, a Build "fork" resumes the exact profile-scoped source and
creates the child with the same explicit profile, rather than relying on Hermes'
profile-implicit `session.branch`.

### 3.6 The inventory boundary

When the Gateway needs to enumerate profiles and sessions, it reads the Hermes
home **only when co-located with it** (or through a trusted read-only mount). A
separately hosted Gateway consumes a bounded metadata-only
`HERMES_INVENTORY_FILE` instead. There is deliberately no ad-hoc SSH inventory
channel. The adapter:

- accepts only the profile-name grammar;
- opens profile SQLite databases read-only / query-only;
- returns bounded profile and session metadata only;
- never reads `.env`, `SOUL.md`, memories, message bodies, transcripts, or
  credentials;
- identifies a session by profile **plus** session id, so identical native ids in
  two profiles cannot collide.

---

## 4. Rooms

### 4.1 What a Room is

A **Room** is a durable collaboration container owned by Hermes Gateway. It can
contain several Hermes profiles, their profile-scoped sessions, Build-managed
Claude/Codex/Hermes sessions, and any number of assigned Build projects.

- A Build project may be assigned to **more than one** Room.
- A Build session belongs to **at most one** Room.
- A Room may bind to one native Hermes Kanban board.

Rooms do not rename or replace Hermes' native concepts. Assigning a Build
project to a Room is an orchestration link, not a conversion of a native record.

### 4.2 The ownership split

| Gateway is canonical for | Build is canonical for |
|---|---|
| Rooms, membership, assigned project ids | Project availability, launch presets |
| Linked session ids, Room activity | Conversations, runs, process state |
| The phone-facing view | Structured completions, permissions, models |
| Room revisions | Hosts, and every native provider identifier |

And the hard boundary: **Gateway never receives raw terminals, filesystem paths,
native session ids, SSH details, credentials, environment values, or arbitrary
launch arguments.**

### 4.3 Device identity and signing

Pairing is a one-use code with a 10-minute lifetime, exchanged at
`POST /api/build/v2/pair` — the only unsigned call.

Build generates an Ed25519 key pair (`gateway-crypto.ts`), validates the SPKI
prefix on export, and keeps the raw 32-byte public key base64url'd. The private
key is PKCS#8 DER, stored through Electron `safeStorage` when available:

```ts
const storedCredentialSchema = z.object({
  version: z.literal(1),
  origin: z.string().url().max(2_048),
  deviceId: z.string().uuid(),
  publicKeyRaw: z.string().regex(/^[A-Za-z0-9_-]{43}$/u),
  privateKeyPkcs8Der: z.string().base64().max(2_048),
  pairedAt: z.string().datetime({ offset: true })
}).strict()
```

Every other call is signed over a canonical string:

```
METHOD \n /path?query \n timestamp \n nonce \n sha256hex(body)
```

carried in four headers: `X-Build-Device`, `X-Build-Timestamp`, `X-Build-Nonce`,
`X-Build-Signature`. The Gateway side runs nonce replay protection. Because the
body hash is inside the signed string, a body-rewriting proxy invalidates the
request rather than passing through.

### 4.4 The endpoints

| Endpoint | Direction | Purpose |
|---|---|---|
| `POST /v2/pair` | → | Exchange one-use code for a device id (unsigned) |
| `PUT /v2/catalog` | → | Publish the redacted inventory |
| `GET /v2/state` | ← | Rooms, chats, profiles, sessions, teams |
| `PUT /v2/state/ack` | → | Acknowledge the Room revisions actually adopted |
| `POST /v2/poll` | ← | Lease pending commands |
| `POST /v2/commands/{id}/complete` | → | Report a command result |
| `PUT /v2/rooms/{room}/auto/{exchangeId}` | → | Super Auto progress |
| `PUT /v2/transcripts/{sessionId}` | → | Bounded transcript page |
| `GET`/`PUT /v2/rooms/{room}/team` | ↔ | Read / replace the team graph |
| `POST /v2/rooms/{room}/team/rounds` | → | Start a round |
| `PUT /v2/rooms/{room}/team/dispatches/{id}` | → | Report a dispatch outcome |
| `…/team/goals`, `…/goals/{id}/sign-off`, `…/approvals/{id}` | ↔ | Goals and approvals |
| `POST /v2/revoke`, `POST /v2/login-link` | → | Unpair; mint a web login link |

Protocol 1 remains frozen alongside protocol 2. The connector **alternates**
poll protocols (`#nextPollProtocol` flips 1↔2 each pass) so an upgraded device
keeps serving legacy phone actions while Room actions use v2. Only when *both*
protocols return empty does it sleep 4s — otherwise it polls straight through.

### 4.5 The connector loop

`GatewayConnector.#runLoop` (`gateway-connector.ts:601`):

1. Refresh state if the catalog is dirty or 30s have elapsed.
2. If a catalog adoption is pending, back off 100ms and restart the pass.
3. Sync the catalog. If it applied, restart immediately — a catalog mutation can
   advance Room revisions, and a v2 command must never be leased against a
   revision Build has not yet adopted.
4. Flush pending command completions and Super Auto progress.
5. Poll, then execute each command in order.

Commands are leased for a fixed **30 seconds** (`GATEWAY_COMMAND_LEASE_MS`).
When a command arrives against a Room revision Build has not caught up to, Build
**defers**: it leaves the command leased and uncompleted, and the Gateway
requeues it after the lease expires. The comment is exact about why:

> Leave the command leased but uncompleted. Gateway requeues it after the fixed
> 30-second lease, avoiding both a stale effect and a permanent failure while
> state propagation catches up.

Clock skew is handled by anchoring: `generated_at` from the poll response is
combined with a local **monotonic** delta, so expiry checks do not depend on the
two machines agreeing about wall-clock time.

### 4.6 Commands

Every v2 command carries `id`, a 43-char `lease_token`, `expires_at`, and
`expected_room_revision`. Prompt and interrupt additionally carry
`expected_run_id`.

| Command | Payload beyond room + revision |
|---|---|
| `room.session.launch` | `project_id`, `launch_preset_id` |
| `room.session.prompt` | `session_id`, `expected_run_id`, `prompt` (≤12,000 chars) |
| `room.session.interrupt` | `session_id`, `expected_run_id` |
| `room.auto.start` | two session/run pairs, `source_message_id` (64 hex), `mode`, `max_auto_replies` (1–12) |
| `room.auto.stop` | `exchange_id` |
| `session.transcript` | `session_id`, `cursor`, `limit` (≤200) |

`launch_preset_id` is the load-bearing design choice. The Gateway can only ask
for a preset **Build previously published**. It cannot inject a cwd, model,
effort, provider, environment, permission mode, Hermes profile, or command line.
The phone picks from a menu; it never composes.

`room.auto.start` refuses participants that are the same session or the same run.

### 4.7 Redaction and caps

The catalog is capped at 2MB. Room arrays are capped at 2,000 projects, 2,000
profiles, 10,000 sessions. Adopted terminals cap at 500, and the reason is
recorded in the code:

> Jeff runs six; anything approaching this is a machine misbehaving, and the
> catalog says so through `omitted.terminals` rather than silently truncating.

That pattern — an explicit omission count instead of a silent truncation —
repeats across the catalog. Terminals carry a **folder name only**, produced by
`folderName()` taking the last path segment.

An assistant completion is exposed as `{message_id, occurred_at}` where the id is
an opaque SHA-256 token Build resolves back internally. Completion text never
leaves Build, yet a Room Auto command can still name an exact completion.

Display strings run through `gatewayDisplayText`, which counts **Unicode code
points** (not UTF-16 units), rejects lone surrogates, and rejects unsafe control
and bidirectional characters. Ids additionally reject whitespace. Every schema is
`.strict()`, so an unexpected key is a parse failure, not a shrug.

### 4.8 What `synced` means

A Room is `synced` only after the paired Build has strictly parsed and
atomically applied a signed state containing that exact revision, then returned
a signed `PUT /v2/state/ack`. Constructing or sending a state response cannot
acknowledge it. Completing a command cannot. Acknowledging a stale snapshot
cannot.

A Room is *globally* synced only when every non-revoked device backing one of its
assigned projects or linked sessions has acknowledged the current revision — an
unrelated same-user device cannot satisfy the condition.

`sync_status` is one of `pending | synced | offline | error`, and the last three
are all explicit states rather than absence of the first.

### 4.9 The claim protocol

The hardest problem Rooms solve: a phone asks Build to launch a session; Build
launches it; the network dies before the catalog reaches the Gateway. Who owns
that session?

The resolution:

1. Build records the Room and revision `R` **locally, before** completing the
   command lease.
2. A separate schema bit marks claim-readiness only once the process actually
   starts (or a restart successfully adopts its transport).
3. While and only while the binding is provisional *and* start-ready, the signed
   catalog session includes `room_claim_revision: R`. Canonical Room sessions
   omit the field entirely.
4. The Gateway accepts a claim only for a genuinely first-seen session with a
   current run, an active Room, a project still assigned to that Room, and a
   revision **this same device previously acknowledged**.
5. On acceptance the Gateway atomically attaches the session and advances the
   Room to `R+1`.

Existing and tombstoned mirrors cannot use a catalog claim to attach, move, or
revive themselves — those need an explicit Gateway launch command. A
same-revision snapshot that omits the session preserves the provisional binding;
a *later* revision that still omits it clears it.

This survives restart before the first catalog PUT, an ambiguous catalog PUT, and
network loss, without either dropping a valid launch or preserving a stale one.

### 4.10 Super Auto in a Room

Super Auto is a bounded two-party relay between two already-running sessions:
mode `contrarian` or `reply`, 1–12 automatic replies, and only a new structured
completion carrying the expected source message id can advance it.

It pauses or stops visibly on manual input, an approval/clarification/secret/sudo
request, `needs_input`, disconnect, timeout, restart, or run replacement. It
cannot change model, permission mode, project, host, provider, profile, cwd, or
native identity. A model cannot approve permissions for another model.

The exchange UUID is deterministically the Gateway's start-command UUID, so the
Gateway pre-registers the id before delivery while Build creates the exchange and
its progress-outbox row atomically. A *queued* stop does not claim the relay is
stopped — the relay stays active with a visible pending delivery until Build
acknowledges. Failed or uncertain stop delivery stays retryable rather than
hiding a live loop.

### 4.11 Room Teams

The orchestration layer on top of Rooms (design:
`docs/superpowers/specs/2026-07-25-room-teams-orchestration-design.md`). One team
per Room. The Gateway owns the graph and executes rounds, because — the stated
reason — the Mac sleeps and the mothership does not.

Nodes are `build_session` or `hermes_session` with a role of
`lead | worker | reviewer`. Edges are typed:

| Edge | Behaviour |
|---|---|
| `delegate` A→B | B receives A's output as an assignment; B's answer continues traversal |
| `report` B→A | Fires when B answers; A receives it as a status report |
| `review` A→B | B critiques and is told explicitly **not** to do the work |
| `broadcast` A→* | Fans out; answers are recorded but **do not cascade** |

`broadcast` not cascading is the storm guard — the only edge type whose answers
are terminal.

Caps: `max_hops` 1–12 (default 4), `max_dispatches_per_round` 1–64 (default 12),
`max_concurrent` 1–8 (default 3), `round_timeout_s` 60–86,400 (default 1800).

Invariants that hold regardless of what any agent writes:

1. **One in-flight dispatch per session.** Traversal that would violate this
   queues.
2. `max_hops` bounds depth. Cycles are legal; hops are what terminate them.
3. Hitting `max_dispatches_per_round` ends the round `completed` with a
   `cap_reached` stat — not an error.
4. `round_timeout_s` ends the round `expired`.

Dispatch lifecycle is `queued → sent → answered | failed | expired`. The key
distinction: for a Build-executed dispatch, the command receipt **proves
delivery, not an answer**. The answer is the session's next completed assistant
turn after the prompt lands — unambiguous precisely because of invariant 1 — and
correlation is by `dispatch_id`, never by matching text.

Retry is at most once. A second failure marks the dispatch `failed`, the round
`degraded`, and writes an event naming the hop and reason. The engine never
silently retries.

An offline Build means dispatches queue and then expire with
`error: "Build offline"`. An exited run fails immediately with
`"Session is not running"` — auto-relaunch is deliberately excluded, because
launching a session is a "new thing" and new things are gated behind human
approval.

Build's canvas keeps all pure logic in `src/renderer/src/team-graph.ts` — node
and edge validation, self-edge and duplicate rejection, traversal preview, cap
math, payload shape — with the Vue component as a dumb renderer over tested
functions. Writes are optimistic-local → signed whole-document PUT with
`expected_revision` → on `409`, refetch and re-render with a notice.

---

## 5. The design rules underneath all three

Worth extracting, because they are what make the subsystems feel consistent:

1. **Fail closed, and name the failure.** Unknown profile, unknown host key,
   missing tmux, offline peer — each produces a distinct, reportable state.
   There is no path where uncertainty resolves to "probably fine."
2. **Degradation is visible.** `degradedReason: 'tmux-unavailable'`,
   `nonpersistent_lost`, `omitted.terminals`, round `degraded`. Nothing silently
   does less than it claims.
3. **Receipts are not answers.** Delivering a prompt and getting a reply are
   separate facts with separate lifecycles.
4. **Every mutation is revision-checked.** Rooms, teams, and runs all carry an
   expected value, and a mismatch defers or 409s rather than applying.
5. **The remote end never gets a free-form command.** Presets, allowlists,
   validated absolute paths, quoted arguments. `posixQuote` is applied to every
   interpolated value in every generated script.
6. **Bound everything, and say what was dropped.** Byte caps, entry caps,
   code-point caps, explicit omission counts.
7. **Prove absence before acting on it.** The tmux revival lock is the clearest
   case: a negative `has-session` is evidence, not proof.

---

## 6. What Cockpit could plausibly take

Graded for a Go TUI that observes rather than owns processes. Cockpit's stated
role — *"an observatory (not a process runner)"* — is the filter.

### Strong fit

**Host key trust as a first-class verdict.** If Cockpit ever reads a remote
host's git or tmux state, `parseKnownHosts` + the four-verdict model is ~150
lines in Go and prevents the worst class of mistake. The rule that a `@revoked`
marker beats an app-level pin is one line and easy to get backwards.

**The grouped-session + `ignore-size` attach.** This is Cockpit's own problem in
reverse, and Build already paid for the measurement. If Cockpit ever displays a
session someone else is attached to, `new-session -t` alone will resize their
window; `attach-session -f ignore-size` is the fix, and `window-size largest` is
the trap. The naming discipline is equally portable: derive the view name from a
hash, and only ever kill names matching that derivation.

**Hook-driven status instead of output scraping.** A tiny loopback HTTP server
plus `BUILD_STATUS_URL`/`BUILD_STATUS_TOKEN` in the agent environment gives real
lifecycle events. In Go this is `net/http` on `127.0.0.1:0` plus a per-run token
— maybe 200 lines — and it upgrades "● working" from a guess to a fact. The
field-allowlist sanitizer is the part to copy verbatim.

**Explicit omission counts.** Cockpit's grid already truncates. Reporting
`omitted: N` rather than silently cutting is nearly free and turns a confusing
display into an honest one.

### Moderate fit

**Reverse tunnels for remote status.** `golang.org/x/crypto/ssh` supports remote
port forwarding directly. Real work, but it is the only way to get hook events
off a remote box without opening a routable port. The immutable-port-across-
reconnect detail is the one that will bite.

**Remote tmux state over one multiplexed SSH connection.** One `ssh.Client` per
host, many sessions on it, generation-guarded reconnect. Cockpit already models
tmux and git per project; the remote version is mostly the same queries down a
different pipe. Budget real time for the generation guard.

**The revision-checked document exchange.** If Cockpit grows any multi-machine
sync, the pattern — whole-document PUT with `expected_revision`, 409 on
mismatch, explicit ack of what was actually adopted — is worth more than it
costs. It is what makes `synced` mean something.

### Poor fit

**Rooms and Room Teams wholesale.** These need a server. The Gateway owns the
graph specifically because the laptop sleeps. Cockpit has no such peer, and
building one is a product, not a feature.

**The Hermes backend transport.** Tightly bound to Hermes' own gateway, its
ready-line protocol, its session-token scheme, and Build's process ownership.
Cockpit could *display* Hermes sessions read-only from the inventory boundary
(§3.6), but spawning `hermes serve` makes Cockpit a process runner.

**Ed25519 device pairing.** Correct, but it exists to authenticate a phone to a
desktop across the internet. Cockpit is local-first; this is a solution to a
problem it does not have yet.

### The one thing to steal regardless of scope

The **preflight-before-effect** habit (§3.5). One cheap bounded question — a
`--version`, a `has-session`, a `stat` — asked immediately before an irreversible
action, failing closed on anything unexpected. It is the cheapest single idea in
this document and the one that prevents the most silent wrongness.

---

## 7. Source map

Read the code over this document wherever they disagree.

| Concern | File |
|---|---|
| SSH connection, tunnels, proxies | `src/main/remote/ssh-manager.ts` |
| Host key trust | `src/main/remote/known-hosts.ts` |
| `~/.ssh/config` import | `src/main/remote/ssh-config.ts` |
| Host record → transport config | `src/main/remote/host-config.ts` |
| Remote tmux PTYs, reconnect, revival | `src/main/remote/remote-pty-manager.ts` |
| Hook sanitizing, receipts, relay upload | `src/main/remote/hook-relay.ts` |
| Local loopback status server | `src/main/services/status-server.ts` |
| Local tmux PTYs | `src/main/services/local-pty-manager.ts` |
| Adopting someone else's tmux session | `src/main/services/adopted-attach.ts`, `adopted-session.ts` |
| Hermes gateway transport | `src/main/hermes/hermes-backend-service.ts` |
| Hermes sessions, events, approvals | `src/main/hermes/hermes-terminal-manager.ts` |
| Hermes over SSH, launcher, preflight | `src/main/hermes/remote-hermes-backend.ts` |
| Mothership resolution | `src/shared/hermes-mothership.ts` |
| Every Gateway wire schema | `src/shared/gateway.ts` |
| Signing and key generation | `src/main/services/gateway-crypto.ts` |
| Credential storage | `src/main/services/gateway-credential-store.ts` |
| HTTP calls and endpoints | `src/main/services/gateway-client.ts` |
| Poll loop, leases, revisions | `src/main/services/gateway-connector.ts` |
| Command execution | `src/main/services/gateway-command-service.ts` |
| Catalog construction and redaction | `src/main/services/gateway-catalog-service.ts` |
| Team graph pure logic | `src/renderer/src/team-graph.ts` |

Design documents in the Build repo:

- `docs/superpowers/plans/2026-07-16-hermes-rooms-collaboration.md` — the Rooms
  architecture, ownership split, and acceptance gates
- `docs/superpowers/specs/2026-07-25-room-teams-orchestration-design.md` — Room
  Teams, edge semantics, dispatch lifecycle
- `docs/setup/HERMES-MOTHERSHIP-SETUP.md` — operator setup for Hermes + Gateway
- `docs/contracts/session-control-v1.md` — the session control contract

**One caution on the design documents.** They were written between July and
August 2026 and describe a `~/Apps/Build` checkout with a different operator.
Paths, hostnames, and the person named in them are stale. The architecture they
describe is current; the deployment details are not.
