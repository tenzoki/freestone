#!/usr/bin/env bash
set -euo pipefail

# install.sh — build freestone-mcp, install it, and print setup instructions.
#
# Usage: ./scripts/install.sh

BINARY="freestone-mcp"
INSTALL_DIR="${HOME}/.local/bin"
CONFIG_DIR="${HOME}/.config/freestone"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

echo "==> Building ${BINARY}..."
go build -o "${BINARY}" ./cmd/freestone-mcp

echo "==> Installing to ${INSTALL_DIR}/${BINARY}..."
mkdir -p "$INSTALL_DIR"
cp "$BINARY" "${INSTALL_DIR}/${BINARY}"
rm -f "$BINARY"

echo "==> Installed."
echo ""

# Config setup
if [ ! -d "$CONFIG_DIR" ]; then
    echo "==> Creating config directory at ${CONFIG_DIR}..."
    mkdir -p "$CONFIG_DIR"
    cp example.config.toml "${CONFIG_DIR}/config.toml"
    cp example.access.rules "${CONFIG_DIR}/access.rules"
    echo "    Edit ${CONFIG_DIR}/config.toml to enable providers."
    echo "    Edit ${CONFIG_DIR}/access.rules to allow directories."
else
    echo "==> Config directory already exists at ${CONFIG_DIR}."
fi

echo ""
echo "==========================================="
echo "  Claude Code MCP Configuration"
echo "==========================================="
echo ""
echo "Add to ~/.config/claude/mcp.json:"
echo ""
echo "  {"
echo "    \"mcpServers\": {"
echo "      \"freestone\": {"
echo "        \"command\": \"${INSTALL_DIR}/${BINARY}\","
echo "        \"args\": [\"serve\"]"
echo "      }"
echo "    }"
echo "  }"
echo ""
echo "==========================================="
echo "  Keychain Setup (macOS)"
echo "==========================================="
echo ""
echo "iCloud (app-specific password from appleid.apple.com):"
echo ""
echo "  security add-generic-password -s \"freestone-icloud-calendar\" -a \"YOUR_APPLE_ID\" -w \"xxxx-xxxx-xxxx-xxxx\""
echo "  security add-generic-password -s \"freestone-icloud-mail\" -a \"YOUR_APPLE_ID\" -w \"xxxx-xxxx-xxxx-xxxx\""
echo ""
echo "Google (OAuth client from Google Cloud Console):"
echo ""
echo "  security add-generic-password -s \"freestone-google-client-id\" -a \"freestone\" -w \"YOUR_CLIENT_ID\""
echo "  security add-generic-password -s \"freestone-google-client-secret\" -a \"freestone\" -w \"YOUR_CLIENT_SECRET\""
echo "  ${INSTALL_DIR}/${BINARY} auth google"
echo ""
echo "Done. Run '${INSTALL_DIR}/${BINARY} serve' to start."
