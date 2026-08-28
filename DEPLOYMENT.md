# MDD VoWiFi Gateway - 完整部署与维护手册

本项目通过 SIM/eSIM 读卡器或受支持的蜂窝模块提供通话与短信管理。VoWiFi 使用运营商 ePDG/IMS；蜂窝通话使用远端 Agent。费用按运营商套餐及漫游规则执行，不保证免费。

---

## 目录
1. [系统架构与原理](#一系统架构与原理)
2. [服务端部署指南](#二服务端部署指南)
   - [方案 A：离线一键安装 (推荐)](#方案-a离线一键安装-推荐)
   - [方案 B：Docker Compose 容器化部署](#方案-bdocker-compose-容器化部署)
   - [方案 C：Nginx 反向代理配置 (/mdd 独立上下文)](#方案-cnginx-反向代理配置-mdd-独立上下文)
3. [客户端部署与支持边界](#三客户端部署与支持边界)
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
|  - 统一 Agent: Windows / macOS；Linux 统一 Agent 尚未交付                    |
+-------------------------------------------------------------------------------+
                                      │ WS/WSS（旧轻量 PC/SC 客户端另有 TCP 路径）
                                      ▼
+-------------------------------------------------------------------------------+
|                    服务端宿主机 (Linux VPS / 本地服务器)                       |
|                                                                               |
|  [控制平面容器] mdd-sim-gateway-control                                       |
|    - 默认监听: HTTPS/WSS 8443（支持 /mdd；HTTP 入口需按实际部署配置）           |
|    - 浏览器同源 PCM WS/WSS；蜂窝音频直接连接 Agent，不依赖 Asterisk           |
|                                                                               |
|  [核心引擎容器] mdd-sim-gateway-engine-<id> (每张 SIM 卡独立容器与网络栈)        |
|    - Python SWu: 与运营商 ePDG 建立 IKEv2/IPsec，用户态处理 ESP               |
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

* **Web 控制台及统一 Agent**：默认 `8443`（HTTPS/WSS）；外部反向代理可使用自己的入口端口。
* **旧轻量 PC/SC 客户端**：仅实际使用该兼容路径时开放 TCP VPCD 端口，限定在受信网络。
* **浏览器音频**：不需要另外开放 RTP UDP 或 Asterisk SIP/WebRTC 端口。

### 多网卡 / VPN 与同源浏览器媒体

浏览器音频只使用当前页面同源的 WS/WSS，不要求用户确认服务器网卡/IP，也不需要浏览器与
Asterisk 直连 RTP 或配置 TURN。VPN、域名、IPv6、反向代理和 localhost 转发仅决定如何访问管理
入口；SIM 的国家出口仍按该线路设置，不由浏览器访问地址推断。

VoWiFi 路径为浏览器 → Control → Asterisk 原生媒体 WebSocket；蜂窝路径为浏览器 → Control →
Agent PCM，不需要 Asterisk 作媒体锚点。每通电话有独立 owner，准备阶段不发送付费拨号/接听；
只有当前双向音频、实际采集/播放计数及新鲜挑战证据通过后才提交。重复请求不重放付费动作，
同线路跨端或跨通话模式争用会被拒绝；未确认终态前保留占用。

页面关闭、连接中断或媒体证据失效会进入精确终止流程；VoWiFi 和蜂窝分别保留 Asterisk/Agent
本地有界租约作为兜底。界面区分“结束中”和“终止未确认”，不能将 HTTP 成功等同于物理挂断。
麦克风仍受浏览器安全上下文约束：通常使用 HTTPS，localhost HTTP 是浏览器支持的例外；这与
已经删除的媒体 IP 确认不是同一件事。升级后请刷新旧页面，旧 SIP/媒体确认接口已退役。

构建/交付记录应同时保存源码归档 SHA、镜像 configuration digest 与生产端 manifest digest。
不同 Docker 存储后端的 image ID 可能不同；导出时给每个组件命名，并核对 OCI 索引包含每个
镜像，不能只比较跨后端 ID 或因此盲目重建。[Docker 后端说明](https://docs.docker.com/engine/storage/containerd/)

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
   * HTTPS: `https://<服务器IP>:8443/mdd`

---

### 方案 B：Docker Compose 容器化部署

```bash
cd /opt/mdd-gateway
sudo install -d -m 0700 /etc/mdd-sim-gateway
sudo install -m 0600 deploy/mdd-runtime.env.example /etc/mdd-sim-gateway/runtime.env
# 按实际宿主修改四个绝对路径；它们必须互不相同且位于源码树外。
sudo ./deploy/mdd-compose.sh validate
sudo ./deploy/mdd-compose.sh up-control
```

持久目录分为四类：`MDD_CONFIG_ROOT` 保存设置、管理员凭据和 TLS；`MDD_STATE_ROOT`
保存 SQLite、审计、实例日志和可恢复事务；`MDD_ARTIFACT_ROOT` 保存 lpac、Agent 发布包、
部署/迁移记录；`MDD_RUNTIME_ROOT` 只保存重启后可重建的 socket。任何一个都不能位于源码目录，
也不能共享同一路径。Control 更新只替换 Control，不会把动态 Engine 纳入 Compose。

旧版本只有一个 `data/` 时，先停止服务并执行只读盘点，再迁移；迁移不会覆盖目标或删除源目录：

```bash
sudo ./deploy/mdd-compose.sh plan-migration /旧的绝对路径/data
sudo ./deploy/mdd-compose.sh migrate-legacy /旧的绝对路径/data
```

确认新 Control、数据库和线路证据正常后，再由管理员单独归档旧目录。不要用 rsync 将源码全目录
覆盖到配置/状态/产物根，也不要把这三个根同步回 Git。

---

### 方案 C：Nginx 反向代理配置 (/mdd 独立上下文)

系统已内置 `/mdd` 上下文与 SPA 路由。以下示例连接默认 HTTPS 上游；信任文件和
`proxy_ssl_name` 必须换成实际网关证书的信任锚和 SAN 名称。保留原始 Host（含端口），否则会破坏
浏览器同源校验。若另行配置了 HTTP 上游，按其真实端口调整，不要假定默认还有 8000 端口。

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    # MDD VoWiFi 网关反向代理
    location /mdd/ {
        proxy_pass https://127.0.0.1:8443/mdd/;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /path/to/gateway-trust.pem;
        proxy_ssl_server_name on;
        proxy_ssl_name gateway.internal; # 替换为上游证书中的 SAN 名称
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $http_host;
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

## 三、客户端部署与支持边界

Windows 与 macOS 标准部署使用统一 Agent 包，具体命令见
[`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md)。下列 Go 单文件命令仅属于旧轻量 PC/SC 兼容客户端，
不代表统一 Agent 的 GUI、健康上报、蜂窝语音或管理功能已经覆盖 Linux。

### 1. Windows 客户端
```cmd
# 运行并连接到服务端
mdd-card-agent-windows-amd64.exe -gateway gateway.example.com -port 35963
```
*如需指定特定读卡器，添加参数 `-reader "ESTKme"` 或 `-reader "Gemalto"`。*

如果 Windows 主机连接了 4G/5G Modem，请不要运行本节的独立 Card Agent。应安装
[`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md) 中的统一 `MddAgent` SCM 服务；它会同时
管理 Modem 与本机全部 PC/SC/eSIM 读卡器，并通过同一个 CLI/GUI 控制面报告状态。

### 2. macOS 客户端

使用统一 `MDD Agent.app` 或 `mdd-agent`，当前部署默认 `modem_enabled=false`，仅管理多 PC/SC/eUICC
读卡器；不会枚举或接管 Modem，也不会索取其麦克风权限。Modem 代码保留，但其语音和私有数据面
尚未作为当前版本交付，不应自行开启或宣称 Intel/其他 Modem 已通过实机矩阵。
首版不安装 launchd；菜单栏 GUI 或 CLI 只能运行一个，重复启动固定退出 `9`。GUI 首次运行会把
Token 保存到当前用户的 `~/Library/Application Support/MDD Agent/config.json`；目录权限为
`0700`、配置文件为 `0600`。关闭状态窗口只隐藏到菜单栏，选择“退出 MDD Agent”才释放硬件。

CLI 会优先使用配置文件中的 Token；文件未配置时，依次使用 `--token`/`--token-stdin` 和
`MDD_AGENT_TOKEN` 作为当前进程的临时回退。`config set token --stdin` 与 GUI 的 Token 窗口写入
同一配置文件，不存在两套状态。只有后续明确启用 Modem 的实验模式才检查音频权限；纯 SSH
没有桌面会话时仍受系统授权限制，不能将其当作 PC/SC-only 客户端的启动要求：

```bash
./mdd-agent config set server gateway.example.com:8443
printf '%s\n' "$MDD_AGENT_TOKEN" | ./mdd-agent config set token --stdin
nohup ./mdd-agent run >mdd-agent.out 2>&1 &
./mdd-agent status --json
```

发布包携带所需运行时，不要求客户安装 Python 或 Homebrew。包含实验 helper 不等于完成
Mac 蜂窝隔离/通话验收；当前支持边界以 PC/SC-only 和正式发布矩阵为准。

### 3. Linux / 树莓派 / NAS 客户端

统一 Linux Agent 尚未实现；以下仅为已有的轻量 PC/SC 兼容路径：

```bash
chmod +x mdd-card-agent-linux-amd64
./mdd-card-agent-linux-amd64 -gateway gateway.example.com -port 35963
```

---

## 四、语音通话与软电话使用

1. **网页端软电话**：
   * 在控制台侧边栏点击 **软电话 (Softphone)**。
   * 支持通过浏览器麦克风直接拨打或接听来电。
   * 麦克风需要 HTTPS 或浏览器认可的 localhost 安全上下文；媒体使用同源 WS/WSS。
2. **独立 SIP 客户端（MicroSIP / Linphone / Zoiper）**：
   * 当前不支持。旧 SIP WebSocket 和媒体确认 API 已关闭；即使持有缓存 SIP 凭据，Engine 的
     旧 WebRTC 端点也只能进入拒绝上下文，不能绕过 native owner 发起通话或短信。
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
