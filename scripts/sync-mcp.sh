#!/bin/bash
# Sync MCP servers from config/mcp.json to Claude Code CLI.
# Claude Code doesn't auto-load .mcp.json in -p mode — servers must be
# registered via `claude mcp add`. This script reads the bot's mcp.json
# and (re)adds each server to Claude CLI's local config.
#
# Usage: ./scripts/sync-mcp.sh [path/to/mcp.json]
set -e

CLAUDE="${CLAUDE_BRIDGE_CLI:-claude}"
MCP_JSON="${1:-config/mcp.json}"
PROJECT_DIR="${CLAUDE_BRIDGE_PROJECT_DIR:-$(pwd)}"

if [ ! -f "$MCP_JSON" ]; then
  echo "ERROR: $MCP_JSON not found" >&2
  exit 1
fi

if ! command -v "$CLAUDE" &>/dev/null; then
  echo "ERROR: claude CLI not found (set CLAUDE_BRIDGE_CLI)" >&2
  exit 1
fi

cd "$PROJECT_DIR"

echo "Syncing MCP servers from $MCP_JSON to Claude CLI (project: $PROJECT_DIR)..."

# Remove existing local MCP servers to avoid stale entries
for name in $(claude mcp list 2>/dev/null | grep -oP '^\S+(?=:)'); do
  claude mcp remove -s local "$name" 2>/dev/null && echo "  removed: $name"
done

# Parse mcp.json and add each server
python3 -c '
import json, sys, subprocess, os

with open(sys.argv[1]) as f:
    cfg = json.load(f)

claude = os.environ.get("CLAUDE_BRIDGE_CLI", "claude")

for name, server in cfg.get("mcpServers", {}).items():
    url = server.get("url", "")
    if not url:
        print(f"  skip {name}: no url")
        continue

    cmd = [claude, "mcp", "add", "-t", "http", "-s", "local"]
    for key, val in server.get("headers", {}).items():
        cmd.extend(["-H", f"{key}: {val}"])
    cmd.extend(["--", name, url])

    result = subprocess.run(cmd, capture_output=True, text=True)
    if result.returncode == 0:
        print(f"  added: {name} -> {url}")
    else:
        err = result.stderr.strip()
        print(f"  FAIL:  {name} -> {err}")
' "$MCP_JSON"

echo ""
echo "Verifying..."
claude mcp list
