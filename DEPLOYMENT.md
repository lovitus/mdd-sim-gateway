# MDD VoWiFi Gateway - 完整部署与维护手册

本项目通过 SIM/eSIM 读卡器或受支持的蜂窝模块提供通话与短信管理。VoWiFi 使用运营商 ePDG/IMS；蜂窝通话使用远端 Agent。费用按运营商套餐及漫游规则执行，不保证免费。

---

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
|  mdd-core                 配置、鉴权、页面、状态事实、Agent 与浏览器 WSS       |
|  mdd-provider-apply       受限的线路 Provider 配置应用                         |
|  mdd-vowifi@<line>        每线路独立的 Go VoWiFi Provider                      |
|  mdd-egress               只监听回环 SOCKS 的用户态国家出口                    |
|                                                                               |
|  默认公开入口: HTTPS/WSS 8443；不要求额外 RTP/SIP 端口                         |
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

默认 Go VoWiFi 路径为浏览器 → Core 同源 WSS relay → Go Provider 原生 SIP/RTP bridge；蜂窝路径为
浏览器 → Core → Agent PCM。修改版 Asterisk 不属于当前 release 或运行时。每通
电话有独立 owner，准备阶段不发送付费拨号/接听；
只有当前双向音频、实际采集/播放计数及新鲜挑战证据通过后才提交。重复请求不重放付费动作，
同线路跨端或跨通话模式争用会被拒绝；未确认终态前保留占用。

页面关闭、连接中断或媒体证据失效会进入精确终止流程；VoWiFi 和蜂窝分别保留 Asterisk/Agent
本地有界租约作为兜底。界面区分“结束中”和“终止未确认”，不能将 HTTP 成功等同于物理挂断。
麦克风仍受浏览器安全上下文约束：通常使用 HTTPS，localhost HTTP 是浏览器支持的例外；这与
已经删除的媒体 IP 确认不是同一件事。升级后请刷新旧页面，旧 SIP/媒体确认接口已退役。

构建/交付记录应保存源码 revision、artifact SHA-256、release manifest 与生产安装 receipt；只有这些
可验证对象能证明运行来源。

### 受限网络中的下载代理

生产主机需要统一下载出口时，应把“业务出口代理”和“主机下载代理”分开。主机下载代理只负责
Release 和系统包；不得写入 SIM 线路或 VoWiFi 出口配置。

- `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY` 和小写同名变量用于登录 shell、安装脚本、curl、wget
  等下载工具；Git 和 APT 还应配置各自的持久代理。
- 如果上游只提供 SOCKS5，建议复用已发布的 sing-box 二进制，在本机回环地址提供一个独立 HTTP
  CONNECT 适配入口，下载工具指向该入口，适配器的唯一 outbound 指向 SOCKS5。不要为此重新编译
  网络组件，也不要让无认证入口监听公网或局域网地址。
- `NO_PROXY` 必须包含 localhost 和管理网段，避免 Core、Agent 与代理上游形成环路。适配器不可用时
  下载应失败，不能自动回退直连。
- 验证至少包括 curl/wget 连接到本地适配入口、适配器日志显示目标经 SOCKS outbound，以及一次
  release checksum 下载；临时配置随即清理。

代理地址属于单机部署数据，不应硬编码进仓库；具体值保存在目标主机权限受控的环境文件、APT
配置和适配器配置中。

---

### 方案 A：不可变 Go artifact 安装（推荐）

GitHub 的 `Go Runtime CI and Release` workflow 会生成一个 Linux tar。普通 main push 的临时 artifact
只用于 job 间验收；只有精确 `v*` tag 在 Linux fresh-host、Windows 和 macOS 门禁全部通过后，才把三平台
包及 `SHA256SUMS` 发布到 GitHub Release。下载后先核对 checksum。Linux tar 的外层包含
`install-release.sh`，内层只有一个严格 release 目录；manifest 记录每个二进制、systemd unit、
Provider 对应源码和许可证的大小、模式及 SHA-256。目标机不需要 Git checkout、Python 或 Docker。
目标宿主必须提供 Linux systemd、`realpath`、`curl`、`openssl`、coreutils `timeout`，以及标准的
`/usr/sbin/useradd`、`/usr/sbin/groupadd`、`/usr/sbin/nologin`。离线 tar 入口还需要 Bash、`tar`
和 `find`；缺少依赖时安装器会在改变服务运行状态前失败。

```bash
mkdir mdd-release && tar -xf mdd-<revision>-linux-amd64.tar -C mdd-release
cd mdd-release
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
sudo ./install-release.sh stop
sudo ./install-release.sh uninstall
```

六个动作边界固定：

- `install RELEASE_DIR`：校验完整 artifact，原子安装并切换不可变 release；仅 fresh host 创建
  `/etc/mdd` 下的管理员认证、Agent token、TLS 和 Core 配置。已有完整配置逐字保留，运行服务不重启。
- `start`：只启用并启动 `mdd-core`、`mdd-provider-apply`、`mdd-egress`。空 catalog 不启动任何
  `mdd-vowifi@` Provider；已有且明确 enabled 的 Provider 实例会在 Core 就绪后恢复。不会自动启动 endpoint Agent。
- `restart`：明确的维护操作，才会重启上述三个固定服务；部署或更新从不隐式调用。
- `stop`：先停止 apply helper，再停止当前严格识别的 Provider，最后停止 Core 与 egress；不禁用开机启动，
  也不停止独立 endpoint Agent。
- `uninstall`：在二次核对所有托管 unit 已停止并禁用后，只移除严格 manifest 对应的 Go release、稳定链接和
  MDD unit；保留 `/etc/mdd`、`/var/lib/mdd*`、安装/应用审计记录及 `mdd` 用户，便于原数据重装恢复。
- `status`：显示三个 unit，并以 `/etc/mdd/tls/server.crt` 同时完成 CA 和 SPKI pin 校验后请求
  `/healthz`；禁止 `-k`。非默认监听端口可通过 `MDD_STATUS_PORT` 指定。

首次安装必须从 stdin 提供 1–256 个 UTF-8 字符的管理员密码；TTY 输入默认隐藏，非交互部署应
由权限受控的 secret/file 写入 stdin，不能把密码放进 argv 或环境变量。密码不会进入 receipt
或日志。bootstrap 只生成 localhost、严格校验后的当前 hostname、`127.0.0.1` 和 `::1` SAN，
不猜物理网卡、VPN 或默认路由地址。首次生成的共享 Agent token 只用于迁移期；系统设置可按稳定
Agent ID 签发、轮换和撤销独立 token。逐台写入 owner-only Agent 配置并确认新凭据重连后，再切换
scoped 模式关闭共享 fallback；未知或撤销的 Agent ID 将被拒绝，旧活动控制/媒体/数据/USB-IP 会话
同时关闭。transition 期间若客户端更新失败，可显式 unenroll 该 ID 回到共享 fallback；scoped 模式禁止
删除身份记录。Core 不获得 `/etc/mdd` 的目录写权限，认证文件由现有 root helper 经本机 Unix socket
严格校验后原子替换；响应丢失时只用完整文档 SHA-256 读回确认，不盲目重放凭据 mutation。签发响应只
回显新 token 一次，凭据状态、日志和备份均不回显秘密。远程浏览器必须使用
该 hostname（并正确解析），且在受控的系统/浏览器
信任存储中信任这张精确自签证书；也可通过持有匹配受信任证书的 HTTPS/WSS 反向代理访问。
Agent 使用 SPKI pin；这些都不是让用户确认某个接口 IP，也不能以跳过证书校验代替。

### 方案 B：离线安装同一 Go artifact

在线与离线没有两套部署逻辑。将上面的完整 tar 复制到主机后执行：

```bash
sudo ./offline-install.sh install /absolute/path/mdd-<revision>-linux-amd64.tar
sudo ./offline-install.sh start
sudo ./offline-install.sh status
```

`offline-install.sh` 只解包并调用 artifact 自带的同版本 Go installer；它拒绝绝对归档条目、
路径穿越、链接/设备节点、多 release 目录以及不规范的输入路径；它不会探测或导入其他运行时。

---

### 方案 C：Nginx 反向代理默认 Go 入口

Go Core 的页面和 API 使用根路径（`/`、`/assets`、`/api`、`/v1`），推荐给它独立域名并整体
反代。旧 `/mdd` 前缀不受支持。以下示例连接默认 HTTPS
上游；信任文件和 `proxy_ssl_name` 必须换成实际网关证书的信任锚和 SAN 名称。保留原始 Host
（含端口），否则会破坏浏览器同源校验。不要假定默认还有 HTTP 8000 端口。

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    # 将该独立域名的完整根路径交给 MDD Go Core。
    location / {
        proxy_pass https://127.0.0.1:8443;
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
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
```

---

## 三、客户端部署与支持边界

Windows、macOS 与 Linux 只使用 release 中的统一 Agent 包，具体命令见
[`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md)。旧轻量 Card Agent、Android VPCD App、Python
`mdd-card-agent.service` 和明文 35963 入口已经退役；不得从历史 artifact 或文档恢复并与统一 Agent 并行。

统一 Agent 在现有认证 WSS health 上协商 `agent-host-health-v1`，上报平台、架构、精确构建、运行方式及
配置所在文件系统的量化容量；不另开端口，也不为健康采集探测 Modem、PC/SC 或音频。系统设置页从同一
topology计算 reader/modem 与隔离状态，Core receipt 超时显示 delayed；scoped 凭据存在但当前无连接显示
offline。旧 Agent 没有该能力时明确显示“此版本未上报”，不得猜测为 Linux 或健康。

### 1. Windows 客户端
安装 [`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md) 中的统一 `MddAgent` SCM 服务；它同时管理
本机允许的 Modem 与全部 PC/SC/eSIM 读卡器，并通过同一个 CLI/GUI 控制面报告状态。只有安装/配置
变更需要管理员权限，日常状态和受控启停使用安装时创建的 Operators 组。

### 2. macOS 客户端

使用统一 `MDD Agent.app` 或 `mdd-agent`，当前部署默认 `modem_enabled=false`，仅管理多 PC/SC/eUICC
读卡器；不会枚举或接管 Modem，也不会索取其麦克风权限。Modem 代码保留，但其语音和私有数据面
尚未作为当前版本交付，不应自行开启或宣称 Intel/其他 Modem 已通过实机矩阵。
首版不安装 launchd；菜单栏 GUI 或 CLI 只能运行一个，重复启动固定退出 `9`。GUI 首次运行会把
Token 保存到当前用户的 `~/Library/Application Support/MDD Agent/config.json`；目录权限为
`0700`、配置文件为 `0600`。关闭状态窗口只隐藏到菜单栏，选择“退出 MDD Agent”才释放硬件。

先在系统设置为该客户端的精确 Agent ID 签发独立 Token。CLI 会优先使用配置文件中的 Token；文件未配置时，依次使用 `--token`/`--token-stdin` 和
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

当前 Linux release 已包含统一 Go `mdd-agent` 与 `mdd-agent.service`，支持 PC/SC／eUICC
读卡器的远程高层协议。服务端安装只放置二进制和 unit，不会自动启用 endpoint Agent；先在设备上
生成 owner-only 配置并写入服务端为该精确 Agent ID 分配的独立 token 与证书 SHA-256，再显式启动：

```bash
sudo install -d -m 0700 /var/lib/mdd-agent
sudo mdd-agent config init -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set server gateway.example.com:8443 -config /var/lib/mdd-agent/config.json
printf '%s\n' "$MDD_AGENT_TOKEN" | sudo mdd-agent config set token --stdin -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set tls_sha256 "$MDD_TLS_CERT_SHA256" -config /var/lib/mdd-agent/config.json
sudo systemctl enable --now mdd-agent.service
```

Linux Modem 适配与 raw USB 透传尚未开放。后者必须先完成当前 Agent、Modem equipment ID、ICCID
三元显式绑定和服务端数据隔离；PC/SC/eUICC 不会切换到 raw USB 模式。

### 4. Agent 本地 SIM PIN

SIM 配置页先读取当前卡的 PIN 状态和剩余次数。页面只在剩余次数大于 2 时开放一次验证，并明确区分
“仅验证一次”和“验证并保存到 Agent”。保存值只写入当前 exact Agent 的 owner-only 配置，Core 不持久化
PIN；旧 Agent 未协商 `sim-pin-config-v1` 时保存入口禁用。移除保存值需要当前配置 revision，只修改 Agent
配置且不向 SIM 发 APDU。任何 unknown 结果都不得重复提交，应重新读取状态和 Agent 保存状态。

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

## 五、线路诊断与支持包

- “诊断”页先选择一条已保存线路，再显式点击刷新；页面不会自动轮询日志。
- 日志只包含最多 500 条类型化 Agent/Provider/Core 状态事件，不读取或暴露 systemd journal、Windows
  Event Log、macOS Console、原始 SIP、AT 或 APDU 内容。
- 页面可按来源筛选并下载同一份脱敏 JSON。全局支持包同样只包含结构摘要和总计有界的脱敏事件；不包含
  SIM 身份、电话号码、设备序列、凭据、网络地址、本机路径或密码字段。

回收站中的线路可以永久删除。操作前必须先禁用线路、Apply 使 Provider 退出，并确认没有 raw USB、通话、
浏览器媒体、蜂窝数据、余额查询或通知投递 lease。页面要求输入完整线路 ID，可选择保留已结束的短信与通话
历史；开始后该选择被冻结。中途失败时再次确认会续接同一 operation，不得 Restore 或新建同 ID 线路。
该操作只删除 MDD 控制面数据，绝不删除物理 SIM/eSIM profile，也不向运营商发送注销请求。

---

## 六、上游代码同步与维护 (Rebase / Cherry-Pick)

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
