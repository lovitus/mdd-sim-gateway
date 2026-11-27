# MDD VoWiFi Gateway - 完整部署与维护手册

## 当前范围与发布基线

- **当前正式版本**：Go `v2.2.0` 与同源 `android-preview-v2.2.0`。
- **关键范围决定**：
  - eSIM 删除固定采用 **标准物理删除 + 删除通知留存 + 多重确认手动重放**，不恢复软删除方案。
  - Windows、macOS、Linux 远程统一 Agent 为正式产品目标；三大平台启用 4G 数据时强制独占隔离且 fail-closed，绝不回落宿主机默认网络或覆盖用户保存的开关意图。
  - macOS 发布保留原 Developer ID（Team `8WPJLUNLY8`）签名，本轮不做公证；Android 原生预览里程碑按 [ANDROID_PREVIEW_CLOSE](docs/decisions/2026-09-21-android-agent.md) 边界维护。
  - 完整范围决议见 [当前范围](docs/decisions/2026-09-20-current-scope.md)，当前需求与硬件验收状态见 [验收台账](docs/status/README.md)，2026 年 9–10 月各批次详细部署回执与哈希归档于 [deployment-rollouts-2026-09-to-10.md](docs/archive/2026-11-27/deployment-rollouts-2026-09-to-10.md)。
- **费用说明**：VoWiFi 连接运营商 ePDG/IMS，蜂窝通话/短信经远程 Agent 物理模块执行，资费均按运营商套餐与漫游规则计费。

## 目录
1. [系统架构与原理](#一系统架构与原理)
2. [服务端部署指南](#二服务端部署指南)
   - [方案 A：不可变 Go artifact 安装（推荐）](#方案-a不可变-go-artifact-安装推荐)
   - [方案 B：离线安装同一 Go artifact](#方案-b离线安装同一-go-artifact)
   - [方案 C：Nginx 反向代理默认 Go 入口](#方案-cnginx-反向代理默认-go-入口)
3. [客户端部署与支持边界](#三客户端部署与支持边界)
   - [Windows 客户端](#1-windows-客户端)
   - [macOS 客户端](#2-macos-客户端)
   - [Linux / 树莓派 / NAS 客户端](#3-linux--树莓派--nas-客户端)
   - [Agent 本地 SIM PIN](#4-agent-本地-sim-pin)
4. [语音通话与软电话使用](#四语音通话与软电话使用)
5. [线路诊断与支持包](#五线路诊断与支持包)
6. [版本升级、回滚与历史版本清理](#六版本升级回滚与历史版本清理)

---

## 一、系统架构与原理

```text
+-------------------------------------------------------------------------------+
|               客户端 (Windows / macOS / Linux / Android Preview)              |
|  - 插卡设备: USB CCID 读卡器 / ESTKme / 9e 卡槽 / 受支持的蜂窝模块             |
|  - 统一 Agent: mdd-agent；能力按平台与硬件资格门禁开放                        |
+-------------------------------------------------------------------------------+
                                      │ 统一认证 HTTPS/WSS
                                      ▼
+-------------------------------------------------------------------------------+
|                    服务端宿主机 (Linux systemd 服务器)                         |
|                                                                               |
|  mdd-core                 配置、鉴权、WebUI、状态事实、Agent 与浏览器 WSS      |
|  mdd-provider-apply       受控的线路 Provider 配置应用与排空恢复               |
|  mdd-vowifi@<line>        每线路独立的 Go VoWiFi Provider (SWu + netstack)    |
|  mdd-egress               共享国家出口与持久化蜂窝回环 SOCKS5 中继            |
|  mdd-updater              受控系统升级校验、原子切换与状态回报                 |
|                                                                               |
|  默认公开入口: HTTPS/WSS 8443；无需暴露额外 RTP/SIP 端口                       |
+-------------------------------------------------------------------------------+
                                      │ IPsec (ESP / UDP 4500)
                                      ▼
                       运营商 VoWiFi 核心网 (ePDG / IMS)
```

---

## 二、服务端部署指南

### 端口开放要求

- **Web 控制台、浏览器音频与统一 Agent**：默认复用单一 `8443/tcp`（HTTPS/WSS），外部反向代理可映射为 `443/tcp`。
- **VoWiFi 出站**：服务端（或配置的上游国家出口）需允许访问目标运营商 ePDG 的 `UDP 500/4500`。无需在服务端入站开放 RTP 或 SIP 端口。

### 多网卡 / VPN 与同源浏览器媒体

浏览器音频固定走当前页面同源的 WS/WSS（浏览器 → Core 同源 WSS relay → Go Provider 原生 SIP/RTP bridge，或蜂窝路径 → Agent PCM）。无需配置公网媒体 IP 或 TURN；SIM 的国家出口完全由该线路绑定的出口策略决定。每通电话由唯一会话 owner 仲裁，提交前校验双向媒体流与新鲜挑战，断线或关闭页面自动进入受保护挂断流程。

### 受限网络中的下载代理

生产主机需通过代理拉取 GitHub Release 或系统更新时，必须将“主机下载代理”与“SIM 业务国家出口”严格分离：

- 主机下载代理仅配置于受控环境文件、APT 或 `mdd-updater` 下载链路，`NO_PROXY` 必须包含 `localhost`、`127.0.0.1`、`::1` 及内网管理网段。
- 若上游仅提供 SOCKS5，可在回环地址运行独立的本地 HTTP CONNECT 转换入口，切勿监听公网或局域网接口，且代理失效时必须 fail-closed，不静默回退直连。

---

### 方案 A：不可变 Go artifact 安装（推荐）

目标主机需提供 Linux `systemd`、`realpath`、`curl`、`openssl`、coreutils `timeout` 及标准 `/usr/sbin/useradd`、`/usr/sbin/groupadd`、`/usr/sbin/nologin`：

```bash
mkdir mdd-release && tar -xf mdd-2.2.0-linux-amd64.tar -C mdd-release
cd mdd-release
# 将 <revision> 替换为解压目录内的实际 mdd-<revision> 目录名：
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
```

`install-release.sh` 六个子命令职责明确：

- `install RELEASE_DIR`：校验完整 manifest 与文件权限/哈希，原子安装并切换 `/usr/lib/mdd/current`；仅在全新主机初始化 `/etc/mdd` 认证、P-256 自签 TLS 证书与默认配置，已有配置与运行中进程保持不变。
- `start`：启用并启动 `mdd-core`、`mdd-provider-apply`、`mdd-egress`（均为 `Type=exec`），已启用的 `mdd-vowifi@` 实例在 Core 就绪后恢复；不自动启用宿主机 endpoint `mdd-agent`。
- `restart`：显式维护动作，重启上述三个核心服务。
- `stop`：按序停止 apply helper、托管 Provider、Core 与 egress，不禁用开机自启，不触碰独立 `mdd-agent`。
- `uninstall`：确认所有托管服务已停止后移除程序与 systemd unit，保留 `/etc/mdd` 与 `/var/lib/mdd*` 数据以便重装恢复。
- `status`：检查核心服务状态，并使用 `/etc/mdd/tls/server.crt` 完成 CA 与 SPKI pin 双向校验后探测 `/healthz`（可通过 `MDD_STATUS_PORT` 指定非默认端口）。

> **注意区分两种证书指纹**：`install-release.sh status` 核对服务端公钥 SPKI pin；远程 `mdd-agent` 配置项 `tls_sha256` 固定的是服务端叶证书完整 DER 的 SHA-256（与 WebUI「安全 → 添加 Agent」导出的指纹一致）。

### 方案 B：离线安装同一 Go artifact

在线与离线共享同一套安装包与安装器。将校验无误的 `.tar` 复制到目标机后执行：

```bash
sudo ./offline-install.sh install /absolute/path/mdd-2.2.0-linux-amd64.tar
sudo ./offline-install.sh start
sudo ./offline-install.sh status
```

### 方案 C：Nginx 反向代理默认 Go 入口

Go Core 使用根路径（`/`、`/assets`、`/api`、`/v1`），建议分配独立域名整站反代并保留原始 `Host` 与长连接升级头：

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location / {
        proxy_pass https://127.0.0.1:8443;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /path/to/gateway-trust.pem;
        proxy_ssl_server_name on;
        proxy_ssl_name gateway.internal; # 替换为上游证书 SAN 中的主机名
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
```

---

## 三、客户端部署与支持边界

所有桌面与服务器客户端统一使用对应平台的 `mdd-agent` 发布包，详细命令见 [`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md)。Agent 通过 `agent-host-health-v1` 在现有 WSS 控制链路上报平台、架构、精确版本、宿主模式与磁盘余量。

### 1. Windows 客户端
- 通过管理员权限运行 `install-windows-agent.ps1` 或 `mdd-agent service-install` 安装单一 `MDDAgent`（或既有 `MddAgent`）Windows 服务，统一管理本机允许的蜂窝模块与全部 PC/SC 读卡器。
- 无读卡器接入时 Windows 按需停止 `SCardSvr` 属于系统正常节能行为，`mdd-agent` 会在确认 `SCardSvr` 未被禁用且当前零读卡器 PnP 设备时正常上报空读卡器就绪，并在插入读卡器后自动建立 PC/SC 会话。
- 如需允许指定普通桌面用户在不持有管理员或 SCM 启停权限的前提下查看服务状态并修改 Core 连接参数，可由管理员显式执行：
  ```powershell
  mdd-agent.exe service-access -operator-sid <SID> -confirm-connection-authority
  ```
  保存后的新连接参数经现有 Core 受控维护重启生效；执行 `mdd-agent.exe service-access -revoke` 可随时即时撤销该授权且不中断硬件服务。

### 2. macOS 客户端
- 提供 `MDD-Agent-macOS-arm64.tar.gz` 与 `MDD-Agent-macOS-universal.tar.gz`（均要求 macOS 15+，原 Developer ID 签名，未公证），支持当前用户 LaunchAgent（GUI / CLI 模式）安装。
- 默认 `modem_enabled=false`（PC/SC-only），管理多读卡器与 eUICC；配置文件位于 `~/Library/Application Support/MDD Agent/config.json`（目录 `0700`、文件 `0600`）。

### 3. Linux / 树莓派 / NAS 客户端
- **systemd 托管安装**：配置 `/var/lib/mdd-agent/config.json`（`0600`）后启用 `mdd-agent.service`；ARM64 独立包自带 `install-linux-agent.sh` 支持完整预检与受隔离托管首装。
- **嵌入式无 systemd 手动部署**：仅需读卡器能力时可按 [`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md#嵌入式设备手动部署) 手动放置二进制并配合 crontab 保活。
- **蜂窝数据与隔离**：Linux 原生支持 ModemManager / AT 适配、受隔离静态 IPv4 与 Ethernet ModemManager DHCPv4 租约，以及同一模块上的语音/短信与数据借用共存（PPP/IPv6 保持 fail-closed）；隔离依赖 `nftables`、设备组、cgroup 与策略路由/`mdd-cellular-guard.service`。

### 4. Agent 本地 SIM PIN
- WebUI 在卡片剩余尝试次数 `> 2` 时开放一次性验证或“验证并保存到当前 Agent”。PIN 仅保存在对应 Agent 本机 `0600` 配置中，Core 绝不落盘存储 PIN；任何 `unknown` 结果禁止自动重试。

---

## 四、语音通话与软电话使用

1. **网页端软电话**：在控制台点击 **软电话 (Softphone)**，在 HTTPS 或 `localhost` 安全上下文下使用浏览器麦克风直接拨打/接听；支持每次显式授权的本地双声道录音。
2. **Android 原生预览客户端**：安装 `android-preview-v2.2.0` APK，登录或导入配置后即可使用服务端线路收发短信与通话，也可按需共享手机 OTG 连接的 USB CCID 读卡器。
3. **第三方独立 SIP 客户端**：当前不支持；旧版 Asterisk WebRTC / 8089 端点已永久移除。

---

## 五、线路诊断与支持包

- **线路诊断**：选择已保存线路后手动刷新，最多展示最近 500 条类型化状态事件，不暴露系统日志或原始 SIP/AT/APDU 载荷。
- **脱敏支持包**：支持一键导出有界脱敏 JSON 摘要，自动剥离 ICCID、电话号码、设备序列号、凭据、IP 地址与本机路径。
- **回收站永久删除**：仅在禁用线路、Provider 退出且无活跃租约时允许执行，仅清理 MDD 控制面记录，绝不擦除物理 SIM/eSIM profile。

---

## 六、版本升级、回滚与历史版本清理

1. **受控升级与回滚**：
   - 升级前记录当前 `/usr/lib/mdd/current` 目标目录与 `/etc/mdd`、`/var/lib/mdd*` 备份摘要，确认通话、短信、eUICC 操作与数据借用处于空闲或维护排空状态。
   - WebUI 在线更新（`mdd-updater`）或命令行 `install-release.sh install` + `restart` 切换后，核对 `/v1/system/runtime` 的 `build_version` / `vcs_revision`、国家出口 `ready` 状态及在线 Agent 重连代际。
   - 如需回滚，将 `/usr/lib/mdd/current` 指回上一已验证 release 目录并执行受控重启（注意若跨越出口中继持久化契约边界，需按维护计划核对一次共享出口配置收敛）。
2. **历史版本目录清理**：
   - `/usr/lib/mdd/releases/` 下建议始终保留 **当前运行版本** 与 **紧邻上一可回滚版本** 两个不可变目录；更早的历史解包目录与临时构建缓存可在核对软链接与运行进程映像路径后安全清理以释放磁盘空间。

---

## 七、当前正式发布与生产部署回执（v2.2.0）

- **同源发布摘要（2026-11-27）**：源码 `c805d1164f64e78bcd979f98f6b334f195e4be3d`，tree `19348a841458a6150bdab7a3bd69585659371ee3`。
  - `android-preview-v2.2.0`（工作流 `46001913107`，`versionCode=253`）：`mdd-agent-preview.apk` SHA-256 `d78ab4e2a14644589df4ebc546ae23cfd86ab577eeb950df04eef5a37b55bbf7`，保留稳定预览签名与包名。
  - `v2.2.0` `Latest`（工作流 `46003552438`）：
    - `mdd-2.2.0-linux-amd64.tar` / `mdd-c805d1164f64-linux-amd64.tar`：`d94c14714666c4eb079cea77d0773c2df5308fde054e3a0657e1b229ffd5db02`
    - `MDD-Agent-Linux-arm64.tar.gz`：`77bfb4b2fa43efa176cebbfb7f223d4e65dfb5998c0d5c933739a2fbfb71462a`
    - `MDD-Agent-Windows-amd64.tar.gz`：`9013b42c57b3664ee5660b285e9ee20218009506a5a89c17c8e600d1442082a7`
    - `MDD-Agent-macOS-arm64.tar.gz`：`50331ce85ff071ddcbb43791d6fe35aff021fe0934dd1b14392a3a5a17d7311f`
    - `MDD-Agent-macOS-universal.tar.gz`：`c63db180142fe5104868c6267b6d2cddb220d5cb118fa36473d26c33c1a8ed66`
- **受控生产升级与校验边界（2026-11-27）**：
  - Linux amd64 网关主机在维护排空保护下切换至 `/usr/lib/mdd/releases/mdd-c805d1164f64`（保留 `mdd-6236b1a6db37` 作为回滚目录并清理更早历史目录），`mdd-core`、`mdd-egress`、`mdd-provider-apply`、`mdd-agent`、`mdd-updater` 及 7 个运行中 `mdd-vowifi@` 实例进程映像 SHA-256 全部匹配 manifest；配置、TLS 指纹与 `mdd-cellular-guard.service`（`active`）保持不变。
  - `/v1/system/update/check?force=true` 返回 `current="2.2.0"`、`latest="2.2.0"`、`comparison_known=true`、`update_available=false`；`fr`/`gb`/`hk` 三个国家出口均 `ready=true`，蜂窝借用 Donor 会话 `ready`，3 条在线英国 VoWiFi 线路恢复 `ims=ready`（`200 OK`）。
  - 三台 SSH 可达远端 Agent（Linux ARM64、Windows 11 AMD64、macOS ARM64）升级至同源 `v2.2.0` 并重连（含 Windows 远端 Whole-Modem USB/IP 导出至网关主机及对应出口恢复）；另一台未开放免密 SSH 的 macOS 读卡器主机保持既有 `1bfcf9a` 连接与双读卡器在线未中断；未执行任何付费通话/短信、SIM PIN 或 eSIM profile 写操作。
