# kiro-think

MITM proxy that injects thinking budget into Kiro CLI API requests, giving you control over Claude's reasoning depth.

Kiro CLI 思考深度注入代理 — 通过中间人代理拦截 Kiro CLI 的 API 请求，注入 thinking budget 标签，控制 Claude 的推理深度。

## How It Works

```
kiro-cli ──→ :8960 (kiro-think) ──→ :3067 (upstream proxy) ──→ AWS API
                    │
                    ├─ Intercept GenerateAssistantResponse
                    ├─ Inject <thinking>enabled</thinking><budget>24576</budget>
                    └─ Forward modified request
```

Kiro CLI uses an internal AWS API (`GenerateAssistantResponse`) for chat. The thinking depth is controlled by XML tags injected into the first user message in conversation history. This proxy intercepts those requests and injects/overrides the thinking budget tags before forwarding.

## Install

```bash
go install github.com/briqt/kiro-think@latest
```

Or download from [Releases](https://github.com/briqt/kiro-think/releases).

## Quick Start

```bash
# 1. Start the proxy (generates CA cert on first run)
kiro-think start

# 2. Launch kiro-cli through the proxy
SSL_CERT_FILE=~/.kiro-think/combined-ca.crt \
HTTPS_PROXY=http://127.0.0.1:8960 \
kiro-cli chat

# Or use the helper script:
./scripts/kiro-cli-proxy.sh
```

## Commands

| Command | Description |
|---------|-------------|
| `kiro-think start` | Start proxy daemon |
| `kiro-think stop` | Stop proxy daemon |
| `kiro-think restart` | Restart proxy daemon |
| `kiro-think status` | Show status, current level, PID |
| `kiro-think level` | Show current thinking level |
| `kiro-think level <LEVEL>` | Set thinking level (hot-reload) |
| `kiro-think run` | Run in foreground (debug) |
| `kiro-think version` | Show version |

## Thinking Levels

| Level | Budget Tokens | Description |
|-------|--------------|-------------|
| `low` | 4,096 | Most efficient, minimal thinking |
| `medium` | 10,000 | Balanced speed and reasoning |
| `high` | 20,000 | Default, deep reasoning |
| `xhigh` | 22,000 | Extended, for Opus 4.7 |
| `max` | 24,576 | Maximum capability |

Switch levels at runtime:

```bash
kiro-think level max    # Set to maximum thinking
kiro-think level low    # Set to minimal thinking
```

The running proxy reloads automatically — no restart needed.

## Configuration

Config file: `~/.kiro-think/config.json` (auto-generated on first run)

```json
{
  "listen": ":8960",
  "upstream": "127.0.0.1:3067",
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
| `listen` | Proxy listen address |
| `upstream` | Upstream HTTP proxy (your existing proxy) |
| `thinking.mode` | `"enabled"` (fixed budget) or `"adaptive"` (effort-based) |
| `thinking.level` | Effort level name |
| `thinking.budget` | Budget tokens (auto-set from level) |
| `targets` | Hostnames to intercept (others are tunneled through) |

## Files

| Path | Description |
|------|-------------|
| `~/.kiro-think/config.json` | Configuration |
| `~/.kiro-think/ca.crt` | Generated CA certificate |
| `~/.kiro-think/ca.key` | CA private key |
| `~/.kiro-think/combined-ca.crt` | CA + system certs (for `SSL_CERT_FILE`) |
| `~/.kiro-think/kiro-think.pid` | Daemon PID file |
| `~/.kiro-think/kiro-think.log` | Log file |

## How the Injection Works

The Kiro API controls thinking via XML tags prepended to the system message in `conversationState.history`:

- **Enabled mode**: `<thinking>enabled</thinking><budget>24576</budget>`
- **Adaptive mode**: `<thinking>adaptive</thinking><effort>high</effort>`

This proxy:
1. Intercepts HTTPS requests via MITM (dynamic cert generation per hostname)
2. Identifies `GenerateAssistantResponse` requests by the `x-amz-target` header
3. Parses the JSON body, finds the first user message in history
4. Strips existing thinking/budget/effort tags
5. Prepends the configured tags
6. Forwards the modified request to the upstream proxy

## Acknowledgments

- [kiro.rs](https://github.com/hank9999/kiro.rs) — Reverse-engineered Kiro API, discovered the thinking tag injection mechanism
- [kiro2api](https://github.com/caidaoli/kiro2api) — Early Kiro API research

## Disclaimer

This project is for research purposes only. Use at your own risk. Not affiliated with AWS, Kiro, Anthropic, or Claude.

## License

[Apache 2.0](LICENSE)
