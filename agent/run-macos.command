#!/bin/bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
cd "$DIR"

if [[ -x "$DIR/mdd-agent" ]]; then
  AGENT=("$DIR/mdd-agent")
else
  AGENT=(python3 "$DIR/mdd_agent.py")
fi

echo "MDD Agent for macOS"
echo "同一登录用户只能运行一个 CLI 或菜单栏 GUI 宿主。"
read -r -p "Gateway host:port: " gateway
if [[ -z "${gateway//[[:space:]]/}" ]]; then
  echo "Gateway host:port is required." >&2
  exit 2
fi
"${AGENT[@]}" config set server "$gateway"

read -r -s -p "Agent Token（输入内容不会进入命令行）: " token
echo
printf '%s\n' "$token" | "${AGENT[@]}" config set token --stdin
unset token

echo "启动统一 Agent；关闭窗口会停止，后台验证可使用 nohup mdd-agent run。"
exec "${AGENT[@]}" run
