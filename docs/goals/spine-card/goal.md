# Goal: spine card in Cockpit — see and reach the spine fleet from Cockpit

## Request

Okay let's do another that makes it super easy to see what spine is working on across every project. We can utilize spine to build it. I have ~/workspace/cockpit which is essentialy my version of an ai agent-centric tmux-backed multiplexer. I want that same level of visibility and ease of use into my spine fleet

I like the idea of having a "spine" card inside of cockpit and maybe entering that opens up spine's own TUI. We should go with a different vibe/look for whatever TUI we end up building for spine.

we'd want it inside of tmux so that my leader + space > S takes me back to cockpit if i click in from cockpit

## Outcomes

1. Cockpit reads spine's fleet snapshot by running `spine bearings --json` on the same cadence as its local sources. If spine is not installed, the command fails or the JSON does not parse, the spine source is listed as unreadable with the reason, as Cockpit does for every source; Cockpit never crashes or hides it. The snapshot's shape is the one in `testdata/spine/bearings.json`.
2. The grid shows one "spine" tile beside the session tiles. It shows how many items need Jeff (or that nothing does), how many goals and agents are underway, and the spend against the cap across underway goals. Below 70 columns it collapses to one line: the status marker, "spine" and the needs-you count.
3. With the spine tile selected, the preview area shows the snapshot's four sections (Needs you, Underway with each item's "Now" line, Charted next, Landed in the last 24 hours), each saying "none" when empty.
4. Every Needs-you item from the snapshot appears in the attention queue (`a`), naming its repository, goal and title.
5. Enter on the spine tile, or on a spine item in the attention queue, switches to a tmux session named `spine`, creating it when absent. The session runs the command set by `spine.command` in Cockpit's config. The default runs `spine tui`, falls back to `spine bearings` when that fails, then leaves a shell. Jeff's existing key for returning to Cockpit works from it unchanged.
6. `cockpit doctor` reports whether `spine` is on PATH and whether its snapshot can be read, with the smallest next action when not.
7. `go build ./...`, `go vet ./...` and `go test ./...` pass.

## Constraints

- Go, standard library plus the dependencies Cockpit already has.
- Tests never run the real `spine`, tmux or gh: the spine source is behind the same kind of seam as Cockpit's other sources, fed from `testdata/spine/bearings.json`.
- Cockpit only reads spine; it never runs a command that changes spine state (no approve, answer, extend, stop or start).
- The tile and preview follow Cockpit's existing styles; spine's own TUI look is a separate goal.

## Out of scope

- The spine TUI itself.
- Acting on asks from Cockpit (approvals stay in spine's console with Touch ID or Face ID).
- Remote hosts' spine fleets.

## Budget
cost: 20      # USD-equivalent, every model session
hours: 12     # wall clock
