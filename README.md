# Personal AI Agent

A lightweight Telegram bot that acts as a personal AI assistant. Written in Go — runs on a NAS, Raspberry Pi, or any small server. Bring your own API keys, no subscriptions.

## Features

- **Multi-model routing** — DeepSeek Chat as primary; Gemini Flash Lite as automatic fallback on errors or rate limits; DeepSeek Reasoner for complex tasks; Gemini Flash for images; Qwen models optional
- **Image support** — send a photo (with or without caption) and it's routed automatically to the vision model
- **Forwarded messages** — forward any message (text, photo, link) to the bot, then ask your question; messages arriving within 2 s are batched automatically
- **Link extraction** — hidden hyperlinks (`text_link` entities) in forwarded messages are surfaced as plain URLs for the LLM
- **MCP tool support** — connects to any MCP-compatible server (HTTP/SSE), same `mcp.json` format as Claude Desktop; per-server `allowTools`/`denyTools` filtering; vector similarity filtering selects only the most relevant tools per request
- **Persistent memory** — SQLite-backed conversation history with automatic session management
- **Context compaction** — auto-summarises old history to stay within token limits
- **Rich formatting** — Markdown converted to Telegram HTML; responses ≥ 4096 chars sent as `response.md`
- **Access control** — allowlist by chat ID + owner-only enforcement
- **Date/time awareness** — current date and time injected into every request; timezone set via `TZ` env var

## Requirements

- Go 1.24+ (or Docker)
- [Telegram Bot Token](https://t.me/BotFather)
- [DeepSeek API key](https://platform.deepseek.com)
- Gemini API key (optional — for fallback, image support, and tool filtering)
- Qwen API key (optional — for classifier and additional models)

## Quick start (NAS / Pi / server)

No source code needed — just pull the pre-built image:

```bash
# 1. Create the directory structure
mkdir -p my-assistant/config my-assistant/data
cd my-assistant

# 2. Download compose file
curl -O https://raw.githubusercontent.com/dzarlax/personal_assistant/main/docker-compose.yml

# 3. Create config files
curl -o config/config.yaml https://raw.githubusercontent.com/dzarlax/personal_assistant/main/config/config.yaml
curl -o config/system_prompt.md https://raw.githubusercontent.com/dzarlax/personal_assistant/main/config/system_prompt.md.example
curl -o config/mcp.json https://raw.githubusercontent.com/dzarlax/personal_assistant/main/config/mcp.json.example
curl -o .env https://raw.githubusercontent.com/dzarlax/personal_assistant/main/.env.example

# 4. Fill in secrets
nano .env

# 5. Start
docker compose up -d
docker compose logs -f
```

## Setup (from source)

```bash
cp .env.example .env
# fill in your API keys and Telegram token

cp config/mcp.json.example config/mcp.json
# configure MCP servers (optional)

cp config/system_prompt.md.example config/system_prompt.md
# personalise the assistant

mkdir -p data
```

**Get your Telegram chat ID:** send `/start` to [@userinfobot](https://t.me/userinfobot).

## Running

**Local:**
```bash
make run
```

**Docker (from source):**
```bash
make docker-up   # copies missing example files, then starts
make logs
```

Data is stored in `./data/conversations.db` (mounted as a volume in Docker).

## Project layout

```
.env                   # secrets — not in git
.env.example           # template
config/
  config.yaml          # model and routing config
  system_prompt.md     # personalise the assistant here
  mcp.json             # MCP servers — not in git
  mcp.json.example     # template
data/                  # SQLite DB — not in git
```

## Configuration

### `config/config.yaml`

All values support `${ENV_VAR}` substitution. Every model requires an explicit `base_url`.

```yaml
telegram:
  bot_token: ${TELEGRAM_BOT_TOKEN}
  allowed_chat_ids:
    - ${TELEGRAM_OWNER_CHAT_ID}
  owner_chat_id: ${TELEGRAM_OWNER_CHAT_ID}

models:
  deepseek:
    provider: deepseek
    model: deepseek-chat
    api_key: ${DEEPSEEK_API_KEY}
    max_tokens: 4096
    base_url: https://api.deepseek.com
  deepseek-r1:
    provider: deepseek
    model: deepseek-reasoner
    api_key: ${DEEPSEEK_API_KEY}
    max_tokens: 8192
    base_url: https://api.deepseek.com
  gemini-flash-lite:
    provider: gemini
    model: gemini-3.1-flash-lite-preview
    api_key: ${GEMINI_API_KEY}
    max_tokens: 2048
    base_url: https://generativelanguage.googleapis.com/v1beta/openai/
  gemini-flash:
    provider: gemini
    model: gemini-3-flash-preview
    api_key: ${GEMINI_API_KEY}
    max_tokens: 4096
    base_url: https://generativelanguage.googleapis.com/v1beta/openai/
  embedding:
    provider: gemini
    model: gemini-embedding-001
    api_key: ${GEMINI_API_KEY}
  qwen-flash:             # optional — used as classifier by default
    provider: qwen
    model: qwen3.5-flash
    api_key: ${QWEN_API_KEY}
    max_tokens: 4096
    base_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1
  qwen3.5-plus:           # optional
    provider: qwen
    model: qwen3.5-122b-a10b
    api_key: ${QWEN_API_KEY}
    max_tokens: 4096
    base_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1
  qwen-max:               # optional
    provider: qwen
    model: qwen3-max
    api_key: ${QWEN_API_KEY}
    max_tokens: 8192
    base_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1

routing:
  default: deepseek
  fallback: gemini-flash-lite
  multimodal: gemini-flash
  reasoner: deepseek-r1
  classifier: qwen-flash        # model for reasoning detection; omit to disable
  classifier_min_length: 100    # min chars to run classifier; 0 = disabled
  compaction_model: deepseek

tool_filter:
  top_k: 20   # top-K tools selected per request via vector similarity; 0 = disabled
```

### `config/mcp.json`

Same format as Claude Desktop. Supports custom headers for auth and per-server tool filtering.

```json
{
  "mcpServers": {
    "my-server": {
      "url": "${SERVER_URL}",
      "headers": {
        "Authorization": "Bearer ${TOKEN}"
      },
      "denyTools": ["dangerous_tool"],
      "allowTools": []
    }
  }
}
```

- `denyTools` — block specific tools, allow the rest
- `allowTools` — allow only listed tools, block the rest
- Omit both to allow all tools from the server

### `config/system_prompt.md`

Plain text or Markdown injected as system prompt on every request.

## Bot Commands

| Command | Description |
|---|---|
| `/clear` | Reset conversation context |
| `/compact` | Summarise and compress history manually |
| `/model` | Show current model |
| `/model list` | List all available models |
| `/model <name>` | Switch to a specific model (e.g. `/model deepseek-r1`) |
| `/model reset` | Back to auto-routing |
| `/routing` | Configure routing roles via inline keyboard |
| `/tools` | List connected MCP tools grouped by server |
| `/help` | Show help |

## LLM Routing

| Priority | Model | When |
|---|---|---|
| 1 | `gemini-flash` | Message contains an image |
| 2 | `deepseek-r1` | `/model deepseek-r1` override, or classifier detects complex reasoning |
| 3 | `deepseek` | Default |
| 4 | `gemini-flash-lite` | Primary unavailable (5xx / 429 / network) |

The classifier (`qwen-flash` by default) is a lightweight call with no history and no tools that returns `yes`/`no`. It only runs for messages longer than `classifier_min_length` characters (default: 100). Set `classifier_min_length: 0` to disable. If the classifier model is not configured, auto-routing to reasoner is skipped.

All routing roles can be changed live via `/routing` — an inline keyboard menu. Changes persist across restarts in `config/routing.json`. On startup, the bot notifies the owner via Telegram if any routing role references an unavailable model and offers an inline picker to select a replacement.

## Session Management

- History persists across restarts (SQLite)
- After **4 hours of inactivity**, a new session starts automatically — the last summary is carried over
- `/clear` does a full reset with no carry-over

## Architecture

```mermaid
flowchart TD
    User(["📱 Telegram"])
    Handler["Telegram Handler"]
    Agent["Agent\nagentic loop"]
    Router["LLM Router"]
    MCP["MCP Client"]
    Store[("SQLite")]
    Emb["Gemini Embedding\ngemini-embedding-001"]

    subgraph LLMs ["LLM Providers"]
        DS["deepseek"]
        DSR["deepseek-r1"]
        GL["gemini-flash-lite"]
        GM["gemini-flash"]
        QW["qwen-* (optional)"]
    end

    subgraph Servers ["MCP Servers"]
        S1["personal-memory"]
        S2["health-dashboard"]
        S3["..."]
    end

    User -->|"text / photo / forward"| Handler
    Handler --> Agent
    Agent <--> Store
    Agent --> Router
    Agent -->|"query embed\ntop-K filter"| MCP
    MCP <-->|"embed tools\nat startup"| Emb
    MCP <-->|"tools/call"| Servers
    Router --> DS
    Router --> DSR
    Router --> GL
    Router --> GM
    Router --> QW
    MCP --> S1
    MCP --> S2
    MCP --> S3
```

See [CLAUDE.md](CLAUDE.md) for developer details.
