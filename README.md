# kiro-think

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/briqt/kiro-think)](https://github.com/briqt/kiro-think/releases)

MITM proxy that injects thinking budget into Kiro CLI API requests, giving you control over Claude's reasoning depth.

Kiro CLI 思考深度注入代理 — 通过中间人代理拦截 Kiro CLI 的 API 请求，注入 thinking budget 标签，控制 Claude 的推理深度。

## How It Works

```
kiro-cli ──→ :8960 (kiro-think) ──→ upstream proxy ──→ AWS API
                    │
                    ├─ Intercept GenerateAssistantResponse
                    ├─ Inject <thinking>enabled</thinking><budget>24576</budget>
                    └─ Forward modified request (other requests pass through untouched)
```

Kiro CLI uses an internal AWS API (`GenerateAssistantResponse`) for chat. The thinking depth is controlled by XML tags injected into the first user message in conversation history. This proxy intercepts **only** those specific requests and injects/overrides the thinking budget tags before forwarding. All other traffic (including non-Kiro HTTPS) is tunneled through without decryption or modification.

## Prerequisites

- **Go 1.24+** (for building from source) or download a pre-built binary
- **Kiro CLI** installed and logged in
- **(Optional)** An upstream HTTP proxy — if you already use one (e.g. for VPN/corporate network), set `"upstream"` in config. By default kiro-think connects directly to AWS.

## Install

### From source

```bash
go install github.com/briqt/kiro-think@latest
```

### Pre-built binary

Download from [Releases](https://github.com/briqt/kiro-think/releases), extract, and place in your `$PATH`.

## Quick Start

```bash
# 1. Install
go install github.com/briqt/kiro-think@latest

# 2. Start the proxy (generates CA cert and config on first run)
kiro-think start

# 3. Launch kiro-cli through the proxy (one command!)
kiro-think run-kiro
```

That's it. `run-kiro` auto-starts the daemon if needed, sets the correct environment variables, and launches `kiro-cli chat`.

### Permanent setup

```bash
# Print a shell alias you can add to ~/.bashrc or ~/.zshrc
kiro-think setup
```

This outputs something like:

```bash
alias kiro='kiro-think run-kiro'
```

After adding it, just type `kiro` to start Kiro CLI with thinking injection.

### Why `SSL_CERT_FILE`?

kiro-think performs MITM decryption on target domains by generating a local CA certificate. Kiro CLI (built with Rust's `rustls`) loads trusted CAs from the system store **and** from `SSL_CERT_FILE`. The `combined-ca.crt` file merges your system CA bundle with the kiro-think CA, so Kiro CLI trusts the proxy's certificates without modifying your system trust store.

This only affects processes launched with `SSL_CERT_FILE` set — your system and other applications are not impacted.

## Commands

| Command | Description |
|---------|-------------|
| `kiro-think start` | Start proxy daemon |
| `kiro-think stop` | Stop proxy daemon |
| `kiro-think restart` | Restart proxy daemon |
| `kiro-think status` | Show status, current level, PID |
| `kiro-think level` | Show current thinking level |
| `kiro-think level <LEVEL>` | Set thinking level (hot-reload via SIGHUP) |
| `kiro-think run-kiro [args]` | Launch kiro-cli through the proxy (auto-starts daemon) |
| `kiro-think setup` | Print shell alias for permanent setup |
| `kiro-think run` | Run proxy in foreground (for debugging) |
| `kiro-think version` | Show version info |

## Thinking Levels

| Level | Budget Tokens | Description |
|-------|--------------|-------------|
| `low` | 4,096 | Most efficient, minimal thinking |
| `medium` | 10,000 | Balanced speed and reasoning |
| `high` | 20,000 | Claude default, deep reasoning |
| `xhigh` | 22,000 | Extended, for Opus 4.7 |
| `max` | 24,576 | Maximum capability |

Switch levels at runtime — the running proxy reloads automatically, no restart needed:

```bash
kiro-think level max    # Set to maximum thinking
kiro-think level low    # Set to minimal thinking
kiro-think level        # Show current level and all options
```

## Configuration

Config file: `~/.kiro-think/config.json` (auto-generated on first run)

```json
{
  "listen": ":8960",
  "upstream": "",
  "thinking": {
    "mode": "enabled",
    "level": "max",
    "budget": 24576
  },
  "log_file": "~/.kiro-think/kiro-think.log",
  "targets": [
    "q.us-east-1.amazonaws.com"
  ]
}
```

| Field | Description |
|-------|-------------|
| `listen` | Proxy listen address (e.g. `:8960` or `127.0.0.1:8960`) |
| `upstream` | Upstream HTTP proxy (empty = direct connection, no proxy needed) |
| `thinking.mode` | `"enabled"` (fixed budget) or `"adaptive"` (effort-based) |
| `thinking.level` | Effort level: `low` / `medium` / `high` / `xhigh` / `max` |
| `thinking.budget` | Budget tokens (auto-set when changing level) |
| `log_file` | Log file path (`~` is expanded) |
| `targets` | Hostnames to intercept; all others are tunneled through untouched |

## Files

| Path | Description |
|------|-------------|
| `~/.kiro-think/config.json` | Configuration |
| `~/.kiro-think/ca.crt` | Generated CA certificate |
| `~/.kiro-think/ca.key` | CA private key (keep secure) |
| `~/.kiro-think/combined-ca.crt` | CA + system certs bundle (for `SSL_CERT_FILE`) |
| `~/.kiro-think/kiro-think.pid` | Daemon PID file |
| `~/.kiro-think/kiro-think.log` | Log file |

## Scope of Impact

kiro-think is designed to be **surgical** — it only modifies what's necessary:

1. **Domain filtering**: Only hostnames listed in `targets` are MITM-decrypted. All other HTTPS traffic passes through as an opaque tunnel.
2. **API filtering**: Even for target domains, only `GenerateAssistantResponse` requests (identified by `x-amz-target` header) are modified. Other Kiro API calls (`GetProfile`, `ListAvailableModels`, `SendTelemetryEvent`) are forwarded unchanged.
3. **Environment scoping**: Only processes launched with `HTTPS_PROXY` pointing to kiro-think and `SSL_CERT_FILE` set to the combined CA are affected. Your system, browser, and other tools are untouched.

## Troubleshooting

### Port already in use

```
listen tcp :8960: bind: address already in use
```

Another process is using port 8960. Check with `ss -tlnp | grep 8960` or change `listen` in config.

### Kiro CLI shows TLS/certificate errors

- Verify `SSL_CERT_FILE` points to `~/.kiro-think/combined-ca.crt`
- Regenerate certs: delete `~/.kiro-think/ca.*` and `combined-ca.crt`, then restart kiro-think
- Check that the combined CA file includes your system certs: `wc -l ~/.kiro-think/combined-ca.crt` should show thousands of lines

### Kiro CLI hangs or times out

- Verify your upstream proxy is running: `curl -x http://127.0.0.1:3067 https://httpbin.org/get`
- Check kiro-think logs: `tail -f ~/.kiro-think/kiro-think.log`
- Try foreground mode for more visibility: `kiro-think run`

### Thinking injection not working

- Check logs for `💉 injected:` messages
- Verify `chat.enableThinking` is `true`: `kiro-cli settings chat.enableThinking`
- Ensure `targets` in config includes the correct AWS endpoint

### Daemon won't start

- Check if already running: `kiro-think status`
- Check log file for errors: `cat ~/.kiro-think/kiro-think.log`
- Try foreground mode: `kiro-think run`

## How the Injection Works

The Kiro API controls thinking via XML tags prepended to the system message in `conversationState.history`:

- **Enabled mode**: `<thinking>enabled</thinking><budget>24576</budget>`
- **Adaptive mode**: `<thinking>adaptive</thinking><effort>high</effort>`

This proxy:
1. Intercepts HTTPS CONNECT requests and checks if the hostname is in `targets`
2. For target hosts: terminates TLS with a dynamically generated certificate, decrypts the request
3. Identifies `GenerateAssistantResponse` by the `x-amz-target` header
4. Parses the JSON body, finds the first user message in `conversationState.history`
5. Strips existing `<thinking>`, `<budget>`, `<effort>` tags
6. Prepends the configured tags
7. Forwards the modified request through the upstream proxy

## Acknowledgments

- [kiro.rs](https://github.com/hank9999/kiro.rs) — Reverse-engineered Kiro API, discovered the thinking tag injection mechanism
- [kiro2api](https://github.com/caidaoli/kiro2api) — Early Kiro API research

## Disclaimer

This project is for research purposes only. Use at your own risk. Not affiliated with AWS, Kiro, Anthropic, or Claude.

## License

[Apache 2.0](LICENSE)
