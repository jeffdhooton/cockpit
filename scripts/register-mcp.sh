#!/usr/bin/env bash
#
# Point agent tooling at the cockpit daemon instead of Helm.
#
# Rewrites the tool server entry in Claude Code's ~/.claude.json and Codex's
# ~/.codex/config.toml, backing both up first. Safe to run twice.

set -euo pipefail

PORT="${COCKPIT_PORT:-45679}"
URL="http://127.0.0.1:${PORT}/mcp"
STAMP="$(date +%Y%m%d-%H%M%S)"

CLAUDE_CONFIG="${HOME}/.claude.json"
CODEX_CONFIG="${HOME}/.codex/config.toml"

backup() {
	local file="$1"
	[ -f "$file" ] || return 0
	cp "$file" "${file}.bak-${STAMP}"
	echo "  backed up to ${file}.bak-${STAMP}"
}

register_claude() {
	if [ ! -f "$CLAUDE_CONFIG" ]; then
		echo "~/.claude.json not found — skipping Claude Code"
		return 0
	fi

	echo "Claude Code (${CLAUDE_CONFIG})"
	backup "$CLAUDE_CONFIG"

	COCKPIT_URL="$URL" CLAUDE_CONFIG="$CLAUDE_CONFIG" python3 <<'PY'
import json, os

path = os.environ["CLAUDE_CONFIG"]
url = os.environ["COCKPIT_URL"]

with open(path) as f:
    data = json.load(f)

servers = data.setdefault("mcpServers", {})
removed = servers.pop("helm", None)
before = servers.get("cockpit")
servers["cockpit"] = {"url": url}

with open(path, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")

if removed:
    print(f"  removed helm ({removed.get('url', '?')})")
if before == servers["cockpit"]:
    print("  cockpit already registered")
else:
    print(f"  registered cockpit -> {url}")
PY
}

register_codex() {
	if [ ! -f "$CODEX_CONFIG" ]; then
		echo "~/.codex/config.toml not found — skipping Codex"
		return 0
	fi

	echo "Codex (${CODEX_CONFIG})"
	backup "$CODEX_CONFIG"

	COCKPIT_URL="$URL" CODEX_CONFIG="$CODEX_CONFIG" python3 <<'PY'
import os, re

path = os.environ["CODEX_CONFIG"]
url = os.environ["COCKPIT_URL"]

with open(path) as f:
    text = f.read()

block = f'[mcp_servers.cockpit]\nurl = "{url}"\n'

# A server block is its header plus the non-empty lines under it. Stopping
# before blank lines leaves the spacing between tables untouched.
def find(name):
    return re.search(
        rf'^\[mcp_servers\.{name}\]\n(?:(?!\[)[^\n]+\n)*',
        text, re.MULTILINE)

helm = find("helm")
cockpit = find("cockpit")

if cockpit:
    if cockpit.group(0).strip() == block.strip():
        print("  cockpit already registered")
    else:
        text = text[:cockpit.start()] + block + text[cockpit.end():]
        print(f"  updated cockpit -> {url}")
    if helm := find("helm"):
        text = text[:helm.start()] + text[helm.end():]
        print("  removed helm")
elif helm:
    text = text[:helm.start()] + block + text[helm.end():]
    print(f"  replaced helm with cockpit -> {url}")
else:
    if not text.endswith("\n"):
        text += "\n"
    text += "\n" + block
    print(f"  registered cockpit -> {url}")

with open(path, "w") as f:
    f.write(text)
PY
}

echo "Pointing agent tooling at ${URL}"
echo
register_claude
echo
register_codex
echo
echo "Done. Restart any running agent session to pick up the change."
