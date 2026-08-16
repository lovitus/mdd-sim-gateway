# MDD VoWiFi Gateway - 完整部署与维护手册

本项目是基于 3GPP 标准打造的企业级 **VoWiFi / VoLTE 智能卡网关系统**。支持将 USB 读卡器（或手机内置卡槽）的 SIM/eSIM APDU 鉴权能力转发至云端或本地服务端，借助运营商 VoWiFi 核心网实现全球免漫游蜂窝电话接打与短信收发。

---

## 目录
1. [系统架构与原理](#一系统架构与原理)
2. [服务端部署指南](#二服务端部署指南)
   - [方案 A：离线一键安装 (推荐)](#方案-a离线一键安装-推荐)
   - [方案 B：Docker Compose 容器化部署](#方案-bdocker-compose-容器化部署)
   - [方案 C：Nginx 反向代理配置 (/mdd 独立上下文)](#方案-cnginx-反向代理配置-mdd-独立上下文)
3. [客户端部署 (Go 独立单文件，免运行环境)](#三客户端部署-go-独立单文件免运行环境)
   - [Windows 客户端](#1-windows-客户端)
   - [macOS 客户端](#2-macos-客户端)
   - [Linux / 树莓派 / NAS 客户端](#3-linux--树莓派--nas-客户端)
4. [语音通话与软电话使用](#四语音通话与软电话使用)
5. [上游代码同步与维护 (Rebase / Cherry-Pick)](#五上游代码同步与维护-rebase--cherry-pick)

---

## 一、系统架构与原理

```text
+-------------------------------------------------------------------------------+
|                    客户端 (Windows / macOS / Linux)                          |
|  - 插卡设备: USB CCID 读卡器 / ESTKme / 9e 卡槽                               |
|  - 代理程序: mdd-card-agent (Go 单文件，拦截恶意 APDU，毫秒级转发鉴权)           |
+-------------------------------------------------------------------------------+
                                      │ TCP (默认端口 35963)
                                      ▼
+-------------------------------------------------------------------------------+
|                    服务端宿主机 (Linux VPS / 本地服务器)                       |
|                                                                               |
|  [控制平面容器] mdd-sim-gateway-control                                       |
|    - 监听: HTTP 8000 + HTTPS 8443 (支持 /mdd 上下文)                           |
|    - 模块: WebUI 管理面板 / LPA eSIM 下载 / 安全 SQLite 账本 / 软电话 WS 代理     |
|                                                                               |
|  [核心引擎容器] mdd-sim-gateway-engine-<id> (每张 SIM 卡独立容器与网络栈)        |
|    - StrongSwan: 与运营商 ePDG 建立 IPsec IKEv2 安全隧道 (ipsec0)             |
|    - Asterisk PBX: 运行 3GPP SIP/IMS 栈 (处理 VoWiFi 注册、语音转码与短信)      |
|    - Egress Proxy: 按卡绑定国家出口代理 (Clash/Sing-box/Xray 分流)            |
+-------------------------------------------------------------------------------+
                                      │ IPsec (ESP / UDP 4500)
                                      ▼
                       运营商 VoWiFi 核心网 (ePDG / IMS)
```

---

## 二、服务端部署指南

### 端口开放要求
* **Web 控制台**：`8000` (HTTP) 或 `8443` (HTTPS)
* **读卡器接入端口**：`35963` (TCP VPCD 桥接端口)
* **VoWiFi 媒体端口**：`10000-13000/udp` (Asterisk RTP 音频流)

---

### 方案 A：离线一键安装 (推荐)

无需联网下载大型镜像，开箱即用：

1. **下载或解压项目目录**：
   确保 `images/` 目录下存在 `mdd-control.tar.gz` 与 `mdd-engine.tar.gz`（可从 Release 页面下载）。
2. **一键执行离线安装**：
   ```bash
   cd /opt/mdd-gateway
   sudo ./offline-install.sh
   ```
3. **访问管理面板**：
   * HTTP: `http://<服务器IP>:8000/mdd`
   * HTTPS: `https://<服务器IP>:8443/mdd`

---

### 方案 B：Docker Compose 容器化部署

```bash
cd /opt/mdd-gateway
docker compose up -d
```

---

### 方案 C：Nginx 反向代理配置 (/mdd 独立上下文)

系统已原生内置 `/mdd` 上下文拦截中间件与 SPA 路由适配，在 Nginx 中只需配置单个 `location /mdd/` 块：

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    # MDD VoWiFi 网关反向代理
    location /mdd/ {
        proxy_pass http://127.0.0.1:8000/mdd/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Prefix /mdd;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
```

---

## 三、客户端部署 (Go 独立单文件，免运行环境)

客户端采用 Go 原生编写并预先交叉编译完成，无任何 Python 或 Node 依赖。二进制文件位于 `agent/go-agent/`（或从 GitHub Release 直接下载）。

### 1. Windows 客户端
```cmd
# 运行并连接到服务端
mdd-card-agent-windows-amd64.exe -gateway 10.44.0.14 -port 35963
```
*如需指定特定读卡器，添加参数 `-reader "ESTKme"` 或 `-reader "Gemalto"`。*

### 2. macOS 客户端 (Apple Silicon / Intel)
```bash
chmod +x mdd-card-agent-darwin-arm64
./mdd-card-agent-darwin-arm64 -gateway 10.44.0.14 -port 35963
```

### 3. Linux / 树莓派 / NAS 客户端
```bash
chmod +x mdd-card-agent-linux-amd64
./mdd-card-agent-linux-amd64 -gateway 10.44.0.14 -port 35963
```

---

## 四、语音通话与软电话使用

1. **网页端软电话**：
   * 在控制台侧边栏点击 **软电话 (Softphone)**。
   * 支持通过浏览器麦克风直接拨打或接听来电。
   * *注：浏览器 WebRTC 要求在 HTTPS 或已加白的 HTTP 环境下调用麦克风。*
2. **独立 SIP 客户端（MicroSIP / Linphone / Zoiper）**：
   * 软电话页面会展示当前 SIM 卡分配的 SIP 账号与密码。
   * 在电脑或手机 SIP 客户端中填入账号密码和服务器 IP，即可实现后台常驻接听，音质纯净稳定。

---

## 五、上游代码同步与维护 (Rebase / Cherry-Pick)

本项目的所有改动均遵循**原子化清晰 Commit 规范**，便于未来拉取上游（Upstream）更新并进行重放或 Cherry-Pick。

### 1. 关键功能提交索引 (Commit Map)
* `build: include compiled webui dist bundle for standalone deployment` — 前端 WebUI 独立分发产物
* `feat: complete dual auth, VoWiFi outbound fixes, offline package and cross-platform Go agents` — 双模鉴权、IMS 呼叫修复、离线安装包与 Go 跨平台客户端
* `fix: auto-derive smartcard reader IMEI and promote draft lines` — 读卡器 IMEI 智能推导与草稿线路自动激活
* `fix: allow present Virtual PCD readers in unified devices` — VPCD 虚拟智能卡通道设备识别
* `fix: precise lpac reader index resolution and rich profile display` — eSIM LPA 读卡器精准寻址与 Profile 状态展示
* `feat: embed sing-box, lpac binary and host module into container` — 内嵌分流代理与 LPA 核心依赖

### 2. 追平上游主干的标准流程 (Rebase / Cherry-Pick)
```bash
# 1. 添加上游原始仓库
git remote add upstream https://github.com/MddIdd/mdd-sim-gateway.git

# 2. 获取上游最新提交
git fetch upstream

# 3. 基于上游最新分支重放我们的增强提交 (Rebase)
git rebase upstream/main

# 4. 若遇到局部冲突，可使用 Cherry-Pick 单独拣选特定功能
git cherry-pick <commit_id>

# 5. 测试无误后推送到自己的私有仓库
git push origin main --force-with-lease
```
