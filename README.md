# formatex-mcp

MCP server for the [FormaTeX](https://formatex.io) LaTeX compilation API. Compile LaTeX documents to PDF directly from Claude Desktop, Cursor, Claude Code, VS Code, n8n, and any other MCP-compatible AI client — no local TeX Live installation required.

## Installation

### Option A — go install

```bash
go install github.com/formatexio/mcp@latest
```

### Option B — Download binary

Grab the latest release from [GitHub Releases](https://github.com/formatexio/mcp/releases):

| Platform | File |
|---|---|
| Linux x86_64 | `formatex-mcp-linux-amd64` |
| Linux ARM64 | `formatex-mcp-linux-arm64` |
| macOS Intel | `formatex-mcp-darwin-amd64` |
| macOS Apple Silicon | `formatex-mcp-darwin-arm64` |
| Windows x64 | `formatex-mcp-windows-amd64.exe` |

Make it executable and move it to your PATH:

```bash
chmod +x formatex-mcp-darwin-arm64
mv formatex-mcp-darwin-arm64 /usr/local/bin/formatex-mcp
```

## Setup

You need a FormaTeX API key — get one at [formatex.io/dashboard/api-keys](https://formatex.io/dashboard/api-keys).

### Claude Desktop

Edit `%APPDATA%\Claude\claude_desktop_config.json` (Windows) or `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

```json
{
  "mcpServers": {
    "formatex": {
      "command": "/usr/local/bin/formatex-mcp",
      "env": {
        "FORMATEX_API_KEY": "fex_your_key_here"
      }
    }
  }
}
```

On Windows use the full path: `C:\\Users\\you\\go\\bin\\formatex-mcp.exe`

### Cursor / Claude Code / VS Code

```json
{
  "mcpServers": {
    "formatex": {
      "command": "formatex-mcp",
      "env": {
        "FORMATEX_API_KEY": "fex_your_key_here"
      }
    }
  }
}
```

### Remote HTTP (no binary)

Use the hosted endpoint instead:

```json
{
  "mcpServers": {
    "formatex": {
      "url": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer fex_your_key_here"
      }
    }
  }
}
```

## Available Tools

| Tool | Description | Uses quota |
|---|---|---|
| `compile_smart` | Auto-detect engine + auto-fix errors | Yes |
| `compile_latex` | Explicit engine choice | Yes |
| `check_syntax` | Validate LaTeX without compiling | No |
| `list_engines` | List available engines | No |
| `get_usage` | Check monthly usage & limits | No |
| `compile_async` | Submit async job, returns job ID | Yes |
| `get_job` | Poll async job, fetch PDF when done | No |
| `list_templates` | List saved templates (Pro+) | No |
| `compile_template` | Compile a saved template (Pro+) | Yes |

**Prompt:** `fix_latex` — diagnoses compilation errors and returns corrected source.

## HTTP server mode

Run the binary as a self-hosted MCP HTTP server:

```bash
formatex-mcp --http :9004
```

Each request must carry the user's API key via `Authorization: Bearer <key>` or `X-API-Key: <key>`. This is how `mcp.formatex.io` is deployed.

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `FORMATEX_API_KEY` | — | **Required** in stdio mode |
| `FORMATEX_API_URL` | `https://api.formatex.io/` | API base URL |

## License

MIT
