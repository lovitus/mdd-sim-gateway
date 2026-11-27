# MDD VoWiFi SIM Gateway (企业级多卡 VoWiFi / VoLTE 网关)

[![Release](https://img.shields.io/github/v/release/lovitus/mdd-sim-gateway)](https://github.com/lovitus/mdd-sim-gateway/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-green.svg)](https://github.com/lovitus/mdd-sim-gateway)
[![License](https://img.shields.io/badge/license-GPL--3.0--only-blue.svg)](LICENSE)

MDD VoWiFi SIM Gateway 是以 **Go Core、Go VoWiFi Provider 和跨平台统一 Agent** 为唯一生产运行时的智能卡与蜂窝网关。浏览器与远程 Agent 通过单一 HTTPS/WSS 入口连接 Core；线路配置、硬件会话、国家出口和通话生命周期分别由唯一权威 owner 管理，不再依赖旧版 Python Control、Docker 容器或 Asterisk。

---

## 🌟 核心特性与架构亮点

1. **不可变 Go 服务端发布与平滑在线更新**：
   - 经严格 `manifest.json` 与 SHA-256 校验后原子安装到版本化目录；`install`、`start`、`restart` 严格分离。
   - `mdd-core`、`mdd-provider-apply`、`mdd-egress` 与单线路 `mdd-vowifi@<line>` 进程边界独立；支持语义化版本号对比与 Core 受控更新维护。
2. **解耦的国家出口与蜂窝借用中继**：
   - 每个蜂窝借用出口在 `mdd-egress` 内拥有固定回环 SOCKS5 中继端口（持久化于 `cellular-relays.json`）；借用租约续期、切换或回收仅切换中继上游，不会重启共享 `sing-box` 进程或中断无关国家的 VoWiFi 隧道。
3. **跨平台统一 Agent（Windows / macOS / Linux）與原生 Android 预览端**：
   - 统一 `mdd-agent` 覆盖 **Linux amd64、Linux ARM64（含 systemd managed 与无 systemd reader 手动模式）、Windows amd64、macOS 15+ arm64 及 Universal**。
   - Windows 支持同一 `MDDAgent` 服务下的显式授权普通用户连接管理（`mdd-agent service-access`）与无读卡器主机按需 `SCardSvr` 空库存感知；Linux 支持静态 IPv4 与 Ethernet ModemManager DHCPv4 蜂窝租约及语音/数据借用共存；三大桌面/服务器平台启用 4G 时均强制独占隔离且 fail-closed。
   - macOS 默认 **PC/SC-only**，保留 Developer ID 签名（`8WPJLUNLY8`，本轮不公证）。
   - **Android 9+ 原生预览客户端**独立发布，支持可选 USB 读卡器共享与服务端线路通话/短信。
4. **同源浏览器软电话、短信与 eSIM 管理**：
   - 内置 WebUI 提供线路、设备、eSIM（标准物理删除 + 通知留存 + 多重确认手动重放）、短信会话游标分页、脱敏诊断及可选[浏览器本地双声道录音](docs/features/local-recording.md)。

---

## 🚀 快速开始

### 1. 发行包下载（v2.2.0）

下载对应平台归档及同版本 `SHA256SUMS`，安装前请先校验 SHA-256 摘要（Linux：`sha256sum`，macOS：`shasum -a 256`，Windows：`Get-FileHash -Algorithm SHA256`）。`RELEASE.json` 记录精确源码 commit、tree 与资产摘要。

| 用途 | 固定下载入口 | 前提与部署说明 |
| --- | --- | --- |
| Linux amd64 服务端与 Agent | [mdd-2.2.0-linux-amd64.tar](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.2.0/mdd-2.2.0-linux-amd64.tar) | 服务端需 systemd，按下节安装；Agent 可独立按需启用 |
| Linux ARM64 Agent | [MDD-Agent-Linux-arm64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.2.0/MDD-Agent-Linux-arm64.tar.gz) | glibc >=2.39、PC/SC、音频 helper；支持 [systemd managed 首装](agent/MODEM_AGENT.md#arm64-独立-managed-agent-首装) 与 [嵌入式手动部署](agent/MODEM_AGENT.md#嵌入式设备手动部署) |
| Windows amd64 Agent | [MDD-Agent-Windows-amd64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.2.0/MDD-Agent-Windows-amd64.tar.gz) | [服务安装、升级与普通用户连接授权](agent/MODEM_AGENT.md#windows)；未做 Authenticode 签名 |
| macOS arm64 Agent | [MDD-Agent-macOS-arm64.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.2.0/MDD-Agent-macOS-arm64.tar.gz) | macOS 15+ Apple Silicon；Developer ID 签名，未公证；默认 PC/SC-only |
| macOS Universal Agent | [MDD-Agent-macOS-universal.tar.gz](https://github.com/lovitus/mdd-sim-gateway/releases/download/v2.2.0/MDD-Agent-macOS-universal.tar.gz) | macOS 15+ arm64/x86_64；Developer ID 签名，未公证；Intel 实体硬件验收单列 |
| Android 9+ 原生客户端（预览） | [android-preview-v2.2.0](https://github.com/lovitus/mdd-sim-gateway/releases/tag/android-preview-v2.2.0) | 稳定 preview 签名与包名，支持覆盖升级兼容预览版；读卡器可选 |

### 2. Linux 服务端首次安装与受控升级

目标主机需具备 Linux `systemd`、`realpath`、`curl`、`openssl`、`timeout` 及标准 `useradd`/`groupadd`/`nologin` 工具，无需 Git、Python 或 Docker：

```bash
mkdir mdd-install
tar -xf mdd-2.2.0-linux-amd64.tar -C mdd-install
cd mdd-install
# 将 <revision> 替换为解压出的 mdd-* 目录后缀：
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
```

- **首次安装**：从 TTY（自动隐藏回显）或权限受控文件经 `stdin` 输入初始管理员密码，切勿写入命令行参数或环境变量。首次 bootstrap 自动生成 P-256 自签 TLS 证书（含 `localhost`、当前 hostname、`127.0.0.1`、`::1` SAN），兼容现代 Chrome/NSS 导入。
- **已有 Go 部署升级**：先备份配置与状态，确认无活跃通话/维护租约后执行 `install` 与显式受控切换，或通过 WebUI「系统设置 → 系统更新」确认目标版本后执行受控升级。旧 Python/Docker `1.x` 部署不支持原地覆盖升级。

### 3. 统一 Agent 客户端接入

Windows、macOS 与 Linux 统一使用 `mdd-agent`（macOS 同时提供 `MDD Agent.app`）。首次运行无配置文件时会自动进入交互配置向导，也可在 WebUI「安全 → 添加 Agent」生成一次性配置包导入。Agent 固定服务端叶证书 DER 的 SHA-256 指纹（`tls_sha256`）。各平台安装、服务托管与隔离细则见：

- [完整部署与维护手册 (DEPLOYMENT.md)](DEPLOYMENT.md)
- [统一 Agent 手册 (agent/MODEM_AGENT.md)](agent/MODEM_AGENT.md)
- [Android 原生预览端说明 (android-agent/README.md)](android-agent/README.md)
- [开发、测试与版本台账指南 (DEVELOPMENT.md)](DEVELOPMENT.md)

---

## 📋 实现与验收状态

- [docs/status/README.md](docs/status/README.md) 与 [docs/status/acceptance.json](docs/status/acceptance.json) 是仓库唯一的版本化需求与验收台账；根目录 [TODO.md](TODO.md)、[TODO_MACOS_AGENT.md](TODO_MACOS_AGENT.md) 与 [postponed-tasks.md](postponed-tasks.md) 均由 `node tools/repository-check.mjs --write` 自动生成。
- 代码实现、CI 验证、正式发布与现场硬件/资费验收严格区分记录；台账中的待验项表示证据边界而非代码缺陷。历史批次演进与旧部署回执统一归档于 [`docs/archive/`](docs/archive/)。
