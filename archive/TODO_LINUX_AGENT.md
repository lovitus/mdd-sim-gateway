# TODO — Linux 远程统一 Agent（暂时不实施，仅留作记录）

> **状态：暂时不实施，仅留作后续设计与评审记录。**
>
> 在维护者明确把本项切换为实施任务以前，本文件不授权修改 Agent、Linux 宿主编排、Control、
> Engine、安装脚本、systemd unit/拉起脚本、ModemManager、NetworkManager、网络命名空间、路由、
> 防火墙、
> USB 驱动绑定或真实设备配置，也不授权发送短信、拨号、接听或执行 eSIM 写操作。
>
> 本方案必须先经过多轮独立只读评审，实施完成后还必须针对实际 diff、自动化测试、发布包和
> 实机证据再次独立复审。评审必须主动排除误报、旧文档、已被新实现覆盖的场景和仅靠状态映射
> 制造的假成功；未取得最终 `PASS` 前不得宣称 Linux 统一 Agent 已完成。

## 1. 目标与完成定义

未来把 Linux 也作为可远程部署的统一 Agent 主机，使插在 Linux 主机上的以下设备能够安全连接
远端 MDD 网关：

- 发布矩阵和配置容量内的多个 PC/SC、CCID 和 eUICC/eSIM 读卡器；
- 发布矩阵和主机资源上限内的多个 AT、QMI、MBIM 或 ModemManager 4G/5G Modem；
- 把 Modem 内置 SIM/eUICC 当作远程智能卡使用，包括稳定 APDU、EID/ICCID/Profile 识别；
- 经能力和所有权门禁验证的数据、短信、通话信令、通话音频、飞行模式及反向蜂窝出口。

Linux Agent 必须同时满足两个目标：

1. 遵守 Windows/macOS 统一 Agent 已定义的远程部署、状态、安全和业务契约；
2. 最大限度复用当前项目已经在 Linux 网关上积累的设备发现、ModemManager、串口、VPCD、
   PC/SC、eSIM、短信、通话、数据和故障恢复逻辑，不再实现第二套 Linux Modem 系统。

“完成”不是 Linux 上能启动现有 Python 入口，也不是单台 EC20 能返回 `AT`。完成至少意味着：

- 与 Windows/macOS 共用同一 Agent Core、服务端协议和领域状态；
- Linux 具有正式的 machine-scoped 宿主、安装/升级/回滚、权限边界和本地控制入口；检测到 active
  systemd 时生成 unit，否则只部署并打印通用拉起脚本，由用户自行选择循环、crond、`rc.local`、
  init.d 或其他外部方式维护；OpenWrt 和其他无 systemd 的嵌入式 Linux 同样属于部署目标；
- 多读卡器和多 Modem 能独立热插拔、换卡、断线与恢复；
- PC/SC/eUICC、Modem SIM APDU、Modem 短信/蜂窝通话/音频、数据和飞行模式分别按实测能力发布；
- 蜂窝出口不能被宿主其他应用借用，Agent/Provider/网络命名空间退出后立即 fail-closed；
- 当前 Linux 网关本机设备模式保持兼容，并有回归门禁证明没有被迁移工作改坏；
- 实现后再从三端真实 Provider 中提炼统一设备描述，而不是提前用一份配置掩盖平台差异。

## 2. 当前事实基线：避免把已有能力或缺口误报

### 2.1 已存在且应复用

- `agent/card_agent.py` 已是 Linux/Windows/macOS 共用的 PC/SC WSS bridge，包含 TOFU、Token、
  多 reader supervisor、热插拔重连和 APDU 安全门禁；Linux 读卡器能力不是从零开始。
- `agent/go-agent` 已有 Linux PC/SC 单文件客户端，但它是轻量 Card Agent，不能冒充包含 Modem、
  短信、通话、数据和健康控制面的统一 Linux Agent；仓库另有运行 Python Card Agent 的旧
  `agent/mdd-card-agent.service` 示例，二者不能合并误报为现成的统一 systemd Agent。
- `agent/modem_agent.py`、`agent/modem_providers.py` 已承载跨平台 Modem 领域状态、SIM APDU、
  SMS/Call、付费操作门禁、远程控制和反向 tunnel 合约；Linux 不应复制这些业务状态机。
- `agent/call-audio-helper` 已有 Linux 音频 backend 和共享媒体 bridge；Linux 缺的是按同一 Modem
  USB topology 绑定端点、由统一 runtime 启用以及发布包/实机验收，不应误报为“完全没有音频实现”。
- `control/app/modem_registry.py`、远程 VPCD WebSocket 和动态 slot registry 已经是远程 Agent 的
  服务端入口；Linux Agent 不另建一套服务端 API、线路模型或 slot 数据库。
- `host/mdd_orchestrator.py` 已实现 Linux USB Modem 发现、稳定 USB path/IMEI 迁移、ModemManager
  object 关联、每设备 desired state、射频/数据协调、serial fallback、VPCD reader/bridge reconcile、
  失败证据和状态发布。
- `host/vpcd_modem_bridge.py` 已实现独占 AT、`AT+CSIM`、逻辑通道分配/清理、ModemManager command
  passthrough、IMEI/ICCID 元数据和多 VPCD slot；应抽取 Provider 核心，不应复制粘贴进 Agent。
- `control/app/cellular_sms.py` 和 `control/app/cellular_call.py` 已有 ModemManager SMS/Call 的
  结构化结果、超时、不确定结果与去重语义；服务端业务记录仍留在 Control，Linux 硬件操作部分
  才是未来抽取候选。
- `control/app/usbreader.py`、`engine/ami_usim.py`、`engine/pin_keeper.py` 与 `engine/swu_ike.py`
  已有 Linux PC/SC reader index 到 sysfs USB port 的稳定绑定及恢复路径。
- 当前线路、eSIM Profile、短信历史、Asterisk/WebRTC、VoWiFi/ePDG、出口选择和页面均已存在，
  Linux Agent 只能接入这些现有领域，不得复制。

### 2.2 目前不能宣称已完成

- `ManagedAgentRuntime.health_snapshot()` 当前只把 Windows/macOS 标为 `supported`，Linux 仍明确
  为 `unsupported`；通用代码可导入或启动不等于正式 Linux 运行时已交付。
- 服务端 health v1 的 meta 只允许 Linux 使用 `support=unsupported`、`collector=unsupported`、
  `manager=user-process`，snapshot 仍受现有 `cli/gui` 枚举约束；未来原生 Linux host health 需要
  版本化扩展客户端与服务端合约，并如实区分 `systemd` 与用户外部维护的 `manual`，不能伪装成
  macOS `user-process`。
- `agent/mdd-card-agent.service` 是旧的本地明文/读卡器示例，不是统一 Agent 的安全安装、控制、
  升级或回滚方案。
- 非 macOS 的通用 `ModemCard` 串口路径没有替代 Linux ModemManager/QMI/MBIM Provider，也没有
  完成多 Modem reconcile、Linux 宿主生命周期、权限与实机矩阵。
- `call_audio.py` 当前不把 Linux 标为已验证平台；音频 helper 中存在 Linux backend 不等于统一
  Agent 已经能够安全选择和使用正确 Modem 的音频端点。
- `agent/cellular_isolation.py` 只定义通用 helper 合约，仓库中没有已经验收的 Linux netns/cgroup/
  nftables 数据隔离实现。仅绑定 source IP 不能证明宿主其他进程无法使用蜂窝出口。
- 当前 Linux 网关的 ModemManager、NetworkManager、pcscd 和 VPCD 逻辑与本机 Control/文件路径/
  systemd 紧密耦合，不能整段复制到远程 Agent 后就称为解耦。
- 当前本机 Orchestrator 会按聚合状态启停共享 ModemManager、触发 udev、重启 pcscd，部分恢复路径
  还会处理全局 `qmi-proxy`。这些行为不能未经重新设计直接用于多设备远程 Agent，否则一台设备
  的变化会破坏其他 Modem、读卡器或宿主应用。
- `mmcli --command` 需要特定 ModemManager 配置并可能与 UIM/QMI 所有权冲突；它是已存在的兼容
  路径，不是所有 4G/5G 模块都支持的通用结论。
- Linux 通话信令存在 ModemManager 路径不等于音频已经可用；必须另行证明同一物理 Modem 的
  ALSA/PipeWire/UAC 或厂商 PCM transport，并完成真实全双工验收。
- Windows/macOS 当前仍标记为待 HIL、待第二厂商或待多 Modem 验证的项目，在 Linux 方案中也只能
  作为待完成契约，不能借“跨平台复用”改写成三端已有完成证据。

### 2.3 实施前必须先处理的公共契约债务

以下是本轮只读审查确认的现有公共问题，不是 Linux Provider 缺口，也不授权现在修改；未来实施
不能复制它们，并应先在共享 Core 中统一解决或形成经评审的迁移方案：

- 当前 Modem control/reverse tunnel 在关闭系统 TLS 校验的 WebSocket 握手中先把 Token 放入 URL，
  再做后置 TOFU；Linux 必须与 Card/health transport 一样在发送凭据前验证证书 pin，并推动共享
  transport 收敛，不能建立 Linux 专用的第三种安全顺序。
- Windows package builder 写入 manifest 字段 `bytes`，公共 verifier/运行时读取 `size`；Linux 发布
  必须直接采用唯一 canonical manifest schema，并以“真实组包 → verifier → 安装/运行”E2E 防止
  复制格式漂移，不能只验证脚本文本。
- POSIX 本地 control socket 当前把连接者视为 admin。Linux 受管宿主模式必须先用 socket 路径和权限
  阻止未授权连接，再用 `SO_PEERCRED`、uid/gid/受管组（必要时 polkit）映射公共 READ/OPERATE/ADMIN
  权限；不得仅因能连接就视为 admin，也不以 root-only socket 代替角色分级。
- 现有 `agent_id` 持久化失败时仍可继续使用临时值。machine host 必须定义格式、owner、symlink、
  原子持久化和失败策略，避免重启后变成新 Agent 或让低权限用户替换身份。
- 所有 host mode（systemd 或用户自行维护的 launcher）的退出、超时、shell/外部维护者消失和
  最终强杀都不能破坏 paid-operation marker 或 Modem 通话 cleanup quarantine；必须
  先设计可执行的权威终止/隔离、跨进程持久门禁和升级门禁，不能用无限等待、清空 marker 或强杀
  掩盖未知操作结果。

## 3. 不变量与明确非目标

- 统一的是 Agent 领域契约、状态和 Provider 接口，不是假装三个操作系统拥有相同设备枚举、驱动、
  数据链路或权限机制。
- 当前阶段不创建统一三端 Device Catalog。Linux Agent Provider 完成并积累第二种设备/协议证据后，
  才从 Windows、macOS、Linux 三端实现中抽取共同设备族描述和各平台 binding。
- 不把 `host/mdd_orchestrator.py` 整体移动进 Agent；只抽取不依赖 Control 数据目录、页面、线路、
  Asterisk、容器编排和网关本机配置的 Linux 硬件 Provider 核心。
- 不在 MDD 业务层重新实现 QMI、MBIM、ModemManager 插件、完整 PDU 栈或 eSIM LPA；优先使用系统
  ModemManager D-Bus、libqmi/libmbim、PC/SC 和现有服务端 eSIM 逻辑。
- 不因 Linux 有 root 或受管 init 就把整个宿主网络或所有 Modem 交给一个全局可变状态机；每个物理
  attachment 只有一个 coordinator，每个 function、数据 session 和付费 operation 都有明确 owner、
  lease/generation；SIM/eUICC 只承担身份和冲突语义，不新造 OS 资源 owner。
- 不把 `/dev/ttyUSB2`、ModemManager object path、PC/SC index、网卡名、USB interface number、
  产品字符串或枚举顺序当作稳定业务身份。
- 不以 `mmcli` 返回成功、进程存在、接口有 IP、页面显示绿色或一条自动化单测代替真实设备能力。
- 不自动重放短信、拨号、接听、DTMF、Profile 写入或其他有费用/有副作用操作。
- 不要求 Linux Agent 复制 Windows WFP 或 macOS private PPP 的具体机制；它必须提供等价的严格
  隔离与 fail-closed 结果，并在公共状态中如实报告 Linux backend。
- 不删除或替换当前网关本机 Linux 硬件模式。迁移期必须允许“网关本机 Orchestrator 模式”和
  “独立 Linux Agent 模式”并存，但同一物理 function 只能由其中一个所有者管理。

## 4. 目标架构

```text
跨平台 Agent Core（现有主线）
├─ AgentHost / installation lease / ConfigStore / local control / health
├─ DeviceSupervisor：desired attachments ↔ running contexts
├─ PcscProvider / ModemContext[] / Provider capability contract
├─ VPCD WSS + Modem control WSS + reverse TCP/UDP tunnel
└─ 付费操作、状态、重连、generation 与清理门禁
         │
         └─ Linux Platform Adapter
             ├─ LinuxServiceHost（foreground core + 可选 systemd unit + 通用拉起脚本）
             ├─ LinuxAttachmentDiscovery（sysfs + udev/mdev/hotplug + PCSC/MM/ubus）
             ├─ LinuxModemManagerProvider（优先 D-Bus）
             ├─ LinuxDataProvider（NM/MM D-Bus、OpenWrt netifd/ubus 或受控 direct backend）
             ├─ LinuxDirectAtProvider（仅在安全独占后回退）
             ├─ LinuxPcscProvider（复用现有 supervisor）
             ├─ LinuxCallAudioProvider（ALSA/PipeWire/UAC，按物理父设备匹配）
             └─ LinuxCellularIsolationProvider
                  └─ 每 Modem 独立 netns/data worker/reverse tunnel
```

Agent 只发布 attachment、能力、状态和可执行的远程操作。服务端继续拥有：

- Agent/SIM/eUICC/eSIM Profile/线路的业务绑定；
- VPCD slot 分配、VoWiFi/ePDG、Asterisk、SIP、WebRTC 和媒体路由；
- 短信历史、来电/呼叫业务状态、WebUI、通知、运营商和国家出口策略；
- 多 Agent 冲突、ICCID/EID 冲突和稳定入口选择。

## 5. Linux 逻辑的复用方式

### 5.1 可直接共用

- `AgentHost`、`ManagedAgentRuntime`、`ConfigStore`、本地控制协议、health reporter、日志脱敏、
  安装级单实例租约和公共 CLI 退出码；
- PC/SC reader supervisor、WSS/TOFU/Token、VPCD frame、APDU safety guard；
- Modem/SIM/SMS/Call/Data 的领域合约、能力语义、服务端 registry 和 reverse tunnel；
- UICC health、voice registration、SMS 去重/不确定结果和付费操作清理门禁；
- 服务端线路、eSIM、VoWiFi、Asterisk/WebRTC、出口和页面。

### 5.2 先抽取再复用

- 从 Orchestrator 抽取无宿主全局副作用的 USB/sysfs/udev discovery、physical identity、
  ModemManager object/function 关联、状态归一化和 per-device desired plan；
- 从 `vpcd_modem_bridge.py` 抽取串口 transport、CSIM、逻辑通道、身份读取及清理，接入统一
  `SimApduProvider`，保留旧 bridge 为兼容 wrapper，避免同时维护两份实现；
- 将 ModemManager SMS/Call 的硬件调用抽成 Linux Provider，保持 Control 中的业务入库、幂等、
  消息历史和页面接口不动；
- 将现有 NetworkManager/ModemManager 数据状态与接口识别转换为 D-Bus 驱动的每 Modem backend，不把当前
  Orchestrator 的全局启停和全局进程清理一起抽入；
- 将 sysfs USB port、IMEI、EID/ICCID 和 generation 组合成公共 attachment evidence，但业务身份
  继续由服务端按卡体/Profile 规则决定。
- 将 `SCARD_ATTR_CHANNEL_ID → bus/devnum → sysfs USB port` 的现有映射接入 LinuxPcscProvider；
  reader name/hash 只作显示和弱 hint，无法取得稳定物理证据时不得自动迁移 attachment/业务绑定。

### 5.3 必须留在 Linux 平台层

- sysfs、udev/mdev/hotplug、D-Bus/ubus、ModemManager、NetworkManager/netifd、libqmi/libmbim、
  tty、ALSA/PipeWire；
- systemd unit 生成、通用拉起脚本、Unix socket ACL、polkit/组权限、Linux capabilities、
  netns/cgroup/nftables 和宿主防泄漏检查；不为 procd、init.d、crond 或用户循环实现专用管理层；
- glibc/musl、BusyBox、发行版/嵌入式固件、CPU 架构、包管理、服务依赖、只读根文件系统及卸载恢复。

### 5.4 必须留在服务端

- Control 数据库/文件、线路生命周期、WebUI、eSIM Profile 业务对象、短信历史、Asterisk、
  VoWiFi Engine、国家出口和代理节点选择；
- 网关本机 VPCD listener/slot registry、远端 Agent registry 和冲突裁决；
- 跨 Agent 的 ICCID/EID 唯一性与业务迁移。

每类底层资源必须先确定唯一 owner，不能用“都是同一 Agent”掩盖 function 争用：

| 资源 | 首选 owner | 约束 |
| --- | --- | --- |
| CCID/PCSC reader | pcscd + 一个 Agent reader worker | PC/SC 名称只用于寻址，卡身份仍取 EID/ICCID |
| QMI/MBIM/网络 function | ModemManager/NetworkManager 或一个直管 Provider | 同一 function 不能被 MM 和 Agent 同时打开 |
| AT/Modem function | 一个持久 Provider | 必须按 USB parent + IMEI 绑定，禁止两个进程轮流 shell-out 抢口 |
| Modem 内 UICC/APDU | 已验证的 MM/QMI-UIM/独立 AT Provider | 与数据不能并发时使用同一 per-Modem 所有权门禁暂停并恢复数据 |
| UAC/ALSA/PipeWire endpoint | 每次通话一个受控 audio helper | 必须属于同一 physical Modem，不能采用系统默认声卡 |
| netns/路由/DNS/reverse worker | 每 Modem 一个 data context | context/generation 消失时旧 socket 与规则必须同时失效 |
| SIM/eUICC/eSIM Profile/线路业务 | 服务端 | IMEI、USB path、reader、slot 均不能成为卡片业务身份 |

## 6. Windows/macOS Agent 等价契约

Linux 实现必须加入共享 contract-conformance fixtures。以下语义必须与 Windows/macOS 等价：

- 一个安装只有一个硬件 runtime；CLI/GUI/服务查询不能创建第二个 owner；
- `status`、`devices`、`doctor`、`logs`、`config`、`reconnect`、`audio.reprobe`、`self-test`、
  `maintenance.prepare-install`、`maintenance.cancel-install` 的版本、权限、deadline、1 MiB 上限、
  错误和退出码；
- owner-only/machine-protected 配置、Token 脱敏、WSS、TOFU/显式 pin、Agent package identity；
- Agent 主动建立 VPCD、Modem control、health、媒体和数据 reverse WSS；远程部署不要求网关主动
  连接 Agent，不开放未鉴权的公网 listener，也不回退跨公网明文 VPCD；
- 多 PC/SC/eUICC reader、多 Modem、热插拔、换卡、迟到事件、重复事件和 generation fencing；
- 每次远端 Modem session 绑定现有的 `agent_id + modem_id + session_id`，重连发布完整 snapshot；
  平台 attachment generation 在本机 supervisor 独立 fencing，旧 `session_id` 或旧 attachment
  generation 的事件、响应和增量均不得污染新 session；
- attachment、IMEI、EID、ICCID、Profile ICCID、VPCD endpoint/slot 的身份分层；
- SIM APDU、SMS、Modem Call Signalling、Modem Call Audio、Data、Flight Mode 独立能力及动态 readiness；APN/Profile
  list/save、自定义认证、数据漫游意图和重启恢复使用同一远端 RPC/状态语义，secret 永不回传；
- `modem_call_signalling/modem_call_audio` 只表示 4G/5G Modem 执行的蜂窝拨号和设备音频；eSIM/
  eUICC 只提供卡片凭据/Profile，本身不因外部进程维护方式而关闭。Modem 内 eSIM 仅在实际参与
  Modem 蜂窝通话时适用该通话门禁；PC/SC eUICC 和服务端 VoWiFi Engine/Asterisk 通话分别遵守
  APDU 与服务端 CallCoordinator 的生命周期，不得被误归为 Linux Modem 通话能力；
- 所有 APDU Provider 共用同一破坏性指令门禁，永久禁止 `ES10c.DeleteProfile`、`DELETE FILE` 及
  等价物理删除路径；Profile 的业务软删除、停用和 attachment tombstone 仍由服务端处理；
- 付费操作使用持久 `operation_id`/lease，未知结果禁止重试，退出前权威清理，清理失败阻止
  升级/重启；
- 数据出口 TCP、UDP、DNS、反向 tunnel 生命周期和无默认网络 fallback；
- host health 独立于 Modem session，按 `run_id/revision` 发布缓存快照；health probe 不得顺带访问
  硬件、触发恢复或改变设备状态；health 只能上报聚合 inventory，不携带 IMEI、ICCID、EID、
  reader name/path 或其他设备/卡片标识；
- 安装/升级/回滚、包 manifest、日志轮转、诊断和权限错误的 fail-closed 语义。
- 服务端 package digest/协议版本门禁必须覆盖 Linux 发布包；旧版或未知包只能发布其已证明能力，
  不得因平台是 Linux 绕过 call/media/paid-lease 合约检查。

平台差异必须显式保留，至少包括：

| 领域 | Linux 目标机制 | 等价要求 |
| --- | --- | --- |
| Host 生命周期 | 前台 core；active systemd 生成 unit，否则输出通用拉起脚本 | 只有已 claim function 的 service/runtime 拥有硬件；外部维护方式由用户选择 |
| Modem 控制 | ModemManager D-Bus 优先，安全独占后才 direct AT | 能力探测和 per-function owner，不按型号猜测 |
| PC/SC | pcsc-lite/libccid + 现有 supervisor | 多 reader 独立 worker 与远程 VPCD |
| 数据 | MM/QMI/MBIM/PPP Provider + 每 Modem 隔离域 | TCP/UDP/DNS 可用且宿主其他应用不可借用 |
| 音频 | 同物理 USB parent 的 ALSA/PipeWire/UAC | 不选默认声卡，必须全双工实测 |
| 权限 | Unix socket ACL、uid/gid/group、可用时 polkit、最小 capabilities | 日常操作不要求反复 sudo，低权限不能替换 helper |
| 安装 | 发行版/固件包或离线包 + manifest + 原子升级/回滚 | 支持 glibc/musl 与发布矩阵架构；付费操作或清理不确定时禁止替换运行时 |

宿主部署只保留两条路径，不代替用户管理无 systemd 系统：

| 运行环境 | 安装器行为 | 部署结果 |
| --- | --- | --- |
| active systemd | systemd unit | 安装、启用、启动并回读验证 |
| systemd 未运行或不存在 | foreground runtime + 通用拉起脚本 | 文件部署完成并打印脚本路径/命令；用户自行接入循环、crond、`rc.local`、init.d 等 |

允许公共状态通过版本化字段如实表达：`host_mode=systemd|manual`、
`autostart_state=configured|external-unverified`、`supervision=systemd|unverified`、`session_scope=machine`、
`isolation_backend=linux-netns`（名称待评审）、`approval_state`。
其他业务差异必须来自真实能力，不得由页面按 `linux` 猜测。

## 7. 数据隔离与所有权的强制前置评审

Linux 允许比 Windows/macOS 更直接地使用 netns/cgroup/nftables，但“可以配置”不等于已经安全。
编码前必须独立验证并固定以下方案：

- 每个 Modem 的数据 interface、路由、DNS、代理 worker 和 session 是否能进入独立 network namespace；
- ModemManager/NetworkManager 控制面如何继续管理位于隔离域的数据面，且不会将默认路由/DNS
  泄漏回 root namespace；
- reverse tunnel 的 TCP/UDP/DNS 必须在对应 namespace 或等价的已证明隔离域内创建，不能仅靠
  `bind(source_ip)`；
- Agent 的管理 WSS 必须走独立的宿主管理网络，不能依赖它自己提供的蜂窝出口，否则数据故障会
  递归切断控制和清理通道；
- 普通宿主进程、Docker 容器、EasyTier、ZeroTier、Tailscale、浏览器和显式接口绑定均不能使用
  蜂窝路径；
- Agent crash、worker crash、`kill -9`、systemd/外部维护者重新拉起、Modem 拔插、MM/NM/netifd restart
  后，namespace、
  veth、route、DNS、nftables rule、socket 和旧 session 均有界回收；
- 多 Modem 各有独立 namespace/context；一个 Modem 故障不停止全局 ModemManager、不删除另一台
  的路由或杀死共享进程；
- 需要特权的窄操作由最小 helper 或 service host 承担，业务 Agent 不开放任意 root 命令执行面；
- 无法证明隔离时只撤销 `cellular_data`/proxy，PC/SC、APDU、短信或通话的独立能力继续如实运行。

正式方案不能直接继承当前本机 Orchestrator 的全局 ModemManager 启停、pcscd 重启、udev trigger
或 `pkill` 行为；这些代码可以作为现状和恢复经验，但必须改造成 per-device、可逆、有界的 Provider
操作后才能进入远程 Agent。

## 8. 预定里程碑（全部暂缓，不得据此开始编码）

### M0 — 多轮前置只读评审

- [ ] 第一轮：事实审计。逐项核对本文件的“已存在/缺失/复用/保留”是否与当前源码、测试和实机
  证据一致，删除旧结论和已被覆盖的 TODO。
- [ ] 第二轮：职责审计。确认没有把 Control、Engine、Asterisk、线路/eSIM 业务和页面搬进 Agent，
  也没有复制 Windows/macOS 已有领域状态机。
- [ ] 第三轮：Linux 专项安全审计。确定 systemd/通用拉起脚本、权限、ModemManager ownership、多 Modem、
  netns 数据隔离、故障回收和当前本机模式兼容方案。
- [ ] 第四轮：可测试性与迁移审计。明确 seam、兼容 wrapper、feature flag、回滚点、自动化矩阵和
  不发送收费操作的前置验证路径。
- [ ] 只有多轮评审均明确 `PASS` 且维护者将本文件状态改为“允许实施”后，才能进入 M1。

### M1 — 纯接口与契约整理

- [ ] 固定跨平台 `Attachment`、`PhysicalIdentity`、`FunctionOwnership`、`ProviderCapabilities`、
  `ProviderStatus`、`FunctionPlan` 和 generation 语义，不引入设备型号表。
- [ ] 为 Windows/macOS 当前实现建立 contract-conformance baseline，先证明抽象没有改变现有行为。
- [ ] 建立 Linux fake D-Bus/ubus、udev/mdev/hotplug、sysfs、PCSC 和 netns fixtures；纯接口阶段
  不得控制真实硬件或系统服务。
- [ ] 为网关本机 Orchestrator 与独立 Linux Agent 定义同一套可原子争用、崩溃可回收的 per-function
  owner claim；安装级 Agent lease 不能代替跨进程/跨模式设备仲裁。

### M2 — Linux 统一宿主、拉起脚本与 PC/SC

- [ ] 实现不依赖 init 的 machine-scoped foreground core。安装器只有在确认 systemd 正在作为当前
  Service Manager 运行时才生成 unit；不得只因存在 `systemctl` 或目录就猜测 systemd 可用。
- [ ] systemd 不可用时仍完成二进制、配置、权限和本地控制部署，并打印一个幂等拉起脚本的路径、
  单次运行命令和必要环境。脚本只负责启动单个 foreground runtime，不内置循环、cron、
  daemonize 或特定 init 逻辑；用户自行选择如何维护，安装器不编辑 crontab、`rc.local` 或 init.d。
- [ ] 拉起脚本只能引用 owner-only 配置文件；Token、pin、secret 不得进入 argv、crontab、日志或
  其他可被非 owner 读取的文件。外部重复拉起仍由安装级 lease 和 per-function claim 安全拒绝。
- [ ] 所有模式的付费操作都使用可跨进程/重启保留的 durable marker；新 runtime 发现未解除 marker
  时必须先查询 Modem 权威状态并完成挂断/清理或进入 quarantine，拒绝新的付费操作和升级，不能
  因外部循环或 crond 重启而自动重放短信、拨号或接听。
- [ ] 项目不识别或管理用户选择的循环、crond、`rc.local`、init.d 等外部维护方式；因此 manual host
  默认报告 `supervision=unverified`。只有外部维护与 Modem 权威挂断能满足经评审的有界清理条件时
  才发布 `modem_call_signalling/modem_call_audio`，否则仅这两项 fail-closed；PC/SC、eSIM/eUICC、
  Modem 的其他独立能力以及服务端 VoWiFi Engine/Asterisk 不受该宿主门禁影响。
- [ ] 实现安装级 lease、受 ACL 保护的本地控制、配置/Token、无 journald 依赖的日志、健康、安装、
  升级、回滚和卸载恢复；正式 service/runtime 是其已 claim attachments/functions 的唯一 Agent owner，
  CLI/GUI 只经本地控制访问，与 Orchestrator 的跨模式仲裁按 M1/M3 的原子 per-function claim 执行。
- [ ] OpenWrt/嵌入式构建按发布矩阵验证 CPU 架构、musl/BusyBox、可写持久目录、时钟/证书、熵源、
  USB hotplug、内核功能和资源上限；缺少 MM/NM/D-Bus、PC/SC、音频或 netns 时只关闭对应 capability，
  其余远程 Agent、安全、身份、控制和升级契约不能另起简化实现。
- [ ] 将现有 PC/SC supervisor 接入 Linux `ManagedAgentRuntime`，完成多 reader、同型号 reader、
  eUICC/USIM、插拔/换卡/空卡和服务重启验收。
- [ ] Linux reader identity 接入现有 sysfs 稳定端口证据；同名/无序列号 reader 重排时不得串卡，
  容量耗尽必须明确返回 unavailable/SlotFull，不覆盖 live endpoint。
- [ ] 证明旧 Go/Python Card Agent 与统一 service 互斥，迁移失败可恢复，不出现双 owner。

### M3 — Linux Modem Provider

- [ ] 抽取并复用 Linux discovery、MM object 关联、CSIM/logical-channel、identity 与状态归一化；
  旧网关本机路径通过兼容 wrapper 继续使用同一核心。
- [ ] 存在且受支持时优先复用 ModemManager D-Bus Provider；OpenWrt/精简系统可在确认 MM 不存在或
  不拥有目标 function 且取得独占 lease 后，选择已验收的 ubus/netifd、QMI/MBIM 或 Direct AT
  Provider。不得为一台设备停止全局 ModemManager，也不得把“缺少 MM”当成跳过 owner claim 的理由。
- [ ] 本机 Orchestrator 与独立 Agent 必须使用同一原子 function claim：同机管理不同设备允许并存，
  争用同一 tty/MM modem/PCSC function 必须 fail-closed，并覆盖检查到占用后立即变化的 TOCTOU。
- [ ] 完成至少两台并发 Modem、重插重编号、同型号、换卡、无 SIM、PIN、eUICC、MM restart、
  serial fallback 和 logical-channel 清理矩阵。

### M4 — Modem SMS、蜂窝 Call 与 Audio

- [ ] 复用公共 Modem SMS/Call/付费 lease 合约，将 Linux MM 或已验收 direct Provider 的硬件调用适配到
  同一结果语义；未知结果不重试。
- [ ] 先完成无费用的列表、状态、来电事件和音频枚举，再分别经人工授权执行一次短信发送、呼出、
  挂断、来电接听、DTMF 和双向音频。
- [ ] 音频必须按同一 physical Modem identity 关联，不按 ALSA card index、名称或默认设备猜测。

### M5 — 严格蜂窝数据出口

- [ ] 通过统一 LinuxDataProvider 复用系统 Profile 管理：常规 Linux 优先 NetworkManager/
  ModemManager D-Bus，OpenWrt 使用已验收的 netifd/ubus 或受控 direct backend；三者必须提供相同的
  list/save、自定义 APN、auth、secret 不回传、漫游允许状态持久化与重启恢复语义。禁止漫游时
  必须关闭对应 data+proxy 且不影响 PC/SC、APDU、短信和通话的独立能力。
- [ ] 在独立 PoC 中先证明 per-Modem namespace、TCP、UDP、DNS、普通应用不可借用、父子死亡回收、
  MM/NM/netifd restart 和无默认网络 fallback；固件缺少所需 netns/nftables 内核能力时 data capability
  必须 fail-closed，不能回退为污染宿主默认网络；PoC 未通过前不接入 Agent 产品链路。
- [ ] PoC PASS 后接入现有 reverse tunnel/服务端稳定 ICCID 出口，不复制 SOCKS/WSS/出口状态机。
- [ ] 验证多 Modem 多出口并发、MTU、IPv4/IPv6、DNS、UDP 生命周期、半开连接、断线和流量计数。

### M6 — 发布、兼容与迁移

- [ ] 提供受支持发行版/固件、init、libc、CPU 架构矩阵、固定依赖、许可证、SBOM、原生或离线包、
  manifest、签名/校验、原子安装升级回滚和版本兼容检查；不要求操作者手工拼装 Python 环境。
- [ ] 当前网关本机 Linux 硬件模式完整回归；新 Agent 模式必须显式启用，不在升级时自动接管设备。
- [ ] 实测本机 Orchestrator 与 Linux Agent 在同机分别拥有不同设备，以及对同一 function 的原子
  冲突拒绝、owner crash 回收和即时回滚；禁止仅靠启动前扫描避免双 owner。
- [ ] 从旧 Card Agent 或本机模式迁移前检测活动线路、付费操作、APDU、data session 和 owner；
  无法确认空闲时拒绝迁移。
- [ ] 升级、回滚和旧 Agent 迁移必须保留并校验 `agent_id`、配置、Token、TOFU pin 以及未解除的
  paid-operation lease/marker；无法可靠迁移时 fail-closed，不生成新身份继续上线。

### M7 — 实现后独立复审

- [ ] 第一轮只读复审实际 diff：检查重复实现、全局副作用、权限扩张、错误 fallback、身份串线、
  付费重试、secret/log 泄漏及 Windows/macOS/本机 Linux 回归。
- [ ] 修正后由另一轮评审重新从当前源码和测试开始，不把上一轮评论机械复用为事实。
- [ ] 复审必须删除误报：已由公共 Core 覆盖的场景不得要求再实现；已被后续修改替代的旧路径不得
  继续报告；测试 mock PASS 不得冒充实机 PASS。
- [ ] 只有最终复审 `PASS` 后才构建发布候选，并对发布包重新执行 Linux 实机 E2E、升级/回滚和
  Windows/macOS contract regression；发布包门禁后再进行最终一次只读复核。

## 9. 必须自动化和实机覆盖的矩阵

- Host：active systemd 与无 systemd 的通用拉起脚本；后者分别由测试循环、crond 或其他外部方式
  重复调用，但项目不管理这些外部工具。覆盖冷启动、重启、崩溃、`kill -9`、重复启动、CLI/GUI、
  升级、回滚、卸载、磁盘满、无 journald 日志轮转，以及外部维护状态不可验证时的如实状态；
- Reader：零/一/多个、同型号、无序列号、空 reader、USIM/eUICC、换卡、换 USB 口、pcscd restart；
- Modem：AT/QMI/MBIM、4G/5G、同型号两台、重编号、无 SIM、PIN/PUK、普通 USIM、Modem 内 eUICC、
  CFUN/飞行模式、MM/NM restart、固件差异和不支持能力；
- 并发：reader + 多 Modem、APDU + data、SMS + data、call + data、call + audio、分别插拔与故障；
- 网络：TCP/UDP/DNS、IPv4/IPv6、MTU、长连接、UDP idle、半开、漫游、无信号、数据限速、服务端断开；
- 隔离：root namespace、普通用户、Docker、Tailscale、EasyTier、ZeroTier、显式接口绑定均无法借用；
- 身份：USB path/port/object/slot 改变不丢业务；IMEI 不冒充 ICCID/EID；重复 live ICCID/EID 冲突
  fail-closed；旧 generation 和迟到事件不能回写；
- 业务：状态读取无副作用；Modem 短信、蜂窝拨号/接听、Profile 写入仅人工授权一次且不自动重试；
- 未受管退出：在 dialing、ringing、active 和 cleanup-quarantine 各阶段覆盖 shell/launcher 消失、
  SIGHUP、`kill -9`、重复启动和重启；验证 durable marker、拒绝新付费操作、权威挂断或通话能力
  fail-closed，且不错误阻断已证明独立安全的非通话能力；
- 三端契约：同一 fixtures 对 Windows、macOS、Linux 执行，除声明的平台字段外结果语义一致；
- 兼容：现有网关本机 Linux 硬件流程、纯远程 Card Agent、Windows Agent、macOS Agent 均不回归。

每一项必须记录“代码测试”“发布包测试”“真实硬件测试”中的证据等级。缺失证据必须写成
`not_verified` 或 `unsupported`，不能用“理论支持”“已发现设备”或另一平台结果代替。

## 10. 评审方法：排除误报与错误修改

每轮评审必须先建立当前事实快照，再检查问题，禁止从旧评论直接复制结论：

1. 精确定位当前代码路径、调用者、平台分支、feature flag 和测试；
2. 判断场景是否已经由公共 Core、Provider 或后续兼容 wrapper 覆盖；
3. 区分静态能力、运行时 readiness、真实操作成功和发布门禁四种证据；
4. 对问题给出可复现输入、预期、实际结果及最小影响范围；没有证据只列为调查项；
5. 修复只作用于最小 seam，并同时验证当前 Linux 本机模式和 Windows/macOS 公共契约；
6. 对删除/迁移旧代码执行调用图、搜索和回放，确认没有仍在使用的安装或恢复路径；
7. 对权限、网络、付费操作和设备持有执行失败注入，不能只审查正常路径；
8. 修正后重新审查最终 diff，关闭已覆盖评论，避免把历史缺陷重复计入发布阻塞项。

评审输出只能使用：`PASS`、`NEEDS CHANGES` 或 `BLOCKED BY EVIDENCE`。设计可行、单测通过或某台
设备成功均不能自动升级为 `PASS`。

## 11. 三端统一设备描述的后置条件

统一 Device Catalog 必须在 Linux Agent M3–M5 完成、至少两个设备族/协议在三端形成实际证据后
另立 TODO。届时只统一：

- 设备族匹配：VID/PID、USB composition、ATI/固件分支及可靠的能力探针；
- 协议能力：CSIM/CGLA、SMS、Call、Audio、PPP/QMI/MBIM/MBN、飞行模式；
- 厂商 quirks：初始化、超时、logical channel、CFUN、USB audio、固件缺陷；
- 验证矩阵：平台、架构、驱动/系统版本、固件和每项能力证据。

操作系统 binding 仍分别保留：Linux sysfs/udev/DBus/tty/netns，Windows PnP/COM/MBN/WFP，
macOS libusb/IORegistry/CoreAudio/private data backend。配置只能选择已经实现并验收的 Provider，
不能用数据描述替代驱动、协议或安全所有权。

## 12. 当前结论

Linux 远程统一 Agent 是合理的下一阶段方向：它可以把现有 Linux 网关的大量硬件兼容性与已经形成
的 Windows/macOS 远程 Agent 特性及待完成契约合并到同一产品契约中，也会为未来三端 Device
Catalog 提供真实基础。但当前只能保留设计记录，不能开始实施。正确顺序是：多轮前置评审 → 抽取公共 seam 且保持
旧路径兼容 → Linux Service Host/PCSC → Modem → Modem SMS/Call/Audio → 严格数据隔离 → 发布迁移 → 多轮实现后
复审 → 最终才统一三端设备描述。
