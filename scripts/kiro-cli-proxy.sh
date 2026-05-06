#!/bin/bash
# Launch kiro-cli through the kiro-think MITM proxy.
# Reads listen port from ~/.kiro-think/config.json automatically.
# Usage: ./kiro-cli-proxy.sh [kiro-cli args...]

CONFIG="$HOME/.kiro-think/config.json"
PORT=8960

if [ -f "$CONFIG" ]; then
  # Extract port number from "listen": ":8960" using grep+sed (no jq dependency)
  P=$(grep -o '"listen"[[:space:]]*:[[:space:]]*"[^"]*"' "$CONFIG" | grep -o '[0-9]\+')
  [ -n "$P" ] && PORT="$P"
fi

exec env \
  SSL_CERT_FILE="$HOME/.kiro-think/combined-ca.crt" \
  HTTPS_PROXY="http://127.0.0.1:$PORT" \
  HTTP_PROXY="http://127.0.0.1:$PORT" \
  kiro-cli chat "$@"
