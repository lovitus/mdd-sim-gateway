#!/usr/bin/env bash
# ==============================================================================
# MDD VoWiFi Gateway - 离线一键导入与安装脚本 (Offline 1-Click Installer)
# ==============================================================================
set -euo pipefail

if [[ "$(id -u)" -ne 0 ]]; then
    echo "[!] 离线安装需要 root 权限。" >&2
    exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGES_DIR="${SCRIPT_DIR}/images"

echo "======================================================"
echo "    MDD VoWiFi Gateway 离线一键安装程序"
echo "======================================================"

# 1. 检查 Docker 环境
if ! command -v docker &>/dev/null; then
    echo "[!] 未检测到 Docker，请先安装 Docker 环境。"
    exit 1
fi

# 2. 离线导入 Docker 镜像
echo "[1/4] 检查并导入离线 Docker 镜像..."
ARCH=$(uname -m)

if [[ ("$ARCH" == "aarch64" || "$ARCH" == "arm64") && -f "${IMAGES_DIR}/mdd-control-arm64.tar.gz" ]]; then
    echo "  -> 检测到 ARM64 架构，正在导入控制面镜像 (mdd-control-arm64.tar.gz)..."
    docker load -i "${IMAGES_DIR}/mdd-control-arm64.tar.gz"
elif [[ -f "${IMAGES_DIR}/mdd-control.tar.gz" ]]; then
    echo "  -> 正在导入控制面镜像 (mdd-control.tar.gz)..."
    docker load -i "${IMAGES_DIR}/mdd-control.tar.gz"
elif [[ -f "${IMAGES_DIR}/mdd-control.tar" ]]; then
    docker load -i "${IMAGES_DIR}/mdd-control.tar"
else
    echo "  [i] 未找到 images/mdd-control.tar.gz，跳过离线控制面镜像导入。"
fi

if [[ ("$ARCH" == "aarch64" || "$ARCH" == "arm64") && -f "${IMAGES_DIR}/mdd-engine-arm64.tar.gz" ]]; then
    echo "  -> 检测到 ARM64 架构，正在导入引擎镜像 (mdd-engine-arm64.tar.gz)..."
    docker load -i "${IMAGES_DIR}/mdd-engine-arm64.tar.gz"
elif [[ -f "${IMAGES_DIR}/mdd-engine.tar.gz" ]]; then
    echo "  -> 正在导入引擎镜像 (mdd-engine.tar.gz)..."
    docker load -i "${IMAGES_DIR}/mdd-engine.tar.gz"
elif [[ -f "${IMAGES_DIR}/mdd-engine.tar" ]]; then
    docker load -i "${IMAGES_DIR}/mdd-engine.tar"
else
    echo "  [i] 未找到 images/mdd-engine.tar.gz，跳过离线引擎镜像导入。"
fi

# 确保镜像 Tag 别名就绪
docker tag mdd-control-arm64:latest mdd-sim-gateway-control:latest 2>/dev/null || true
docker tag mdd-gateway-control:latest mdd-sim-gateway-control:latest 2>/dev/null || true
docker tag mdd-control-arm64:latest mdd-sim-gateway/control:latest 2>/dev/null || true
docker tag mdd-sim-gateway-control:latest mdd-sim-gateway/control:latest 2>/dev/null || true
docker tag mdd-gateway-control:latest mdd-sim-gateway/control:latest 2>/dev/null || true
docker tag mdd-engine-arm64:latest mdd-sim-gateway/engine:latest 2>/dev/null || true
docker tag mdd-gateway-engine:latest mdd-sim-gateway/engine:latest 2>/dev/null || true

# 3. 初始化彼此隔离、且位于源码树外的持久目录
MDD_CONFIG_ROOT="${MDD_CONFIG_ROOT:-/etc/mdd-sim-gateway}"
MDD_STATE_ROOT="${MDD_STATE_ROOT:-/var/lib/mdd-sim-gateway}"
MDD_ARTIFACT_ROOT="${MDD_ARTIFACT_ROOT:-/var/lib/mdd-sim-gateway-artifacts}"
MDD_RUNTIME_ROOT="${MDD_RUNTIME_ROOT:-/run/mdd-sim-gateway}"
for root in "$MDD_CONFIG_ROOT" "$MDD_STATE_ROOT" "$MDD_ARTIFACT_ROOT" "$MDD_RUNTIME_ROOT"; do
    [[ "$root" = /* ]] || { echo "[!] 所有运行目录都必须是绝对路径。" >&2; exit 2; }
    [[ "$root/" != "$SCRIPT_DIR/"* ]] || { echo "[!] 运行目录不能位于源码目录。" >&2; exit 2; }
done
[[ "$MDD_CONFIG_ROOT" != "$MDD_STATE_ROOT" &&
   "$MDD_CONFIG_ROOT" != "$MDD_ARTIFACT_ROOT" &&
   "$MDD_CONFIG_ROOT" != "$MDD_RUNTIME_ROOT" &&
   "$MDD_STATE_ROOT" != "$MDD_ARTIFACT_ROOT" &&
   "$MDD_STATE_ROOT" != "$MDD_RUNTIME_ROOT" &&
   "$MDD_ARTIFACT_ROOT" != "$MDD_RUNTIME_ROOT" ]] || {
    echo "[!] 配置、状态、产物和运行时目录必须互不相同。" >&2
    exit 2
}
echo "[2/4] 初始化隔离的配置、状态、产物和运行时目录..."
install -d -m 0700 "$MDD_CONFIG_ROOT" "$MDD_STATE_ROOT" \
    "$MDD_ARTIFACT_ROOT" "$MDD_RUNTIME_ROOT"

# 4. 启动服务
echo "[3/4] 启动 MDD Gateway 服务..."
docker compose version &>/dev/null || {
    echo "[!] 离线部署要求 Docker Compose v2；禁止退回手写 docker run。" >&2
    exit 2
}
MDD_PORT_VALUE="${MDD_PORT:-8443}"
MDD_BIND_VALUE="${MDD_BIND:-0.0.0.0}"
[[ "$MDD_PORT_VALUE" =~ ^[0-9]+$ && "$MDD_PORT_VALUE" -ge 1 &&
   "$MDD_PORT_VALUE" -le 65535 ]] || { echo "[!] MDD_PORT 无效。" >&2; exit 2; }
for value in "$MDD_CONFIG_ROOT" "$MDD_STATE_ROOT" "$MDD_ARTIFACT_ROOT" \
             "$MDD_RUNTIME_ROOT" "$MDD_PORT_VALUE" "$MDD_BIND_VALUE"; do
    [[ "$value" =~ ^[A-Za-z0-9_./:@,+-]+$ ]] || {
        echo "[!] Compose 运行配置包含不支持的字符。" >&2; exit 2;
    }
done
RUNTIME_ENV="$MDD_CONFIG_ROOT/runtime.env"
RUNTIME_ENV_TMP="$RUNTIME_ENV.tmp.$$"
umask 077
{
    printf 'MDD_CONFIG_ROOT=%s\n' "$MDD_CONFIG_ROOT"
    printf 'MDD_STATE_ROOT=%s\n' "$MDD_STATE_ROOT"
    printf 'MDD_ARTIFACT_ROOT=%s\n' "$MDD_ARTIFACT_ROOT"
    printf 'MDD_RUNTIME_ROOT=%s\n' "$MDD_RUNTIME_ROOT"
    printf 'MDD_PORT=%s\n' "$MDD_PORT_VALUE"
    printf 'MDD_BIND=%s\n' "$MDD_BIND_VALUE"
    printf 'MDD_CONTROL_IMAGE=mdd-sim-gateway/control:latest\n'
    printf 'MDD_ENGINE_IMAGE=mdd-sim-gateway/engine:latest\n'
    printf 'MDD_MANAGER_URL=https://host.docker.internal:%s\n' "$MDD_PORT_VALUE"
} > "$RUNTIME_ENV_TMP"
chmod 0600 "$RUNTIME_ENV_TMP"
mv -f "$RUNTIME_ENV_TMP" "$RUNTIME_ENV"
MDD_ENV_FILE="$RUNTIME_ENV" sh "$SCRIPT_DIR/deploy/mdd-compose.sh" up-control-image

# 5. Display an address only when the host has one unambiguous non-bridge global IPv4.
# Management still listens on the configured bind address when this is empty; the installer
# must not guess a physical/VPN route and present it as the browser/media ingress.
mapfile -t HOST_IP_CANDIDATES < <(
    ip -o -4 addr show scope global 2>/dev/null |
        awk '{ split($2, i, "@"); split($4, a, "/"); print i[1] "|" a[1] }' |
        while IFS='|' read -r interface address; do
            [[ -n "${address}" && ! -d "/sys/class/net/${interface}/bridge" ]] && printf '%s\n' "${address}"
        done | sort -u
)
HOST_IP=""
if [[ ${#HOST_IP_CANDIDATES[@]} -eq 1 ]]; then
    HOST_IP="${HOST_IP_CANDIDATES[0]}"
fi
DISPLAY_HOST="${HOST_IP:-<host-ip>}"

echo "======================================================"
echo "[4/4] MDD VoWiFi Gateway 安装并启动成功！"
echo "------------------------------------------------------"
echo " 管理面板访问地址："
echo "   - HTTPS (自签直连):  https://${DISPLAY_HOST}:8443/mdd"
echo ""
echo " 客户端 (Card Agent) 位于 agent/go-agent/ 目录："
echo "   - Windows:  agent/go-agent/mdd-card-agent-windows-amd64.exe"
echo "   - macOS:    agent/go-agent/mdd-card-agent-darwin-arm64 (或 darwin)"
echo "   - Linux:    agent/go-agent/mdd-card-agent-linux-amd64"
echo "======================================================"
