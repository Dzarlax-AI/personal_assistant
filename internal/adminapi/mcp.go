package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"telegram-agent/internal/config"
	"telegram-agent/internal/llm"
)

// SettingKeyMCPServers is the kv_settings key holding the current MCP
// server list (JSON-encoded map[name]MCPServerConfig). Source of truth —
// whatever the admin UI writes wins over the legacy mcp.json file at
// startup.
const SettingKeyMCPServers = "cfg.mcp.servers"

// LoadMCPServersFromSettings reads the DB-backed MCP list. Returns (nil,
// false, nil) when no override is set so the caller can fall back to the
// legacy file. Returns an error only on corrupted JSON — missing value is
// not an error.
func LoadMCPServersFromSettings(ctx context.Context, s llm.SettingsStore) (map[string]config.MCPServerConfig, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	v, ok, err := s.GetSetting(ctx, SettingKeyMCPServers)
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return nil, false, err
	}
	var out map[string]config.MCPServerConfig
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, true, fmt.Errorf("parse mcp servers from settings: %w", err)
	}
	return out, true, nil
}

// saveMCPServers writes the full MCP list to kv_settings. Replaces the
// entire value — the admin UI always sends a complete picture. Also
// mirrors the list to MCP_BRIDGE_EXPORT_PATH when set (for claude-bridge).
func saveMCPServers(ctx context.Context, s llm.SettingsStore, servers map[string]config.MCPServerConfig) error {
	if s == nil {
		return fmt.Errorf("settings store not available")
	}
	data, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	if err := s.PutSetting(ctx, SettingKeyMCPServers, string(data)); err != nil {
		return err
	}
	// Best-effort mirror for claude-bridge — failures are logged upstream
	// but don't roll back the DB write.
	if path := os.Getenv("MCP_BRIDGE_EXPORT_PATH"); path != "" {
		if err := writeBridgeMCPFile(path, servers); err != nil {
			return fmt.Errorf("mirror to bridge file: %w", err)
		}
	}
	return nil
}

// writeBridgeMCPFile renders the MCP server map in Claude Desktop format
// (wrapped in {"mcpServers": {...}}) and writes it atomically. The bridge
// reads this file once per `claude -p` invocation so no restart is needed
// on either side.
func writeBridgeMCPFile(path string, servers map[string]config.MCPServerConfig) error {
	wrapper := map[string]any{"mcpServers": servers}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	// Ensure parent dir exists — the first deploy may run before the path is created.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Atomic write via tempfile + rename so claude-bridge never observes a
	// half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sortedMCPServerNames returns map keys sorted alphabetically for stable UI
// rendering.
func sortedMCPServerNames(m map[string]config.MCPServerConfig) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
