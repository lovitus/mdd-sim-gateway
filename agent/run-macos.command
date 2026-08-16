#!/bin/bash
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
cd "$DIR"

echo "========================================================"
echo "  MDD Card Agent - Smartcard Forwarding for macOS"
echo "========================================================"
echo ""

read -p "Enter Gateway IP/Domain [127.0.0.1]: " GATEWAY
GATEWAY=${GATEWAY:-127.0.0.1}

echo ""
echo "Starting Card Agent -> $GATEWAY:35963..."
echo ""

python3 card_agent.py --gateway "$GATEWAY" --port 35963
