#!/usr/bin/env bash
# ==============================================================================
# MDD VoWiFi Gateway - 离线一键导入与安装脚本 (Offline 1-Click Installer)
# ==============================================================================
set -euo pipefail

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
docker tag mdd-gateway-control:latest mdd-sim-gateway/control:latest 2>/dev/null || true

# 3. 初始化工作目录与权限
echo "[2/4] 初始化运行数据目录..."
mkdir -p "${SCRIPT_DIR}/data/instances"
mkdir -p "${SCRIPT_DIR}/data/certs"
chmod -R 755 "${SCRIPT_DIR}/control" "${SCRIPT_DIR}/engine" 2>/dev/null || true

# 4. 启动服务
echo "[3/4] 启动 MDD Gateway 服务..."
if docker compose version &>/dev/null; then
    COMPOSE_CMD="docker compose"
elif command -v docker-compose &>/dev/null; then
    COMPOSE_CMD="docker-compose"
else
    echo "[!] 未找到 docker-compose，尝试直接启动容器..."
    COMPOSE_CMD=""
fi

if [[ -n "${COMPOSE_CMD}" ]]; then
    cd "${SCRIPT_DIR}"
    ${COMPOSE_CMD} up -d --remove-orphans
else
    docker run -d --name mdd-sim-gateway-control \
        --restart unless-stopped \
        --network host \
        -v /var/run/docker.sock:/var/run/docker.sock \
        -v "${SCRIPT_DIR}/data:/data" \
        -v "${SCRIPT_DIR}/control:/app/control:ro" \
        -v "${SCRIPT_DIR}/engine:/app/engine:ro" \
        -v "${SCRIPT_DIR}/webui/dist:/app/webui/dist:ro" \
        mdd-sim-gateway-control:latest
fi

# 5. 获取本机 IP 并提示
HOST_IP=$(ip route get 1.1.1.1 2>/dev/null | awk '{print $7; exit}' || hostname -I 2>/dev/null | awk '{print $1}' || echo "127.0.0.1")

echo "======================================================"
echo "[4/4] MDD VoWiFi Gateway 安装并启动成功！"
echo "------------------------------------------------------"
echo " 管理面板访问地址："
echo "   - HTTP (内网推荐):   http://${HOST_IP}:8000/mdd"
echo "   - HTTPS (自签直连):  https://${HOST_IP}:8443/mdd"
echo ""
echo " 客户端 (Card Agent) 位于 agent/go-agent/ 目录："
echo "   - Windows:  agent/go-agent/mdd-card-agent-windows-amd64.exe"
echo "   - macOS:    agent/go-agent/mdd-card-agent-darwin-arm64 (或 darwin)"
echo "   - Linux:    agent/go-agent/mdd-card-agent-linux-amd64"
echo "======================================================"
