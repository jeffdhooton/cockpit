package main

const configTemplate = `# Cockpit — tmux-native Terminal Command Center
# Configuration file

[general]
# Name of the tmux session cockpit runs in
session_name = "cockpit"
# How often to refresh local sources (tmux, git, obsidian) in seconds
refresh_interval = 5

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
`
