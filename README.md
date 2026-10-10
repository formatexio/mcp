# formatex-mcp

MCP server for the [FormaTeX](https://formatex.io) LaTeX compilation API. It lets an MCP client such as Claude, Claude Code, Cursor, VS Code, Codex, Windsurf, Zed or Gemini CLI compile LaTeX to PDF without a local TeX installation.

There are two ways to use it:

- **Hosted endpoint, nothing to install.** Point a client that speaks Streamable HTTP at `https://mcp.formatex.io/mcp`.
- **Local binary over stdio.** Install `formatex-mcp` (below) and let the client start it.

You need a FormaTeX API key either way. Keys start with `fex_` and live at [formatex.io/dashboard/api-keys](https://formatex.io/dashboard/api-keys).

## Install the local binary

Skip this section if you only use the hosted endpoint.

### Option A: go install

```bash
go install github.com/formatexio/mcp/cmd/formatex-mcp@latest
```

The binary `formatex-mcp` lands in `$(go env GOPATH)/bin`; make sure that directory is on your `PATH`.

### Option B: release binary

Download the file for your platform from [GitHub Releases](https://github.com/formatexio/mcp/releases):

| Platform | File |
|---|---|
| Linux x86_64 | `formatex-mcp-linux-amd64` |
| Linux ARM64 | `formatex-mcp-linux-arm64` |
| macOS Intel | `formatex-mcp-darwin-amd64` |
| macOS Apple Silicon | `formatex-mcp-darwin-arm64` |
| Windows x64 | `formatex-mcp-windows-amd64.exe` |

Each release also carries `checksums.txt` with the SHA-256 of every file. Compare it with `sha256sum <file>` (Linux) or `shasum -a 256 <file>` (macOS), then make the file executable and put it on your `PATH`:

```bash
chmod +x formatex-mcp-darwin-arm64
mv formatex-mcp-darwin-arm64 /usr/local/bin/formatex-mcp
formatex-mcp --version
```

The macOS binaries are not notarized. If macOS refuses to open a file you downloaded with a browser, remove its quarantine attribute with `xattr -d com.apple.quarantine /usr/local/bin/formatex-mcp`.

## Set up your client

Every example shows the hosted endpoint first. Replace `fex_your_key_here` with your key and keep files that contain it out of version control. The Claude Code, Cursor, VS Code and Codex hosted examples read the key from an environment variable or a prompt instead.

### Claude Desktop and claude.ai (hosted)

1. Open Customize, then Connectors, then Add custom connector.
2. Enter the URL `https://mcp.formatex.io/mcp`.
3. Choose Sign in and paste your key on the FormaTeX authorize page. Or skip the sign-in and add the request header `Authorization: Bearer <your key>`.

`claude_desktop_config.json` is only for local stdio servers; remote servers are added as connectors.

### Claude Desktop (local binary)

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

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

If the app cannot find the binary, set `command` to its absolute path, for example `/usr/local/bin/formatex-mcp`, or `"C:\\Users\\you\\go\\bin\\formatex-mcp.exe"` on Windows (JSON needs the backslashes doubled).

### Claude Code

Hosted:

```bash
claude mcp add --transport http formatex https://mcp.formatex.io/mcp \
  --header "Authorization: Bearer $FORMATEX_API_KEY"
```

Local binary:

```bash
claude mcp add --env FORMATEX_API_KEY=fex_your_key_here --transport stdio formatex -- formatex-mcp
```

To share the server with a project, put a `.mcp.json` at the project root (not in `.claude/`). An `http` entry needs `"type": "http"`, and `${VAR}` is expanded from your environment:

```json
{
  "mcpServers": {
    "formatex": {
      "type": "http",
      "url": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer ${FORMATEX_API_KEY}"
      }
    }
  }
}
```

### Cursor

Add the server to `.cursor/mcp.json` in a project or to `~/.cursor/mcp.json` for all projects. Hosted:

```json
{
  "mcpServers": {
    "formatex": {
      "url": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer ${env:FORMATEX_API_KEY}"
      }
    }
  }
}
```

For the local binary use `"command": "formatex-mcp"` and `"env": { "FORMATEX_API_KEY": "fex_your_key_here" }` in place of `url` and `headers`.

### VS Code

Add `.vscode/mcp.json`. The top-level key is `servers`, not `mcpServers`. An `inputs` entry makes VS Code prompt for the key once and keep it out of the file:

```json
{
  "inputs": [
    {
      "type": "promptString",
      "id": "formatex-api-key",
      "description": "FormaTeX API key",
      "password": true
    }
  ],
  "servers": {
    "formatex": {
      "type": "http",
      "url": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer ${input:formatex-api-key}"
      }
    }
  }
}
```

For the local binary use `"type": "stdio"`, `"command": "formatex-mcp"` and `"env": { "FORMATEX_API_KEY": "${input:formatex-api-key}" }`.

### Codex

Edit `~/.codex/config.toml`. Hosted, reading the key from the `FORMATEX_API_KEY` environment variable:

```toml
[mcp_servers.formatex]
url = "https://mcp.formatex.io/mcp"
bearer_token_env_var = "FORMATEX_API_KEY"
```

Local binary:

```toml
[mcp_servers.formatex]
command = "formatex-mcp"
env_vars = ["FORMATEX_API_KEY"]
```

Or add the local server from the command line:

```bash
codex mcp add formatex --env FORMATEX_API_KEY=fex_your_key_here -- formatex-mcp
```

### Windsurf

Windsurf calls the remote URL `serverUrl`. In `mcp_config.json`:

```json
{
  "mcpServers": {
    "formatex": {
      "serverUrl": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer fex_your_key_here"
      }
    }
  }
}
```

### Zed

Zed keeps MCP servers under `context_servers` in its `settings.json`:

```json
{
  "context_servers": {
    "formatex": {
      "url": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer fex_your_key_here"
      }
    }
  }
}
```

### Gemini CLI

Gemini CLI calls the remote URL `httpUrl`. In `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "formatex": {
      "httpUrl": "https://mcp.formatex.io/mcp",
      "headers": {
        "Authorization": "Bearer fex_your_key_here"
      }
    }
  }
}
```

### Any other client

- Streamable HTTP: URL `https://mcp.formatex.io/mcp` with the header `Authorization: Bearer <key>` or `X-API-Key: <key>`. The endpoint does not serve SSE.
- stdio: run `formatex-mcp` with `FORMATEX_API_KEY` in its environment.

## Tools

Every compile tool returns a download URL for the PDF.

| Tool | What it does | Uses quota |
|---|---|---|
| `compile_smart` | Compile with automatic engine detection and error fixing. Recommended for most documents. | Yes |
| `compile_latex` | Compile with an explicit engine: `pdflatex`, `xelatex`, `lualatex` or `latexmk`. | Yes |
| `compile_async` | Submit a compilation job and return a job ID immediately. | Yes |
| `compile_template` | Compile a saved template by ID with variable substitutions (Pro plan and higher). | Yes |
| `check_syntax` | Validate LaTeX without compiling. | No |
| `list_engines` | List the available engines and when to use each. | No |
| `get_usage` | Show this month's usage and your plan limits. | No |
| `get_job` | Poll an async job: PDF URL when done, error log when failed. | No |
| `list_templates` | List your saved templates (Pro plan and higher). | No |
| `fetch_pdf_bytes` | Fetch a finished job's PDF as base64 bytes. Only needed to save or inspect the file. | No |

Each tool carries a title and annotations, so clients can auto-approve the read-only ones.

### Resources

| URI | Content |
|---|---|
| `formatex://engines` | Available engines with guidance on when to use each. |
| `formatex://usage` | Current month usage and plan limits. |

### Prompt

`fix_latex` takes `latex_source`, `error_log` and an optional `engine`, and returns structured instructions for diagnosing and fixing a compilation error.

## Self-hosting

Run the same binary as an HTTP server:

```bash
formatex-mcp --http :9004
```

In HTTP mode the server holds no API key of its own: every request to `/mcp` carries the caller's key as `Authorization: Bearer <key>` or `X-API-Key: <key>`, and a request without one gets `401`. This is how `mcp.formatex.io` runs.

### Flags and environment

| Flag | Environment variable | Default | Description |
|---|---|---|---|
| `--api-key` | `FORMATEX_API_KEY` | none | API key. Required in stdio mode, not used in HTTP mode. |
| `--api-url` | `FORMATEX_API_URL` | `https://api.formatex.io/` | FormaTeX API base URL. |
| `--base-url` | `MCP_BASE_URL` | none | Public origin of this server, for example `https://mcp.example.com`. Setting it enables the OAuth endpoints below. |
| none | `MCP_OAUTH_SECRET` | random per process | Key that signs OAuth client ids, at least 32 bytes. Keep it identical across restarts and deploys, otherwise registered clients stop working and must register again. Only read when a base URL is set. |
| `--http` | none | none | Listen address for HTTP mode, for example `:9004`. Omit it for stdio. |
| `--version` | none | none | Print the version and exit. |

A flag overrides its environment variable.

### Endpoints

| Endpoint | Purpose |
|---|---|
| `/mcp` | MCP over Streamable HTTP. Needs an API key. |
| `GET /health` | Liveness probe, returns `{"status":"ok","service":"formatex-mcp","version":"..."}`. |

With a base URL set, the server also acts as the OAuth 2.1 authorization server that remote MCP clients use to sign in:

| Endpoint | Purpose |
|---|---|
| `GET /.well-known/oauth-protected-resource` | RFC 9728 protected resource metadata. Also served under `/mcp`. |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 authorization server metadata. |
| `POST /register` | RFC 7591 dynamic client registration. |
| `GET, POST /authorize` | Consent page where the user pastes a FormaTeX API key. |
| `POST /token` | Exchanges the authorization code (PKCE `S256`) for a bearer token. |

The bearer token is the API key the user pasted, so the server issues no credential of its own. The POST endpoints are rate limited per client IP. With a base URL set, the `401` from `/mcp` carries a `WWW-Authenticate` header that points clients at the resource metadata.

## Development

```bash
go vet ./...
go test ./... -race -count=1
go build -o formatex-mcp ./cmd/formatex-mcp
```

The server is developed in a private repository. `internal/app` and `internal/mcp` are generated from it by `scripts/sync-from-api.sh /path/to/formatex-api`, so edit them there and sync rather than patching them here.

## License

MIT
