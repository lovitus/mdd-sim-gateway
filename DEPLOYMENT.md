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

### 多网卡 / VPN 与 WebRTC 媒体入口

管理监听、SIM 的国家出口和浏览器 WebRTC 媒体入口是三套独立配置。MDD 不使用公网默认路由、
固定网段或 SIM 的出口国家推断媒体地址。宿主编排器发布当前已分配且可用的物理/VPN IPv4 清单；
浏览器通过哪个受管地址访问管理页面，就确认该会话使用哪个媒体入口。不同浏览器可同时确认不同
入口，互不修改全局 Engine 配置，也不会改变法国卡等线路已经选择的 GB ePDG 出口。

设备页会在首次部署、Control 重启、Engine 代际变化或宿主接口清单变化后提示确认和执行无资费
本地 Echo 测试。确认只接受当前宿主清单中的不透明候选 ID，不能提交任意 IP；真实 IMS 呼出还会
针对当前浏览器、WebSocket、Engine 代际和入口重新验证双向 RTP。测试失败只阻止该会话发起真实
VoWiFi 呼叫，不影响管理、设备、读卡器、短信、蜂窝数据或国家出口。

每个获准的真实 VoWiFi 呼叫还会在 Asterisk 内设置 10 秒绝对安全租约；Control 仅在所属 WSS
及 exact Asterisk 通话仍存活时续租。页面关闭、网络中断或 Control 异常退出后，本地租约到期会
独立挂断该通话；正常关闭则先按 exact uniqueid 主动挂断，不扫描或结束其他用户的通话。

当前轻量代理适用于浏览器直接访问网关已分配 IPv4、且 RTP UDP 端口按原端口发布的场景。浏览器
经另一台反向代理、NAT、域名入口、IPv6，或多个网络彼此不具备直连路由时，应配置浏览器和
Asterisk 共用的标准受管 TURN 服务。不要把 RTP 私有协议塞入 SIP WSS，也不要使用重复
`ice_host_candidates`、默认路由或固定地址伪装多宿主支持。

### 受限网络中的下载代理

生产主机需要统一下载出口时，应把“业务出口代理”和“主机下载代理”分开。主机下载代理只负责
Release、系统包、Git 依赖和 Docker 镜像；不得写入 SIM 线路或 VoWiFi 出口配置。

- `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY` 和小写同名变量用于登录 shell、安装脚本、curl、wget
  等下载工具；Git 和 APT 还应配置各自的持久代理。
- Docker daemon 的 `HTTP_PROXY`/`HTTPS_PROXY` 必须写入独立 systemd drop-in，再执行
  `systemctl daemon-reload && systemctl restart docker`。重启前必须确认无活动通话，并确认所有
  MDD 容器具有恢复策略。
- 如果上游只提供 SOCKS5，建议复用已发布的 sing-box 二进制，在本机回环地址提供一个独立 HTTP
  CONNECT 适配入口，Docker 指向该入口，适配器的唯一 outbound 指向 SOCKS5。不要为此重新编译
  网络组件，也不要让无认证入口监听公网或局域网地址。
- `NO_PROXY` 必须包含 localhost、管理网段和 Docker 内部网段，避免 Agent、Control、Engine 与
  SOCKS 上游自身形成代理环路。适配器不可用时下载应失败，不能自动回退直连。
- 验证至少包括：curl/wget/Git 连接到本地适配入口、适配器日志显示目标经 SOCKS outbound、一次
  小型 Docker 镜像拉取成功，以及服务重启后代理和容器都恢复。测试镜像和临时配置随即清理。

代理地址属于单机部署数据，不应硬编码进仓库；具体值保存在目标主机权限受控的环境文件、APT
配置、Docker drop-in 和适配器配置中。

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

## 三、仅 PC/SC 读卡器的轻量客户端部署 (Go 独立单文件，免运行环境)

客户端采用 Go 原生编写并预先交叉编译完成，无任何 Python 或 Node 依赖。二进制文件位于 `agent/go-agent/`（或从 GitHub Release 直接下载）。

### 1. Windows 客户端
```cmd
# 运行并连接到服务端
mdd-card-agent-windows-amd64.exe -gateway gateway.example.com -port 35963
```
*如需指定特定读卡器，添加参数 `-reader "ESTKme"` 或 `-reader "Gemalto"`。*

如果 Windows 主机连接了 4G/5G Modem，请不要运行本节的独立 Card Agent。应安装
[`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md) 中的统一 `MddAgent` SCM 服务；它会同时
管理 Modem 与本机全部 PC/SC/eSIM 读卡器，并通过同一个 CLI/GUI 控制面报告状态。

### 2. macOS 客户端 (Apple Silicon / Intel)

只有 PC/SC/eSIM 读卡器时仍可使用轻量 Card Agent：

```bash
chmod +x mdd-card-agent-darwin-arm64
./mdd-card-agent-darwin-arm64 -gateway gateway.example.com -port 35963
```

连接 4G/5G Modem 时使用统一 `MDD Agent.app` 或 `mdd-agent`，不要再启动独立 Card Agent。
首版不安装 launchd；菜单栏 GUI 或 CLI 只能运行一个，重复启动固定退出 `9`。GUI 首次运行会把
Token 保存到当前用户的 `~/Library/Application Support/MDD Agent/config.json`；目录权限为
`0700`、配置文件为 `0600`。关闭状态窗口只隐藏到菜单栏，选择“退出 MDD Agent”才释放硬件。

CLI 会优先使用配置文件中的 Token；文件未配置时，依次使用 `--token`/`--token-stdin` 和
`MDD_AGENT_TOKEN` 作为当前进程的临时回退。`config set token --stdin` 与 GUI 的 Token 窗口写入
同一配置文件，不存在两套状态。GUI 与本地 CLI 每次启动都会检查麦克风授权；首次未决定时调用
macOS 标准授权框，授权后只刷新语音音频能力。纯 SSH 没有登录中的桌面会话时，macOS 可能不显示
授权框，CLI 会报告限制但不阻塞 Modem、短信、数据或读卡器：

```bash
./mdd-agent config set server gateway.example.com:8443
printf '%s\n' "$MDD_AGENT_TOKEN" | ./mdd-agent config set token --stdin
nohup ./mdd-agent run >mdd-agent.out 2>&1 &
./mdd-agent status --json
```

发布包已内置固定版本的私有 raw-USB/lwIP companion 与通话音频 helper，不要求 Homebrew、Gammu、
Python 或系统 PPP。蜂窝 IP 不进入 macOS 网络栈；当前实机发布门禁仅完成 macOS 15.2 arm64 +
Quectel EC20F，Intel 与其他 Modem 协议必须在对应发布矩阵通过后才能标记支持。

### 3. Linux / 树莓派 / NAS 客户端
```bash
chmod +x mdd-card-agent-linux-amd64
./mdd-card-agent-linux-amd64 -gateway gateway.example.com -port 35963
```

---

## 四、语音通话与软电话使用

1. **网页端软电话**：
   * 在控制台侧边栏点击 **软电话 (Softphone)**。
   * 支持通过浏览器麦克风直接拨打或接听来电。
   * *注：浏览器 WebRTC 要求在 HTTPS 或已加白的 HTTP 环境下调用麦克风。*
2. **独立 SIP 客户端（MicroSIP / Linphone / Zoiper）**：
   * 当前不支持。Engine 的 WSS 不向局域网公开，真实 IMS 呼出只接受 Control 代理下、
     已完成当前浏览器双向媒体自检的单次 SIP dialog。
   * 不要通过重新暴露 8089 绕过该门禁。未来如需独立 SIP 客户端，应实现独立的受控
     admission 和断线挂断生命周期，而不是复用内置浏览器凭据。

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
