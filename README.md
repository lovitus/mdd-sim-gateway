# MDD VoWiFi SIM Gateway (企业级多卡 VoWiFi / VoLTE 网关)

[![Release](https://img.shields.io/badge/release-v1.3.12-blue.svg)](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v1.3.12)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-green.svg)](https://github.com/lovitus/mdd-sim-gateway)
[![License](https://img.shields.io/badge/license-MIT-orange.svg)](LICENSE)

MDD VoWiFi SIM Gateway 是一个高可用、轻量级的 **VoWiFi 智能卡蜂窝网关与软交换系统**。
支持把插在 USB 智能卡读卡器（或 eSIM 卡槽）里的物理 SIM 卡/eSIM，通过跨平台轻量客户端连接到本地或云端 Linux 服务器，利用移动运营商原生的 **VoWiFi (Voice over Wi-Fi / 3GPP ePDG / IMS)** 核心网建立安全连接，在全球任意地点免漫游接打移动电话与收发短信。

---

## 🌟 核心特性与架构亮点

1. **零漫游与全球蜂窝连接**：
   - 基于标准 IPsec IKEv2（strongSwan）与 3GPP SIP/IMS 协议栈，卡槽插在办公室/家中，服务器可部署在本地或任意 VPS。
2. **多卡独立容器化引擎**：
   - 每张 SIM 卡拥有独立的容器隔离环境与 Linux 网络命名空间（`ipsec0`），互不干扰，支持根据卡归属国自动绑定出站代理分流（Clash / Sing-box / Xray）。
3. **极简跨平台 Go 客户端（Card Agent）**：
   - 提供适用于 **Windows、macOS (Apple Silicon & Intel) 以及 Linux** 的独立单文件客户端，无需安装 Python 或驱动，即插即用，内置物理防删指令安全拦截与自动重连机制。
4. **全功能 Web 管理平台**：
   - **双模鉴权（Header Token + Cookie）**：杜绝跨端口、跨协议或反代场景下的 Cookie 丢失与 401 报错。
   - **双并发端口监听**：同时支持 `HTTP 8000` 与 `HTTPS 8443`，支持 `/mdd` 统一上下文路径，单条 Nginx `location /mdd/` 即可完成完整反代。
   - **内置 WebRTC 软电话**：支持在浏览器中直接拨打与接听真实手机号码，集成同端口 WebSocket 代理。
   - **长短信防拆分聚合**：自动拼接运营商多包到达的长短信。
   - **eSIM LPA 管理**：集成标准 LPA 引擎，支持直接在界面下载、启用、切换 eSIM 配置文件。

---

## 🚀 快速开始

### 1. 服务端离线一键部署

1. 从 [Releases 页面](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v1.3.12) 下载离线镜像包（`mdd-control.tar.gz` 和 `mdd-engine.tar.gz`），放置于 `images/` 目录下。
2. 在 Linux 服务器上执行：
   ```bash
   sudo ./offline-install.sh
   ```
3. 打开浏览器访问：
   * HTTP：`http://<服务器IP>:8000/mdd`
   * HTTPS：`https://<服务器IP>:8443/mdd`

详细安装与 Nginx 配置说明参见：[完整部署与维护手册 (DEPLOYMENT.md)](DEPLOYMENT.md)。

---

### 2. 读卡器客户端使用 (Go 独立单文件)

从 [Releases 页面](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v1.3.12) 下载对应系统的客户端：

* **Windows**:
  ```cmd
  mdd-card-agent-windows-amd64.exe -gateway <服务端IP> -port 35963
  ```
* **macOS**:
  ```bash
  ./mdd-card-agent-darwin-arm64 -gateway <服务端IP> -port 35963
  ```
* **Linux**:
  ```bash
  ./mdd-card-agent-linux-amd64 -gateway <服务端IP> -port 35963
  ```

更多客户端参数与二次开发参见：[客户端使用手册 (agent/README.md)](agent/README.md)。

---

## 🛠 代码维护与上游同步

本项目严格保持清晰模块化的 Commit 记录，详情请参阅 [DEPLOYMENT.md - 上游代码同步与维护](DEPLOYMENT.md#五上游代码同步与维护-rebase--cherry-pick)。
