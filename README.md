# MDD VoWiFi SIM Gateway (企业级多卡 VoWiFi / VoLTE 网关)

[Release history](https://github.com/lovitus/mdd-sim-gateway/releases)
[![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-green.svg)](https://github.com/lovitus/mdd-sim-gateway)
[![License](https://img.shields.io/badge/license-GPL--3.0--only-blue.svg)](LICENSE)

MDD VoWiFi SIM Gateway 是一个以 **Go Core、Go VoWiFi Provider 和统一 Agent** 为默认运行时的
智能卡蜂窝网关。浏览器与 Agent 通过同一个 HTTPS/WSS 入口连接 Core；线路配置、硬件事实和
Provider 运行事实由各自唯一 owner 管理。部署不再默认启动 Python Control、Docker Engine 容器
或宿主机路由编排。

仓库的发布与安装入口只包含 Go Core、Go Provider、统一 Agent 和明确列入 manifest 的原生能力边界；
旧 Python/Docker 部署不再提供构建、安装或运行入口。

> **当前发布状态（2026-09-24）：** GitHub Latest 仍指向旧版 v1.3.14，不包含当前 Go 运行时，
> 也包含已退役客户端。请勿把它当作当前安装包。当前 Go 运行时尚无兼容的正式 tag Release；
> Android 是草稿/预览项目，生产 Core 尚未部署其 mobile API。

---

## 🌟 核心特性与架构亮点

1. **不可变 Go 服务端发布**：
   - 严格 manifest 校验后原子安装到版本化目录；普通安装不会隐式启动或重启服务。
   - Core、Provider apply 和用户态国家出口各有清晰进程边界；单线路 Provider 按显式配置启动。
2. **统一 HTTPS/WSS 入口**：
   - 页面、管理 API、Agent 和浏览器 PCM 都复用一个公开 TCP 入口，不要求用户确认服务器网卡 IP，也不暴露 RTP 端口。
3. **跨平台统一 Agent**：
   - 当前 CI 发布矩阵为 **Linux amd64、Windows amd64、macOS arm64**；包内包含 manifest 声明的原生 helper／依赖，不能宣称所有硬件免驱或所有架构已验收。
   - macOS 默认 **PC/SC-only**，Modem、音频和私有数据能力保留安全门禁；不为满足清单擅自开启。Windows、macOS、Linux 均为远程 Agent 的当前目标。
   - 三大平台开启 4G 时必须强制独占隔离；隔离失败不放行、不回落宿主网络、不覆盖用户开关。
   - 本轮不做 macOS 公证，不使用 `.p8`；现有签名检查保留，Universal 包装另行记录。
   - **Android 原生客户端目前处于草稿/预览阶段**；生产 Core 路由和真实硬件/后台验收仍未完成。旧 VPCD APK 不能冒充新客户端。跨平台实现与实机验收分别见 [版本化状态](docs/status/README.md)。
4. **证书固定与最小生命周期动作**：
   - Agent 使用保存的证书指纹；安装器的状态检查同时验证本地 CA 证书和服务端 SPKI pin，禁止 `-k`。
   - `install`、`start`、`restart` 分离；只有明确执行 `restart` 才会重启运行服务。
5. **浏览器管理与通话**：
   - 内置页面覆盖线路、设备、eSIM、短信、诊断和同源 WSS 语音；当前挂载页面只使用同一个会话级 Go 通话 owner；录音需用户明确启用；Android 仍是草稿/预览。平台验收须保留既有报告并仅核对缺口，不以本轮未复验清零历史证据。
6. **eSIM 删除的最终方向**：
   - 标准物理删除＋删除通知留存＋多重确认手动重放；此前保留 profile 报删的软删除方案已放弃，不再列为定制待办。

---

## 🚀 快速开始

### 1. 服务端部署（默认 Go artifact）

**当前没有可用于正式安装的 Go Release。** 不要从 [Latest Release](https://github.com/lovitus/mdd-sim-gateway/releases/latest)
下载 v1.3.14 的旧客户端或 APK。普通 main push 的 workflow artifact 只用于 CI 内部验收；只有精确
`v*` tag 在全部平台门禁通过后才发布可长期下载的 Release。正式发布时的 Linux tar 外层只包含安装脚本和一个经过严格 manifest
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
密码放进 argv 或环境变量。首次 `install` 创建管理员凭据、过渡期 Agent token 和仅含 localhost、当前
hostname 及回环地址 SAN 的自签 TLS 身份，但不会启动服务；重复安装保留现有配置及进程 PID。
登录系统设置后应为每个稳定 Agent ID 分别签发独立 token，逐台更新并确认重连，最后关闭共享
fallback；签发响应只显示新 token 一次，状态列表不回显秘密。
本机可打开 `https://localhost:8443/`；远程浏览器应使用证书 SAN 中的 hostname（并保证解析可达），
同时在受控的系统/浏览器信任存储中信任这张精确自签证书，或使用带匹配受信任证书的 HTTPS/WSS
反向代理。Agent 继续使用 SPKI pin。系统不会要求用户确认或固定某个网卡 IP。证书固定和离线
artifact 安装见 [DEPLOYMENT.md](DEPLOYMENT.md)。

仓库根 `install.sh` 只转发到同一个 Go 安装器；在线与离线安装没有第二套运行时。

详细安装与配置说明参见：[完整部署与维护手册 (DEPLOYMENT.md)](DEPLOYMENT.md)。
开发、构建、制品溯源和临时证据规则见：[DEVELOPMENT.md](DEVELOPMENT.md)。

---

### 2. 统一 Agent 客户端

Windows、macOS 和 Linux 只使用 release 中的统一 `mdd-agent`（macOS 也提供 `MDD Agent.app`）。
同一进程管理该主机允许启用的 PC/SC/eUICC、Modem、短信、通话、音频和受隔离数据能力；不存在
另一个轻量 Card Agent、Android VPCD App、共享 token 客户端或明文 35963 兼容入口。先在系统设置按
稳定 Agent ID 签发独立 token，再通过 stdin 写入 owner-only 配置。平台安装、服务管理、证书 pin 和
当前实机支持边界见 [DEPLOYMENT.md](DEPLOYMENT.md#三客户端部署与支持边界) 与
[统一 Agent 手册](agent/MODEM_AGENT.md)。

---

## 实现与验收状态

[docs/status/README.md](docs/status/README.md) 和同目录 JSON 是公开、可版本化的验收台账；根 TODO 摘要由它生成。
代码存在、CI 通过和生产实机验收是三种不同证据，未授权的收费或卡片写测试不会被补做来凑齐勾选。

## 🛠 代码维护与上游同步

本项目严格保持清晰模块化的 Commit 记录，详情请参阅 [DEPLOYMENT.md - 上游代码同步与维护](DEPLOYMENT.md#六上游代码同步与维护-rebase--cherry-pick)。
