# MDD VoWiFi SIM Gateway (企业级多卡 VoWiFi / VoLTE 网关)

[![Release](https://img.shields.io/github/v/release/lovitus/mdd-sim-gateway)](https://github.com/lovitus/mdd-sim-gateway/releases/latest)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS%20%7C%20Android-green.svg)](https://github.com/lovitus/mdd-sim-gateway)
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
   - **Android APK**：支持手机卡槽 OMAPI 直接读取及 Type-C OTG 读卡器。
   - **Go 单文件**：提供适用于 **Windows、macOS (Apple Silicon & Intel) 以及 Linux (amd64/arm64/armv7)** 的独立单文件客户端，无需安装 Python 或驱动，即插即用，内置物理防删指令安全拦截与自动重连机制。
4. **证书固定与最小生命周期动作**：
   - Agent 使用保存的证书指纹；安装器的状态检查同时验证本地 CA 证书和服务端 SPKI pin，禁止 `-k`。
   - `install`、`start`、`restart` 分离；只有明确执行 `restart` 才会重启运行服务。
5. **浏览器管理与通话**：
   - 内置页面覆盖线路、设备、eSIM、短信、诊断和同源 WSS 语音；旧版拨号盘与通话历史保留为页面复用基线，不把尚未完成的对齐冒充已交付功能。

---

## 🚀 快速开始

### 1. 服务端部署（默认 Go artifact）

从 [Releases 页面](https://github.com/lovitus/mdd-sim-gateway/releases/latest) 下载 Linux tar，并先按同一
Release 的 `SHA256SUMS` 核对文件。普通 main push 的 workflow artifact 只用于 CI 内部验收；只有精确
`v*` tag 在全部平台门禁通过后才发布可长期下载的 Release。Linux tar 外层只包含安装脚本和一个经过严格 manifest
描述的 `mdd-<revision>` release 目录；不要从源码目录现场构建或回退到 Docker。
目标宿主需要 Linux systemd、`realpath`、`curl`、`openssl`、coreutils `timeout`，以及标准的
`useradd`、`groupadd`、`nologin` 账户工具；不需要 Git checkout、Python 或 Docker。

```bash
tar -xf mdd-<revision>-linux-amd64.tar
cd <解包目录>
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
sudo ./install-release.sh stop
# 保留 /etc/mdd、/var/lib/mdd* 和 mdd 用户，只移除 Go 软件与 unit：
sudo ./install-release.sh uninstall
```

TTY 会隐藏首次管理员密码输入；非交互部署应从权限受控的 secret/file 通过 stdin 提供，禁止把
密码放进 argv 或环境变量。首次 `install` 创建管理员凭据、Agent token 和仅含 localhost、当前
hostname 及回环地址 SAN 的自签 TLS 身份，但不会启动服务；重复安装保留现有配置及进程 PID。
本机可打开 `https://localhost:8443/`；远程浏览器应使用证书 SAN 中的 hostname（并保证解析可达），
同时在受控的系统/浏览器信任存储中信任这张精确自签证书，或使用带匹配受信任证书的 HTTPS/WSS
反向代理。Agent 继续使用 SPKI pin。系统不会要求用户确认或固定某个网卡 IP。证书固定和离线
artifact 安装见 [DEPLOYMENT.md](DEPLOYMENT.md)。

仓库根 `install.sh` 只转发到同一个 Go 安装器；在线与离线安装没有第二套运行时。

详细安装与配置说明参见：[完整部署与维护手册 (DEPLOYMENT.md)](DEPLOYMENT.md)。
开发、构建、镜像溯源和临时证据规则见：[DEVELOPMENT.md](DEVELOPMENT.md)。

---

### 2. 客户端连接使用 (Go 单文件 / Android App)

从 [Releases 页面](https://github.com/lovitus/mdd-sim-gateway/releases/latest) 下载对应系统的客户端，并核对 `SHA256SUMS`：

* **Android 手机**:
  - 下载 `mdd-card-agent.apk`，打开 App 填入网关 IP、端口 `8443` 与在 WebUI 设置的 `Agent Token`，点击“启动 SIM 转发”。
* **Windows（只有 PC/SC 读卡器）**:
  ```cmd
  mdd-card-agent-windows-amd64.exe -server <服务端IP>:8443 -token "<AGENT_TOKEN>"
  ```
  带 4G/5G Modem 的 Windows 主机必须使用统一 `mdd-agent` SCM 服务；该服务会同时管理
  Modem 和全部 PC/SC/eSIM 读卡器，CLI、GUI 和安装说明见
  [远程 4G/5G Agent 手册](agent/MODEM_AGENT.md)。不要同时运行 Card Agent。
* **macOS**:
  ```bash
  ./mdd-card-agent-darwin-arm64 -server <服务端IP>:8443 -token "<AGENT_TOKEN>"
  ```
  上述命令适用于只有 PC/SC/eSIM 读卡器的轻量场景。连接 4G/5G Modem 时请使用统一
  `MDD Agent.app` / `mdd-agent`；它在同一运行时管理 Modem 与多个读卡器，并通过私有 raw-USB
  数据面避免蜂窝流量成为 macOS 系统网卡。部署和当前已验证硬件范围见
  [DEPLOYMENT.md](DEPLOYMENT.md#2-macos-客户端-apple-silicon--intel)。
* **Linux**:
  ```bash
  ./mdd-card-agent-linux-amd64 -server <服务端IP>:8443 -token "<AGENT_TOKEN>"
  ```

更多客户端参数与二次开发参见：[客户端使用手册 (agent/README.md)](agent/README.md)。

---

## 🛠 代码维护与上游同步

本项目严格保持清晰模块化的 Commit 记录，详情请参阅 [DEPLOYMENT.md - 上游代码同步与维护](DEPLOYMENT.md#五上游代码同步与维护-rebase--cherry-pick)。
