package main

const configTemplate = `# Cockpit — tmux-native Terminal Command Center
# Configuration file

[general]
# Name of the tmux session cockpit runs in
session_name = "cockpit"
# How often to refresh local sources (tmux, git, obsidian) in seconds
refresh_interval = 5
# Startup view: "grid" (sessions + repos as one grid) or "dashboard" (all panels)
default_view = "grid"

[obsidian]
# Path to your Obsidian vault root
vault_path = "~/vault"
# Markdown file for today's tasks (relative to vault or absolute)
today_file = "~/vault/today.md"
# Markdown file for inbox captures (relative to vault or absolute)
inbox_file = "~/vault/inbox.md"

# Add repos to monitor. Each [[repos]] entry is one repository.
# [[repos]]
# path = "~/workspace/my-project"
# label = "my-project"
#
#   Background processes launch as tmux windows when you jump to the project.
#   Window 0 stays your shell and is where you land; each process gets its own
#   numbered window. Navigate with prefix+1..9, prefix+n/p, or prefix+w.
#   [[repos.processes]]
#   name = "dev"
#   command = "npm run dev"
#   auto_start = true
#   # working_dir = "packages/web"   # relative to the repo, or absolute
#   # env = { PORT = "3000" }
#
#     Optional patterns that cockpit_status matches against the process output.
#     [repos.processes.status]
#     ready = 'Local:\s+(\S+)'
#     error = 'error|failed'

# [[repos]]
# path = "~/workspace/another-project"
# label = "another"

[github]
# Enable GitHub PR and CI status checks (requires gh CLI)
enabled = true
# How often to refresh GitHub data in seconds
refresh_interval = 60

[signals]
# How long before a tmux session is considered stale
stale_session_threshold = "24h"
# Show stale tmux sessions in signals panel
show_stale_sessions = true
# Show repos with unpushed commits in signals panel
show_unpushed = true
# Show repos with failing CI in signals panel
show_failing_ci = true

[daemon]
# Serve the tool server so agents (Claude Code, Codex) can drive the workspace
enabled = true
# Port the tool server binds on loopback
port = 45679
`
