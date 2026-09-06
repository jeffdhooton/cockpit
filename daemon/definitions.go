package daemon

// obj is a shorthand for a JSON schema object node.
func obj(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// str describes a string property.
func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// integer describes an integer property.
func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// boolean describes a boolean property.
func boolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

const projectDesc = "Project label — the same string cockpit uses as the tmux session name. A remote project is addressed as host/label, or with the host argument."
const processDesc = "Process name, which is its tmux window name"

// Definitions returns every tool this daemon exposes. The names and argument
// shapes match Helm's tool suite so agents written against one work against
// the other.
func (t *Tools) Definitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "cockpit_list_projects",
			Description: "List all configured projects with session state and process health",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "cockpit_list_processes",
			Description: "List a project's processes with state (running, dead, not_started), desired_state (running, or stopped when the user stopped it on purpose), outcome, exit_code, window and pane ids, and whether cockpit manages the window. " +
				"Also reports windows the config does not declare, such as spawned agents. A failed read returns outcome unavailable rather than an empty list.",
			InputSchema: obj(map[string]any{"project": str(projectDesc), "host": str("Configured host name for a remote project (optional)")}, "project"),
		},
		{
			Name: "cockpit_read_output",
			Description: "Read the last N lines of a process's terminal output. Works for configured processes and for any open window, including spawned agents. " +
				"Reports what it dropped: blank_lines_removed counts collapsed pane padding, and truncated means the capture reached the requested scrollback so older output exists above it.",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc + " (or pass pane_id)"),
				"pane_id": str("Pane id (%N) from cockpit_workspaces, as an alternative to process; must belong to the project's session"),
				"lines":   integer("Number of lines of scrollback (default 100, max 10000)"),
			}, "project"),
		},
		{
			Name:        "cockpit_start",
			Description: "Start a configured process. A process that is already running is left alone; a dead window is reused rather than duplicated. Clears a previous stop. Refused when a window of that name exists that cockpit did not start.",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc),
			}, "project", "process"),
		},
		{
			Name:        "cockpit_stop",
			Description: "Stop a running process by closing its window. The stop is remembered on the tmux session, so the process stays stopped across project entry and cockpit restarts until cockpit_start is called; it is refused for a window cockpit did not start, or one that has been split.",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc),
			}, "project", "process"),
		},
		{
			Name:        "cockpit_restart",
			Description: "Restart a process in the window it already holds",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc),
			}, "project", "process"),
		},
		{
			Name:        "cockpit_signals",
			Description: "Get everything that needs attention across all projects: dead processes, failing checks, unpushed commits, and stale sessions, most urgent first",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "cockpit_workspaces",
			Description: "Read the workspace tree: every host, its live tmux sessions, and each session's panes classified as agent (hook record or a claude/codex foreground command; status is unknown until a hook reports), process (a configured process, with the same display text as cockpit_list_processes), shell, or other, plus each session's status rollup, agent and process counts, project branch summary, and configured repos with no session. " +
				"Read-only: no previews, no navigation, nothing started; read a pane with cockpit_read_output and pane_id. schema_version 1.",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "cockpit_attention",
			Description: "Read the attention queue: every item that needs a person, with a stable id, kind, priority, host, project, typed target, evidence source, and freshness, plus per-source coverage saying what could and could not be observed. " +
				"Read-only: it navigates nowhere, dismisses nothing and starts nothing. Housekeeping items (unpushed commits, stale sessions) are flagged so they do not count as urgent. schema_version 1.",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "cockpit_git_status",
			Description: "Get git status for one or all projects: branch, dirty count, unpushed and behind counts, last commit",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc + " (optional — omit for all)"),
			}),
		},
		{
			Name: "cockpit_spawn_agent",
			Description: "Start a long-running, externally-visible agent (claude, codex, gemini, a shell) in its own tmux window. " +
				"The window appears alongside the project's other processes, outlives this conversation, and you interact with it through further calls to cockpit_read_output and cockpit_write_input. " +
				"This is not the built-in sub-task tool: a sub-task returns one result and ends, while this creates a real process the user can watch and talk to. " +
				"Use it when asked to kick off, launch, or hand work to a parallel session. Pass 'prompt' to deliver an opening instruction once the agent has finished booting.",
			InputSchema: obj(map[string]any{
				"command":     str("Command to run, for example 'claude' or 'codex'"),
				"name":        str("Window name (generated when omitted)"),
				"project":     str(projectDesc + " (defaults to the first configured project)"),
				"working_dir": str("Working directory, absolute or relative to the project root"),
				"env": map[string]any{
					"type":                 "object",
					"description":          "Additional environment variables",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"prompt": str("Opening instruction, typed once the agent's startup output goes quiet. " +
					"The call waits for delivery and reports the outcome in prompt_delivery; a process that " +
					"exited first is reported as not delivered rather than typed into."),
			}, "command"),
		},
		{
			Name:        "cockpit_write_input",
			Description: "Type text into a running process's terminal. Appends Enter by default.",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc),
				"input":   str("Text to type"),
				"submit":  boolean("Press Enter afterwards (default true). Set false for partial input."),
			}, "project", "process", "input"),
		},
		{
			Name:        "cockpit_whoami",
			Description: "Identify this cockpit instance — version, process id, port, config path, and live tmux sessions",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name: "cockpit_status",
			Description: "Get typed status events for a process, matched from its terminal scrollback using the regexes set under [repos.processes.status] in config.toml. " +
				"Use it to answer 'is the dev server up?' without reading raw output. " +
				"A process with no patterns configured returns no events — fall back to cockpit_read_output. " +
				"Events come from tmux scrollback rather than a live stream, so they reach back only as far as the pane's history.",
			InputSchema: obj(map[string]any{
				"project": str(projectDesc),
				"process": str(processDesc + " (optional — omit for every process in the project)"),
				"limit":   integer("Max events per process (default 16, max 64). Newest first; each process reports an omitted count for matches beyond the limit."),
			}, "project"),
		},
		{
			Name:        "cockpit_capture",
			Description: "Capture a thought to today's list for later triage — the same thing `cockpit cap` does from a terminal",
			InputSchema: obj(map[string]any{
				"text": str("What to capture"),
			}, "text"),
		},
		{
			Name:        "cockpit_tasks",
			Description: "Read today's task list, optionally toggling one checkbox first",
			InputSchema: obj(map[string]any{
				"toggle_line": integer("Line number of a task to toggle before reading (optional)"),
			}),
		},
	}
}
