# TODOS

## Design Debt

### Micro-interaction spec (polish phase)
**What:** Define transition behavior for loading→data, task toggle feedback, and panel focus changes.
**Why:** The difference between "works" and "feels crafted." Cockpit's design DNA is "the whole package."
**Context:** Bubbletea handles frame-level rendering. Decisions needed: snap vs fade for loading→data, checkbox toggle visual feedback (brief color flash?), panel focus transition. Address during Next Steps #9 (Polish).
**Depends on:** Core panel implementation (steps 1-8).

## Implementation Decisions

### `cockpit init` overwrite behavior
**What:** Decide what happens when `cockpit init` is run and `~/.config/cockpit/config.toml` already exists.
**Why:** Prevents accidental config loss. Most CLI tools error with "config already exists, use --force to overwrite."
**Context:** Safe default is error + `--force` flag. Alternative: show diff between template and existing config, or prompt interactively.
**Depends on:** Scaffolding (step 1).
