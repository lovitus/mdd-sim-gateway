# MDD VoWiFi SIM Gateway (企业级多卡 VoWiFi / VoLTE 网关)

[![Release](https://img.shields.io/github/v/release/lovitus/mdd-sim-gateway)](https://github.com/lovitus/mdd-sim-gateway/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-green.svg)](https://github.com/lovitus/mdd-sim-gateway)
[![License](https://img.shields.io/badge/license-GPL--3.0--only-blue.svg)](LICENSE)

MDD VoWiFi SIM Gateway 是一个以 **Go Core、Go VoWiFi Provider 和统一 Agent** 为默认运行时的
智能卡蜂窝网关。浏览器与 Agent 通过同一个 HTTPS/WSS 入口连接 Core；线路配置、硬件事实和
Provider 运行事实由各自唯一 owner 管理。部署不再默认启动 Python Control、Docker Engine 容器
或宿主机路由编排。

仓库的发布与安装入口只包含 Go Core、Go Provider、统一 Agent 和明确列入 manifest 的原生能力边界；
旧 Python/Docker 部署不再提供构建、安装或运行入口。

---

## 🌟 核心特性与架构亮点

1. **不可变 Go 服务端发布**：
   - 严格 manifest 校验后原子安装到版本化目录；普通安装不会隐式启动或重启服务。
   - Core、Provider apply 和用户态国家出口各有清晰进程边界；单线路 Provider 按显式配置启动。
2. **统一 HTTPS/WSS 入口**：
   - 页面、管理 API、Agent 和浏览器 PCM 都复用一个公开 TCP 入口，不要求用户确认服务器网卡 IP，也不暴露 RTP 端口。
3. **跨平台统一 Agent**：
   - 发布矩阵为 **Linux amd64 服务端/Agent、Linux ARM64 reader-only Agent、Windows amd64 Agent、macOS arm64 Agent**；不能宣称所有硬件免驱或所有架构已验收。
   - macOS 默认 **PC/SC-only**，Modem、音频和私有数据能力保留安全门禁；不为满足清单擅自开启。Windows、macOS、Linux 均为远程 Agent 的当前目标。
   - 三大平台开启 4G 时必须强制独占隔离；隔离失败不放行、不回落宿主网络、不覆盖用户开关。
   - 本轮不做 macOS 公证，不使用 `.p8`；现有签名检查保留，Universal 包装另行记录。
   - **原生 Android 客户端已阶段性交付为预览版**：可共享受支持的读卡器，也可不插读卡器，使用服务器线路接打电话、收发短信；不是已退役的 VPCD APK。当前安装版本与既有实机证据见 [版本化状态](docs/status/README.md)，长时间 USB/休眠、耗电及极端切网验收仍单独记录。
4. **证书固定与最小生命周期动作**：
   - Agent 使用保存的证书指纹；安装器的状态检查同时验证本地 CA 证书和服务端 SPKI pin，禁止 `-k`。
   - `install`、`start`、`restart` 分离；只有明确执行 `restart` 才会重启运行服务。
5. **浏览器管理与通话**：
   - 内置页面覆盖线路、设备、eSIM、短信、诊断和同源 WSS 语音；挂载页面使用同一个会话级 Go 通话 owner。
   - [浏览器本地可选录音](docs/features/local-recording.md)已实现：每次明确确认同意后开始，支持保存/丢弃、静音及通话结束清理。现有自动化包含真实 Chromium 的合成音频验证，不冒充真实通话录音验收；平台和录音的未验场景继续保留。
6. **eSIM 删除的最终方向**：
   - 标准物理删除＋删除通知留存＋多重确认手动重放；此前保留 profile 报删的软删除方案已放弃，不再列为定制待办。

---

## 🚀 快速开始

### 1. 服务端部署（默认 Go artifact）

**v2.0.0 已正式发布（2026-10-18）：** 以下固定下载入口已公开，Go `v2.0.0` 是当前 Latest；
Android 是独立 preview。公开包及校验清单已下载核对，准确源码和 CI 见
[发布回执](DEPLOYMENT.md#2026-10-18-v200-正式发布非生产升级)。发布没有自动升级现有设备。
旧 `v1.3.14` 属于 Python/Docker 时代；Actions 临时工件也不是正式下载入口。

10 月 19 日已按后续授权升级生产服务端和六台桌面/Linux Agent；指定 Android 手机已安装 v182，
本轮应用内验证按用户决定跳过。随后 PR45 仅升级故障 Linux Agent，策略已恢复就绪、
原借用和出口已恢复；其余设备仍为 v2.0.0。准确版本及未验边界见
[部署回执](DEPLOYMENT.md#2026-10-19-pr45-linux-策略恢复)，不代表全端业务或耐久全部通过。

10 月 22 日 PR46 已合并并按授权仅切换 Core，认证失败文案已部署；原借用/出口已恢复，
配置、证书 pin、开关和隔离保持。实际 Chrome 主页面已回看；本轮没有认证失败现场样本，
不把组件回归冒充现场复现。精确工件和短暂恢复记录见 [部署回执](DEPLOYMENT.md)。
临时诊断已结束并清理，事件和结束回执已不可取，最初断口原因仍未知。
简化部署的 Mac GUI 接入已验；正式 Linux 包的首次 CLI 默认路径与身份复用旅程已在
无硬件 hosted 环境补验（`39746284080`），不代表其他平台首次安装或硬件验收。

| 用途 | 固定下载入口 | 前提与部署说明 |
| --- | --- | --- |
| Linux amd64 服务端与 Agent | [mdd-2.0.0-linux-amd64.tar](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.0.0/mdd-2.0.0-linux-amd64.tar) | 服务端需 systemd，按下节安装；Agent 可按手动流程运行 |
| Linux ARM64 读卡器 Agent | [MDD-Agent-Linux-arm64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.0.0/MDD-Agent-Linux-arm64.tar.gz) | glibc >=2.39、PC/SC、pcscd/libccid；仅 reader，不含 Core/Provider/modem guard；可不使用 systemd |
| Windows amd64 Agent | [MDD-Agent-Windows-amd64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.0.0/MDD-Agent-Windows-amd64.tar.gz) | [首次服务安装与已有服务升级](agent/MODEM_AGENT.md#windows)；程序未做 Authenticode 签名 |
| macOS arm64 Agent | [MDD-Agent-macOS-arm64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.0.0/MDD-Agent-macOS-arm64.tar.gz) | macOS 15+；原 Developer ID 签名，未公证；[配置和安装](agent/MODEM_AGENT.md#macos) |
| Android 9+ 原生客户端（预览） | [独立 Android preview](https://github.com/lovitus/mdd-sim-gateway/releases/tag/android-preview-v2.0.0) | 原 application ID/稳定 preview 签名，独立 versionCode；读卡器可选，不支持本机 modem |

从 [v2.0.0](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v2.0.0) 同时下载 `SHA256SUMS`，核对所选文件后再解包。
Linux 用 `sha256sum 文件名`，macOS 用 `shasum -a 256 文件名`，Windows 用
`Get-FileHash -Algorithm SHA256 文件名`，与清单对应行完整比较；Android 使用其独立 preview 的清单。
`RELEASE.json` 记录源码、tree 和资产哈希；Linux 版本号包与源码 revision 包逐字节相同。
只有精确版本 tag 通过原有门禁、草稿上传及下载读回校验后才公开。Linux tar 外层只包含安装脚本和一个经过严格 manifest
描述的 `mdd-<revision>` release 目录；不要从源码目录现场构建或回退到 Docker。
目标宿主需要 Linux systemd、`realpath`、`curl`、`openssl`、coreutils `timeout`，以及标准的
`useradd`、`groupadd`、`nologin` 账户工具；不需要 Git checkout、Python 或 Docker。

```bash
mkdir mdd-install
tar -xf mdd-2.0.0-linux-amd64.tar -C mdd-install
cd mdd-install
# 将 <revision> 替换为本目录中实际的 mdd-* 子目录后缀：
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
```

TTY 会隐藏首次管理员密码输入；非交互部署应从权限受控的 secret/file 通过 stdin 提供，禁止把
密码放进 argv 或环境变量。首次 `install` 创建管理员凭据、过渡期 Agent token 和仅含 localhost、当前
hostname 及回环地址 SAN 的自签 TLS 身份，但不会启动服务；重复安装保留现有配置及进程 PID。
新 Agent 可用首次配置向导，或网页“安全 → 添加 Agent”的一次性显示配置包；管理员口令不会保存在 Agent。
已有凭据不因安装新版自动轮换，不复制其他主机的 Agent 身份。
本机可打开 `https://localhost:8443/`；远程浏览器应使用证书 SAN 中的 hostname（并保证解析可达），
同时在受控的系统/浏览器信任存储中信任这张精确自签证书，或使用带匹配受信任证书的 HTTPS/WSS
反向代理。Agent 固定服务端叶证书 DER 的 SHA-256，不是 SPKI 哈希。系统不会要求用户确认或固定某个网卡 IP。证书固定和离线
artifact 安装见 [DEPLOYMENT.md](DEPLOYMENT.md)。

仓库根 `install.sh` 只转发到同一个 Go 安装器；在线与离线安装没有第二套运行时。

**已有 Go 部署升级：** 先保留旧包及权限受控的配置/状态备份，确认通话、数据和维护 owner 空闲，
使用现有安装器的 preflight/install/显式 restart 与回退流程。`install` 不等于已切换运行进程。
当前 SHA 标识构建可能显示“版本不可比较”，不应把它误判为已是最新版；保持原更新门禁，走明确版本的受控安装。
**旧 Python/Docker 1.x：** 本发行不提供原地自动数据迁移保证，不要把本节命令直接覆盖旧部署。
先保留原数据并完成针对性迁移；本批不恢复旧运行时或自动启用旧线路。

详细安装与配置说明参见：[完整部署与维护手册 (DEPLOYMENT.md)](DEPLOYMENT.md)。
开发、构建、制品溯源和临时证据规则见：[DEVELOPMENT.md](DEVELOPMENT.md)。

---

### 2. 统一 Agent 客户端

Windows、macOS 和 Linux 只使用 release 中的统一 `mdd-agent`（macOS 也提供 `MDD Agent.app`）。
同一进程管理该主机允许启用的 PC/SC/eUICC、Modem、短信、通话、音频和受隔离数据能力；不存在
另一个轻量 Card Agent、Android VPCD App、共享 token 客户端或明文 35963 兼容入口。
首次在终端运行 `mdd-agent`（或打开桌面 App），缺配置时按向导输入 Core URL，再选择网页登录或导入
网页“安全 → 添加 Agent”的配置包。已有配置按原 `-config` 路径启动，不重复初始化；向导也支持
`mdd-agent config setup -config /absolute/path/config.json`。平台安装、服务管理、证书 pin 和
当前实机支持边界见 [DEPLOYMENT.md](DEPLOYMENT.md#三客户端部署与支持边界) 与
[统一 Agent 手册](agent/MODEM_AGENT.md)。

嵌入式 Linux 用户可选择[手动下载、放置、配置及 crontab 保活流程](agent/MODEM_AGENT.md#嵌入式设备手动部署)，
不必为 reader Agent 安装 systemd。现有安装器及 modem 隔离逻辑不变；架构/运行库需匹配，
ARM64 的原生构建和一台读卡器主机已有独立回执，但不宣称无 systemd modem 已适配。

Android 使用独立的[原生预览客户端](android-agent/README.md)，复用 Core 线路和现有通话仲裁；
读卡器共享是可选用途，不是使用远程通话/短信的前提。预览交付不等于所有手机和运营商均已验收。

---

## 实现与验收状态

[docs/status/README.md](docs/status/README.md) 和同目录 JSON 是公开、可版本化的验收台账；根 TODO 摘要由它生成。
代码存在、CI 通过和生产实机验收是三种不同证据，未授权的收费或卡片写测试不会被补做来凑齐勾选。
台账中的 pending 数量是验收/证据分类，不是缺陷数量；已实现但尚缺现场证据的功能不得写成未实现。

## 🛠 代码维护与上游同步

本项目严格保持清晰模块化的 Commit 记录，详情请参阅 [DEPLOYMENT.md - 上游代码同步与维护](DEPLOYMENT.md#六上游代码同步与维护-rebase--cherry-pick)。
