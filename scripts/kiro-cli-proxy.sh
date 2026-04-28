#!/bin/bash
# Launch kiro-cli through the kiro-think MITM proxy.
# Usage: ./kiro-cli-proxy.sh [kiro-cli args...]
exec env \
  SSL_CERT_FILE="$HOME/.kiro-think/combined-ca.crt" \
  HTTPS_PROXY="http://127.0.0.1:8960" \
  HTTP_PROXY="http://127.0.0.1:8960" \
  kiro-cli chat "$@"
