# Hermes Tile — Design

Date: 2026-09-01
Status: **approved** — implement

One grid tile that answers the question you check by hand today: is Hermes
up, and is it talking to Slack. Companion to `2026-09-01-remote-hosts-design.md`;
shares nothing with it but the grid.

## 1. What it reads

Hermes on `mini` runs as two launchd services, `ai.hermes.gateway` and
`ai.hermes.dashboard`. The dashboard listens on `0.0.0.0:9119` and answers
`GET /api/status` **without a token** (verified 2026-09-01 over the tailnet):

```json
{"version":"0.16.0","gateway_running":true,"gateway_pid":676,
 "gateway_state":"running",
 "gateway_platforms":{"photon":{"state":"connected",...},
                      "slack":{"state":"connected",...}},
 "gateway_exit_reason":null, ...}
```

Anything about sessions needs the dashboard session token, which lives in
`~/.hermes/.env` on `mini`. **This design reads none of it.** No credential
enters cockpit's config; nothing is written to Hermes.

Note for the operator: the dashboard binds `0.0.0.0`, so it answers on
whatever LAN the mini sits on, not only the tailnet.

## 2. Config

```toml
[[hermes]]
label = "hermes"
url = "http://100.96.45.73:9119"
refresh_interval = 30     # seconds; default 30
```

`url` must parse and use `http` or `https`. `label` follows `validLabel`.

## 3. Source

```go
// sources/hermes.go
type HermesStatus struct {
	Label     string
	Reachable bool
	Gateway   string   // "running", "stopped", or the raw gateway_state
	Platforms []string // names whose state is "connected", sorted
	Version   string
	Err       error    // set when unreachable or unparseable
}
func GetHermesStatus(ctx context.Context, client *http.Client, cfg config.HermesConfig) HermesStatus
```

- 5-second timeout. Body capped at 64KB. Only the fields above are decoded;
  everything else in the response is ignored.
- Unreachable, non-200, or unparseable → `Reachable: false` with `Err` set.
  A stopped gateway is reachable with `Gateway: "stopped"`; the two are
  different facts and render differently.

## 4. Tile

Renders in the running group, keyed by label, as a target with no session and
no repo:

```
╭────────────╮
│ hermes     │
│ ● gateway  │    running → accent; stopped → warning
│ slack photon│   connected platforms, truncated to the tile
╰────────────╯
```

Unreachable shows `⚠ unreachable` in the warning colour and keeps the last
platform line. Enter does nothing; the tile is read-only and says so in the
key hints when selected.

`Target` gains an optional `Hermes *HermesStatus`. `BuildTargets` appends
Hermes targets after running sessions and before dormant repos, so they sit
with the live things.

## 5. Signals

`gateway_running == false` while reachable produces a `SignalHermesDown`
signal, placed after blocked agents and before dead processes. Unreachable
does **not** signal: the tailnet being down is not a Hermes problem, and the
tile already says unreachable.

## 6. Testing

- Parsing the real status document (fixture from the probe): platforms
  sorted, gateway state read, version read.
- A stopped gateway: reachable, `Gateway: "stopped"`, signal emitted.
- Connection refused and timeout: `Reachable: false`, no signal.
- A 401 or HTML body: `Reachable: false`, `Err` names the status.
- Tile rendering for each of the three states.
- Config validation: bad URL, non-http scheme.

## 7. What this does not do

- No sessions, no token, no writes, no Enter action.
- No polling of the gateway's own health URL; the dashboard's summary is the
  single source.
