# TODO — 以最小适配层复用远程 4G/5G Modem

## 目标

把 Agent 后面的 4G/5G Modem 接入现有系统，使服务端可以像使用本地 ModemManager 一样：

- 按 SIM 身份借用蜂窝数据网络；
- 收发短信；
- 在浏览器中拨号、接听、挂断和使用双向语音；
- 以后允许网络电话应用接入服务端并复用同一套通话能力。

本功能不是新的远程设备管理平台。不得复制现有短信、通话、线路、消息历史、代理库、
Asterisk 或 WebRTC 实现；只增加硬件发现、远程命令和媒体接入所必需的薄适配层。

## 平台专项实施基线

- Windows、Linux 与服务端的共同领域目标仍以本文件为准。
- 当前 VoWiFi/来电/macOS Agent 恢复工作的逐项状态、评审、实施、复审、部署与实机证据，
  以 [TODO_CURRENT_RECOVERY.md](TODO_CURRENT_RECOVERY.md) 为唯一执行任务板；
  `TODO_ACTIVE_RECOVERY.md` 仅保留历史，不执行其中旧的下一步。继续工作或会话压缩后恢复时
  必须先读该任务板；不得把已闭环事项重新实现，也不得把“已实施、待复审/待部署”误报为完成。
- macOS 的 CLI/GUI 生命周期、Modem/PCSC Provider、宿主流量隔离、现成依赖选型和实机验收，
  以 [TODO_MACOS_AGENT.md](TODO_MACOS_AGENT.md) 为唯一实施基线；本文件中与其冲突的旧调研结论
  视为已被该专项 TODO 取代。
- 未来 Linux 远程统一 Agent 的复用边界、三端等价契约、实施顺序与复审门禁记录在
  [TODO_LINUX_AGENT.md](TODO_LINUX_AGENT.md)。该专项当前**暂时不实施，仅留作记录**；未经维护者
  明确启动授权，不得据此修改现有 Linux 本机编排或 Agent。

## 当前完成边界（2026-08-19）

- [x] 当前 Windows EC20 主场景已完成：稳定 ICCID/IMEI attachment、4G 数据启停、APN Profile、
      数据漫游、宿主流量隔离、按 SIM 暴露的反向 SOCKS5 TCP/UDP 出口、断线重连与页面状态。
- [x] Windows 通用 Agent 单进程同时管理 Modem 和所有外接 PC/SC/eSIM 读卡器：两类 Provider
      独立热插拔、独立重连。已实机验证 EC20 保持在线时，Windows WinSCard 读卡器自动分配动态
      VPCD 槽位并可传输 APDU；当前测试卡被正确识别为普通 USIM，而非错误伪装成 eUICC。
      2026-08-21 修复同名读卡器拔除后旧 worker 仍存活、重插不重建桥接的问题：连续两个成功发现
      周期确认缺失后才停止对应 worker，避免单次 Windows 枚举抖动；重插复用稳定 reader identity
      和原 VPCD 槽位。仅拔卡时清空旧 ATR，并在后续 ATR/APDU 请求重新连接。Windows 实机部署后
      Agent 重新读到 ATR，网关确认 `card inserted` 回到原 slot 1。
- [x] 蜂窝短信主路径已完成：列表/历史读取、远程发送领域接口、MBN 运行时不可用时切换到独占 AT
      function，并在 R08 实机取得 `+CMGS / OK` 与接收方确认。收费操作不自动重试。
- [x] 远程通话**信令**已完成拨号、状态、挂断和 DTMF 领域接口；实机只拨一次授权号码，从 dialing
      进入 alerting 后成功挂断。CLCC 只统计 `mode=0` 语音记录。
- [x] Windows EC20 浏览器呼出双向语音已完成实机验收：UAC/WASAPI -> 每通话媒体 WSS ->
      AudioSocket -> Asterisk -> WebRTC，网页扬声器与麦克风均正常。来电使用同一媒体链路的
      prepare/ring/answer 门禁已经实现；Agent 已在实机保留 `CLCC` 来电方向、状态和号码，浏览器
      来电界面已人工验收。实际接听及来电双向语音仍待一次人工验收。
- [ ] 仍未达到“所有平台可直接交付”：Windows 一次提权安装器/低权限守卫服务、持久 URC companion、
      飞行模式最终状态验收、短信页视觉验收、Linux/macOS Provider 和拔插/崩溃/换卡矩阵仍待完成。

因此不能把整个 TODO 概括为“只剩远程拨号”。当前 Windows EC20 的数据、短信和浏览器呼出语音
主路径已经完成；主要缺口是来电接听/双向语音验收及产品化/跨平台收尾。

## 工程规则：先研究，后实现

- [ ] **硬验收优先级**：远程蜂窝数据出口和短信收发必须实现；语音可以延期，但 Provider、设备
      所有权和事务恢复模型必须预留拨号、来电、挂断及音频 transport，不能为短信做一次性架构。
- [ ] **Windows 本机先行门槛**：任何 Windows 能力必须先在完全排除 Web、服务端和 Agent RPC 的
      本机脚本/成熟工具中走通真实硬件，再实现最薄桥接。未在本机成功的能力不得靠继续修改页面、
      API 或状态映射来“修复”，也不得标为完成。

- [ ] 每项新增 Modem 能力在编码前必须留下简短设计记录，至少包含：参考实现或官方协议链接、
      准备复用的状态机/接口语义、平台差异、许可证和不直接复用的原因。不得只凭单台设备的
      日志为 EC20、某个 COM 口或某个操作系统版本添加业务层特判。
- [ ] 优先借鉴成熟项目的“能力接口 + 平台/协议 Provider + 厂商小型覆盖层”结构：
      [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 作为能力与插件模型参考；
      Windows 使用官方 [Mobile Broadband Win32 API](https://learn.microsoft.com/en-us/windows/win32/mbn/mbn-start-page)
      和 [Windows SMS API](https://learn.microsoft.com/en-us/windows-hardware/drivers/mobilebroadband/developing-sms-apps)；
      Linux 优先复用 ModemManager/libmbim/libqmi；Quectel 等厂商文档只用于设备初始化、音频和
      固件差异，不替代通用 Provider。
- [ ] GitHub 项目只用于借鉴接口、状态机和已知兼容性；采用代码前必须核对许可证。不得把 GPL
      实现复制进不兼容模块，也不得因为某个开源项目支持某设备就假设本机驱动暴露了相同能力。
- [ ] 原生 API 可用时，不把本地化 CLI 文本解析作为权威状态源。CLI 只能用于诊断或无原生绑定时
      的受控兼容层，并必须使用结构化返回码、超时和可回放测试覆盖。
- [ ] Provider 只依据系统枚举出的协议与能力选择，不依据产品名：Windows MBIM/MBN、Linux
      ModemManager（内部可为 MBIM/QMI/AT）、macOS 系统能力或受控厂商适配、最后才是通用 AT。
      通用 AT Provider 只能独占选中的 AT function；操作系统网络管理器占用另一个网络 function
      并不自动冲突，但必须通过同设备并发验收和统一协调者管理。
- [ ] 每个物理 Modem 只有一个协调者，但 USB 组合设备按 function 分配唯一 Owner：同一个网络
      function、AT/Modem COM 口或 NMEA/UAC 音频 function 绝不能被两个进程并发打开；不同 function
      可以在通过实机并发验收后分别交给系统数据栈、独立 Modem 服务和媒体桥。`system_managed`
      不再等同于“禁止所有 AT”；禁止的是同一 function 争用和没有协调/恢复规则的隐式半接管。
- [ ] 协调者必须声明设备的并发能力。支持并发的设备直接并行数据与短信/语音；不能并发的设备使用
      可回滚的独占事务：快照数据 Profile、代理与隔离 → 暂停数据 → 执行一次短信或通话会话 →
      恢复原数据、隔离和代理。任何失败或进程退出都必须恢复；付费操作绝不自动重放。该策略位于
      本机 Modem 服务，不得泄漏为服务端的 EC20/Windows 特判。
- [ ] 优先采用 `system_managed` 保持通用安装和驱动兼容；允许设备和平台验证通过后选择
      `agent_managed`，以获得统一的数据、信令和流量隔离。直管模式必须具备权限预检、占用检测、
      原配置快照、失败回滚和卸载恢复，不能通过永久改驱动绑定或全局停用系统蜂窝服务实现。
- [ ] “unsupported” 只能来自已选 Provider 明确报告的能力缺失；单个 AT 命令失败、端口忙或一种
      后备路径失败只能记为该 Provider/transport 失败，不能据此把正常卡片或读卡器判为不支持。
- [ ] 每个 Provider 必须先通过同一组领域合约、状态机回放和断线/重连测试，再进行有限次数的实机
      数据、短信和付费通话验证。短信发送和拨号测试禁止自动重试。

### 架构纠偏：复用成熟控制面（2026-08-18）

- [ ] 停止在 MDD 业务层继续扩写自制的 MBN/QMI/MBIM/AT 状态机。通用的是领域合约、能力语义、
      Agent 通信和恢复规则，不是假设 Windows、Linux、macOS 能共享同一个底层 Modem 实现。
- [ ] Linux 生产实现直接复用 ModemManager D-Bus；不再解析 `mmcli` 文本或复制其设备插件。
- [ ] 把 Gammu/SMSD 作为 `GenericAtSmsProvider` 的首选现成后端进行兼容性验证，而不是继续维护
      自制 PDU/短信存储状态机。它只在能够独占 AT 控制口时启用；作为 GPLv2 独立进程集成，采用前
      必须完成分发与许可证审查，不能链接或复制进许可证不兼容的 MDD 模块。
- [ ] Windows `system_managed` 只发布经过实机验收的能力。MBN/RMNET 可以独占网络 function，独立
      Modem 服务可以独占另一个 AT function，但必须由同一协调者按 PnP parent/IMEI 绑定、串行化
      状态改变并通过“数据在线时短信/通话”并发测试；不得让 MDD Agent 与 Gammu 同时打开 COM 口。
      Windows SMS 应用 API 需要设备授权、元数据和用户同意，不能被当作无人值守服务必然可用的
      通用实现。
- [ ] macOS 没有可验证的系统蜂窝控制面时，只允许专项 TODO 验收通过的原始 USB/独占 AT/私有
      数据 Provider，或明确 `unsupported`；不为每个 USB Modem 编写业务层特判。
- [ ] 只有无法按独立 USB function 安全分工的设备才进入 `agent_managed` 整机模式：先把整个 Modem
      从宿主网络管理器安全释放，再交给一个成熟控制面独占。没有原配置快照、回滚和拔插恢复前，
      该模式不得进入生产，也不得在页面显示为可用；它不是当前 Windows EC20 的首选路径。
- [ ] 在确实需要该权限模型的平台，安装器一次性申请管理员权限并安装两个进程：日常低权限 Agent
      负责发现、状态和 WSS；最小特权守卫只负责设备 lease、隔离和恢复。macOS 首版若经专项 TODO
      的“无宿主网络 function”资格门禁通过，不为架构对称额外引入常驻守卫。运行期 IPC 必须带访问控制。
- [ ] 发布包不得假定用户手工安装 Python、Gammu、ModemManager 或厂商工具；每个平台的安装器应
      探测受支持后端及版本，能合法捆绑的固定版本随包提供，系统后端则做明确的版本/能力预检。

### 现成后端调研与选型记录（2026-08-18）

- [x] [Sigmo](https://github.com/damonto/sigmo) 是目前最接近“独立 Modem 服务”的可复用实现：
      MIT、单个 Go 二进制、版本化 `/api/v1`，公开版直接管理 QMI/MBIM，已提供 Modem/SIM、SMS、
      USSD、网络扫描与注册、飞行模式、频段、数据连接、eSIM，以及绑定蜂窝接口的 HTTP/SOCKS5
      （含 UDP）出口。第一轮 PoC 优先把它作为独立进程运行，由 MDD 只写小型 API Adapter；不得
      把其页面、消息数据库或用户体系并入 MDD。
- [ ] Sigmo 进入正式依赖前必须补齐固定提交、可重复构建、API 合约夹具和断线恢复验证。其底层
      [wwan-go](https://github.com/damonto/wwan-go) 也是 MIT/pure Go，协议层覆盖 QMI、MBIM、AT、
      UICC，但目前无稳定版本标签；直接 QMI/MBIM transport 仅支持 Linux，Windows 只支持 AT
      串口。因此不得把“Go 可交叉编译”误写成 Windows/macOS 已支持直管蜂窝数据。
- [ ] Sigmo 仓库中的 IMS、VoLTE/VoWiFi 通话及 WebRTC 路由不能作为当前可交付依赖：相关 Pro
      代码虽声明 MIT，但构建依赖私有 `ims-go` 等模块。公开版先只承担已可构建和验收的数据、
      SMS、USSD、SIM/eSIM 与网络控制；通话继续走下述 ModemManager/Asterisk 能力链。
- [x] [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 仍是稳定生产基线：
      D-Bus 统一 AT/QMI/MBIM，通用 QMI Provider 已实现 Voice service 的拨号、接听、挂断、DTMF
      与来电状态；是否可用由模块实际暴露的 QMI Voice service 决定，而不是按 EC20 型号猜测。
      音频是独立能力，不能因信令成功就宣告浏览器语音可用。
- [ ] Linux 通话 PoC 先验证
      [asterisk-chan-modemmanager](https://github.com/koreapyj/asterisk-chan-modemmanager) 作为独立 GPLv2
      Asterisk 模块：它复用 ModemManager Voice 并支持 USB Audio/ALSA、SMS 和 Quectel 初始化命令，
      有机会替代自制信令/媒体胶水。采用前需核对目标模块是否枚举 USB Audio，及 GPL 分发边界；
      EC20 的 NMEA PCM 仍可能需要一个小型厂商媒体 transport，不能污染通用 CallBackend。
- [x] [ModemDeck](https://github.com/human-agent65535/ModemDeck) 已证明“Linux 独占模块 +
      ModemManager + 薄 HTTP Agent + Asterisk/WebRTC + 独立数据面”架构可运行，但许可证为 PolyForm
      Noncommercial。它只作为 Provider/lease/恢复/媒体分层的参考，未经单独授权不得复制、链接或
      作为商业分发依赖。
- [x] 未找到成熟、活跃且跨平台的 ModemManager REST/gRPC 包装层；低活跃度包装项目不能替代
      Sigmo 或 MDD 自己的薄 Adapter。MDD Adapter 只转换现有领域小接口与后端 API/D-Bus，不实现
      QMI/MBIM、PDU、网络注册或 Modem 插件。
- [x] WSL2/usbipd-win 方案经用户否决：运行时、安装和维护成本过重，不进入 MDD 主线；不得安装、
      升级或启用目标机现有 WSL，也不得要求用户维护 Linux VM。其调研只证明 USB 整机独占可解决
      Windows MBN 争用，不作为产品依赖或 PoC 前置条件。
- [ ] Windows 首选轻量组合已收敛为：保留系统 MBN/RMNET 独占 MI_04 高速数据 function；一个独立
      `ManagedModemService` 独占 MI_02 AT function，并复用 Gammu/libGammu 的短信、USSD、来电事件
      和通话信令状态机；媒体桥首选同 USB container 的 UAC/WASAPI，失败时才租用 MI_01 NMEA PCM。
      PnP parent/IMEI 只做硬件 attachment 绑定，线路、出口和业务仍稳定映射 ICCID。
- [ ] Windows RAS/PPP 只保留为无 MBN/MBIM 数据 function 设备的兼容 Provider，不作为当前 EC20
      主线：目标机 `Quectel USB Modem #2` 的系统属性报告 `MaxBaudRateToSerialPort=115200`，未证明
      实际吞吐不受限前不能用它替代已实测 150 Mbps 链路的 RMNET 网卡。
- [ ] 优先安装一个独立服务并暴露版本化 localhost IPC/API；MDD 只写
      `ManagedModemServiceProvider`。若 Gammu 没有现成的统一服务 API，只允许写一个持有单一
      libGammu state machine 的小型 GPL companion daemon，不能让业务 Agent 反复 shell-out 或
      与 SMSD 争抢串口。不得自行实现 PPP、PDU、QMI/MBIM 或 Windows 虚拟网卡协议。
- [x] [OpenGSMGateway](https://github.com/demogorgonz/OpenGSMGateway) 虽标称 Windows/Linux Call/SMS
      API，但实现主要是 PowerShell 串口脚本、固定 sleep 后挂断和粗粒度文件锁，没有持久 URC/
      call state machine；只作为“Windows 串口路径可行”的参考，不作为生产依赖。
- [x] 不采用 EC20 `RNDIS/ECM + AT` 作为通用主线：USB mode 会写入模块配置，Windows 驱动切换可能
      丢失 COM 口；EC20 RNDIS 数据会话还可能只能通过 `CFUN` 断开并连带破坏短信/通话。该方案只
      保留为明确型号、可恢复且用户主动选择的兼容模式。
- [ ] macOS 没有 WSL/usbipd-win 等价的成熟系统路径；Lima 的通用 USB passthrough 仍未成为稳定
      能力。第一版保持同一 MDD Adapter/领域合约，但仅启用经过验证的独占 AT/CCID 后端；数据和
      通话缺失时明确 `unsupported`。不得为追求表面上的三平台一致而内嵌一套新的 QMI/MBIM 栈。
- [x] 2026-08-19 已在 macOS 15.2 (Apple Silicon) 取得 EC20 的真实枚举基线，此前"USB 树完全没有
      设备"是 Type-C 直连协议握手问题，经转接降级到 USB 2.0 后解决，与驱动和 Agent 代码无关。
      实测 composition 为 `2c7c:0125`，`Device Speed=2`(480 Mb/s)，`Current Required=500 mA`
      且可用余量恰为 500 mA（长时间 RF 需外部供电或有源 Hub）。5 个 function 全为
      `bInterfaceClass=255` 厂商自定义（DIAG/NMEA/AT/MODEM/QMI-RMNET），因此 macOS 不创建任何
      `/dev/cu.*`，也没有蜂窝网卡：AT 通道必须由原始 USB transport 独占 claim。数据面不得为了
      方便先切成会被 macOS 使用的 CDC ECM；首个安全 PoC 按专项 TODO 验证私有 PPP 数据面。
- [x] macOS 通话音频不是阻塞项：同一 EC20 还暴露 `bInterfaceClass=1` USB Audio Class function，
      macOS 已自动匹配 `AppleUSBAudioDevice`，`SPAudioDataType` 中出现两个 Quectel/USB 端点
      （capture + render）。与 Windows UAC 路径同源，因此 macOS 侧可复用 CoreAudio 而无需厂商
      驱动；设备选择仍必须按同一 USB container 匹配，禁止按名称或音频索引猜测。

#### 首轮 PoC 判定门槛

- [ ] 当前优先级固定为 Windows EC20 主流程；Linux 只作为已知可用的成熟后端基线，不继续占用
      当前实机验证时间。Windows 数据、SMS、通话信令与媒体的 Owner 方案走通后，才开始 macOS
      独立调研和实现；不得以 Linux 构建通过替代 Windows 完成度。
- [ ] 只在获得切换授权后改动 EC20 Windows 主机：先快照 MBN Profile、PnP/USB identity、当前
      数据与隔离状态；轻量直管失败时必须一键恢复现有 Windows MBN 数据出口，不改变持久 USB mode。
- [ ] 组合 Provider 以同一物理模块完成 ICCID/IMEI、注册状态、APN、漫游、数据启停、绑定模块接口的
      SOCKS5 TCP+UDP，以及断开/重连。普通 Windows 应用不得使用该蜂窝接口；Agent/后端退出时
      出口必须立即关闭。
- [ ] SMS 先做无费用的列表/接收和状态回放；仅在后端同时保持数据连接且明确报告可发送后，人工
      授权一次发给本机号码，禁止自动重试。若本机验证证明该驱动不能并发，则允许由协调者受控暂停
      数据完成一次发送并自动恢复，但必须先单独验证“暂停后的本机发送成功”和“恢复后出口可用”。
      通话先验证信令能力与音频端点，再按相同所有权/恢复规则仅拨打一次授权号码并立即挂断。
- [ ] PoC 通过后，MDD 只保留 `ManagedModemServiceProvider` Adapter；Windows/Linux 的业务 API、
      页面、ICCID 稳定入口、短信历史和 Asterisk/WebRTC 路径完全复用。PoC 未通过则保留当前已验收
      的 Windows MBN 数据出口，不再继续扩写 MBN 短信/通话补丁。

#### Windows 轻量组合 PoC 证据（2026-08-18，无短信/通话费用）

- [x] 目标机 USB 组合设备按 function 枚举为 MI_04 `Quectel Wireless Ethernet Adapter #2`、MI_03
      `Quectel USB Modem #2`、MI_02 `Quectel USB AT Port`、MI_01 `Quectel USB NMEA Port` 和 MI_00
      DM；PnP parent 与 IMEI 用于把这些临时 attachment 组合为一个 Modem，生产代码不得硬编码
      COM14/15/16 或 MI 编号。
- [x] 未安装 WSL、未停 Agent、未断 MBN、未改驱动或 USB mode；便携版 Gammu 1.43.3 独占当前
      空闲的 COM16，成功识别 EC20F、固件、IMEI、IMSI，枚举 SIM/ME 短信文件夹并完成存储扫描，
      `monitor 1` 成功启用 incoming SMS/CB/call/USSD 通知。退出后 COM 口立即释放。
- [x] 上述 Gammu 操作期间，MI_04 始终保持 LTE Connected、漫游 460-11 China Telecom、APN
      `ctnet`；Gammu 同时读取到 GPRS attached、信号和网络状态。因此当前设备已证明“MBN 高速
      数据 function + 独立 AT/Modem function”基础并发成立，不能再以物理模块为粒度一刀切禁止。
- [x] 当前 Windows MBN helper 的 `GetSmsConfiguration` 返回 `E_PENDING (0x8000000A)`；系统
      `netsh mbn show smsconfig` 同时能读到 PDU 格式、SMSC `+85362101201` 和存储容量，但明确显示
      `Ready to send SMS: No`。这证明当前失败是 MBN miniport/SMS control path 的限制或异步状态，
      不是 SIM 不支持短信；主线改用独立 AT 后端，不继续把 MBN SMS 补丁当成唯一出路。
- [x] 2026-08-19 已在停止 Agent、排除服务端后用三条 Windows 本机路径复核：系统 `netsh mbn`、
      独立 MBN COM helper、独立串口脚本/Gammu。SIM ready，短信配置可读，SMSC 保持卡片提供的
      `+85362101201`（香港号码使用澳门 SMSC 不构成错误）；AT 显示 CS 未注册、PS/LTE 漫游注册、
      IMS 未注册。关键可重复差异是：断开 `ctnet` 数据后 Windows 显示 `Ready to send SMS: Yes`，
      Agent 恢复数据连接后变为 `No`。因此下一门槛是在 Windows 本机断数据状态完成一次人工授权
      发送，再验证数据/Profile/隔离/代理恢复；此前不得继续扩写远端桥接。
- [x] 进一步官方资料核对已确认当前实机是硬件支持短信/语音、但固件基线与目标网络承载不匹配：
      当前 `EC20CEHDLGR06A07M1G` 自动激活 `OpenMkt-Commercial-CT`，却保持 IMS `0,0`；移远官方社区
      针对同一完整固件号明确回复“R06 基线不支持电信 VoLTE，需要重新采购 R08 基线版本”。这与
      CS 未注册、LTE 漫游注册、MBN/Gammu 两条发送路径均在网络完成阶段失败一致。第三方博客也只
      能证明 HDLG 型号硬件表含 SMS/Voice，并明确提醒旧固件不支持纯 VoLTE 短信。不得把该限制
      归因于 `+85362101201` SMSC（香港卡使用澳门短信中心可以正常），也不得用换 CLI、重写 PDU、
      强刷跨基线固件或继续修改服务端掩盖。
- [x] 2026-08-19 在用户明确授权可报废当前调试硬件后，已先保存该模块独有的 QCN/EFS、原 R06
      固件和完整只读基线，再人工使用 QFlash 把精确 HDLG 包升级为 `EC20CEHDLGR08A06M1G`；工具
      显示 PASS，IMEI/ICCID/RF 均保留。升级后 `CREG=5`、`CEREG=5`，自动激活
      `VoLTE_OPNMKT_CT`，IMS/VoLTE 已开启。该一次性实验成功不改变生产安全规则：未知 SKU、无本机
      QCN、无精确回退包或无人值守场景仍禁止跨基线自动刷写。
- [x] 已记录固件升级安全流程和用户引导规则：自动读取版本并提示兼容性是允许的；同分支同基线
      升级必须具备官方包、发布说明、SHA-256、匹配 QFlash、原固件和本机 QCN/EFS 备份，并由用户
      明确确认停机后执行。R06→R08 因 CEFS/校准差异必须拒绝自动刷写并提示更换硬件/厂商服务。
- [x] 已建立 Windows 离线批量升级包和仓库维护脚本：套件总清单覆盖 740 个工具、固件、文档与
      脚本文件并通过 SHA-256/Quectel 签名校验；逐台脚本实测取得 986 个默认 NV、53 个 SIM NV、
      1184 个 EFS 文件的 XQCN，完整备份约 25 分钟。脚本按 IMEI 隔离备份、拒绝缺失/过小/校验不符
      的 XQCN，输出 QFlash DM 端口/460800/精确 Firehose 路径，并在 DIAG 后执行受控 CFUN 复位、
      等待 Agent 与真实蜂窝数据恢复。工具/固件/QCN 不进入 Git；流程维护于
      `agent/windows/ec20-upgrade/`。
- [x] 已实现签名固件兼容矩阵及设备页提示：`control/app/firmware_matrix.py` 只做检测，不下载、
      不解包、不刷写。硬件分支与基线一律从 `AT+GMR` 修订串推导，不使用 `ATI` 的营销型号名
      （同一 `EC20F` 覆盖多个不兼容分支）。Agent 新增 `firmware` 字段（Provider snapshot 优先，
      否则 `AT+GMR`；解析失败保持空串而不猜测），经 hello 进入 registry 并出现在设备页。
      `EC20CEHDLGR08A06M1G` 为已验收基线；`EC20CEHDLGR06A13M1G` 记录为已知缺陷基线并声明
      `impact=sms/ims`，设备页直接说明“该基线未启用 IMS/VoLTE，LTE-only 附着下 MO 短信会以
      未指明错误被拒绝”。“引导升级”在结构上要求三个条件同时成立：同基线、矩阵记录了官方包
      SHA-256、且不涉及改写校准/CEFS；当前 R06→R08 跨基线且未记录包摘要，因此只能显示
      “需人工停机升级或更换硬件”并指向 `agent/windows/ec20-upgrade/README.md`。未收录分支或
      同分支未验收基线一律为 `unknown`，只要求人工核对，不给升级引导也不判定故障。
- [ ] 补录 `EC20CEHDLGR08A06M1G` 官方包 SHA-256（来自离线套件 `SHA256SUMS.txt`）到
      `Branch.target_package_sha256`。在补录前矩阵按设计无法显示任何“引导升级”；补录也只解锁
      同基线场景，跨基线仍必须人工。远程无人值守刷写在至少一种合格 Windows 模块完成升级、
      断电恢复、数据与短信实机验收前不实现。
- [x] 短信提交前置条件已可诊断，不再依赖复现：`sms_service_center` 之前只由 Windows MBN 上报
      且被服务端丢弃，现在通用 AT 路径也读取只读 `AT+CSCA?`（60 秒缓存、从不回写），服务端在
      `sms_diagnostics` 中同时暴露短信中心和 advisory。advisory 在 `sms_ready=True` 时也保留，
      因为 2026-08-18 的失败正是“驱动报告就绪但提交仍被拒绝”。提交失败信息附加当时的短信中心、
      bearer 和 CREG，把“error 350 / 未指明错误”变成可判断的前置条件缺失。香港卡使用澳门 SMSC
      属正常，矩阵与 advisory 都不据此判定故障。
- [x] APN 候选为空或有多个时不再返回死胡同文案：`_apn_guidance` 区分“系统 Profile 有多个”、
      “Modem 报告多个候选”和“完全没有可用 APN”三种情况，并附带 MCC/MNC 前缀（只取 IMSI 前 5 位，
      不回显完整 IMSI）。该文案只使用 Agent 已知值，连接路径不新增 AT 往返，避免重现按连接
      重复探测导致的握手超时。
- [ ] 未知 MCCMNC 的 APN 最终解法仍未验证：候选为空时是否可用“空 APN / 网络指派默认 APN”附着，
      必须先按 Windows 本机先行门槛在本机脚本（netsh mbn / MBN profile 无 AccessString）走通真实
      硬件，再决定是否放开 `_save_cellular_profile` 的非空 APN 校验。当前实现只负责把缺失说清楚。
- [x] APN 候选为空时优先补两条通用来源，不自建运营商表：
      1. 激活后读 `AT+CGCONTRDP`（3GPP TS 27.007）取网络实际指派的 APN 并回写为 profile，只读、
         无费用、与运营商无关，可覆盖“网络会指派默认 APN”的多数场景；
         已实现于 `ModemControl._modem_profile_candidates`，仅当 `CGDCONT` 无可用的非服务上下文时
         回退读取 `CGCONTRDP?`。
      2. 引入现成运营商 APN 数据库
         [mobile-broadband-provider-info](https://gitlab.gnome.org/GNOME/mobile-broadband-provider-info)
         作为候选来源。已核对许可证为 **CC-PDDC**（Creative Commons 公共领域奉献），
         NetworkManager/ModemManager 长期使用，属数据而非 GPL 代码，可安全随发布包分发。
         已随包置于 `agent/data/serviceproviders.xml`，`agent/apn_providers.py` 按 MCC/MNC 解析
         并过滤 `usage="internet"`，结果只加入 `suggested_profiles` / APN guidance，不作为自动创建
         profile 的依据。
      两者都只提供“候选”，最终 profile 仍由用户确认；不得据此自动改写已在用的 profile。
- [x] 2026-08-19 实机数据推翻“SMSC 为空即将失败”的假设，已按证据回退该判定：EC20 在 R08 上
      `sms_ready=true`、`sms_provider=auxiliary_at`、短信可发，但 Windows MBN 报告的
      `sms_service_center` 为空串——模块把短信中心维护在 MBN 接口之下。因此空 SMSC 只作为事实
      展示，不再产生 advisory（否则会把正常设备标成可疑）；`service_centre()` 改为平台值为空时
      回落到只读 `AT+CSCA?`，把“缺信息”补成真实值而不是发警告。失败时仍记录当时的短信中心。
- [ ] SMSC 仍缺一块，需要实机验证后再做：
      1. 蜂窝路径没有“显式设置 SMSC”的入口（VoWiFi/线路路径已有 EF_SMSP 读取与手工覆盖）。若要
         支持 `AT+CSCA=` 写入，必须先快照原值、仅由用户显式触发、可回滚，并单独验证写入是否
         持久化到 EF_SMSP；不得在自动恢复或心跳路径里写。
- [x] 按 ICCID 记录“最近一次成功提交时的 SMSC”，之后仅在该值发生变化时提示。这是唯一能区分
      “SMSC 缺失”与“SMSC 错误”的低成本手段，且不需要额外收费尝试。已实现于 `agent/sms_history.py`，
      在 `ModemCard.sms_send` 成功后写入 `~/.mdd-agent/sms_smsc.json`，状态页暴露
      `sms_service_center_changed` 与 advisory。
- [x] R08 实机 `AT+QPCMV=? -> (0,1),(0-2)`，支持 USB NMEA PCM、Debug UART 和 UAC。已用
      guarded compare-and-set 只把 `usbcfg` 最后的 UAC 位从 0 改为 1，其余 VID/PID 和 6 个 function
      位原样保留；配置会立即触发 USB 重枚举，因此实现不能把旧 COM handle 的预期写超时误判为
      配置失败，必须等待同一 USB container 重现并回读。Windows 随后使用系统 `usbaudio.sys`
      枚举 `AC Interface`、capture 和 render endpoint，三者与该 Modem 的 AT/NMEA/MBN function
      具有同一个 PnP ContainerId；设备选择必须按 container 映射，禁止按 `AC Interface` 名称或
      PortAudio 数字索引猜测。Agent 与 `ctnet` 数据在重枚举后已恢复并保持 Connected。
- [x] 无声卡/AudioSrv 不可用时的 NMEA fallback 生命周期已在同一 Windows 实机完成无通话 dry-run：
      快照 `QGPSCFG outport=usbnmea` → 临时 `none` → `QPCMV=1,0` → 独占 NMEA COM 并写入一帧
      1600-byte PCM → `QPCMV=0` → 恢复 `usbnmea`；最终 Agent 和 MBN 数据均恢复。真实实现必须把
      GNSS/NMEA 当作可恢复租约，任何异常、拔插或进程退出都执行幂等清理。
- [x] Windows UAC 运行时候选已做脱离项目的本机 PoC：`malgo/miniaudio` 生成约 4.8 MiB 单文件，
      只依赖 Windows 系统 DLL；按明确 WASAPI endpoint ID（不是默认声卡或名称）以
      8 kHz/S16/mono duplex 连续打开、读写、关闭 10/10 次。临时 SYSTEM 计划任务的非交互会话也
      成功打开同一 capture/render endpoint 并取得 16000 capture frames。因此客户机没有实体声卡
      不是阻塞；EC20 UAC 自身就是虚拟声卡。该结果尚不替代拔插恢复、AudioSrv 故障注入和真实接通
      上行验收，后两类失败必须自动切换到 NMEA provider 或明确 fail-closed。
- [ ] 双向蜂窝媒体最后门槛：一次有界外呼已通过 UAC 捕获 100800 帧非静音下行振铃 PCM
      （8 kHz/16-bit/mono），但对端处于 dialing → alerting 后未接听，因此没有注入上行测试音，
      不得据此声明 `call_audio=true`。取得一个会接听的测试端后只做一次短时双向验收，必须
      `ATH`、`QPCMV=0` 并恢复 Agent/数据；失败和超时均禁止自动重拨。官方 `AT+QAUDLOOP`
      在本固件只回环模拟 MIC/SPEAKER 路径；空闲 UAC 注入 997 Hz 后 capture 仍为静音，不能用它
      代替真实接通验收或伪造上行成功。

#### Gammu CLI Adapter 实施与实机结果（2026-08-18）

- [x] 已实现进程外 `GammuCliProvider` 和组合 Provider：Windows MBN 继续拥有数据 function，Gammu
      只探测未被占用的独立 Modem/AT 端口，并且必须读取到与 MBN 相同的 IMEI 才能组合。端口号、
      型号和 USB MI 编号均未写死；当前实机自动得到 COM14（Agent）+ COM16（Gammu）。
- [x] Gammu 以官方 1.43.3 独立 GPLv2 可执行程序旁挂，MDD 不链接或复制 libGammu；Windows 目标机
      同目录部署了 `gammu.exe` 和许可证文本，未安装 WSL、未切换驱动、未改变持久 USB mode。
      用户自行安装、PATH、同目录和 `--gammu/--gammu-port` 四种发现方式已写入安装文档。
- [x] 已处理 Windows 中文环境的 UTF-8/GBK 输出差异、opaque SMS 存储 ID、SMS 列表短缓存、
      Gammu 不实现 `getdisplaystatus` 时的操作状态回退，以及 OS 数据心跳不再被 Gammu 串口锁饿死。
      付费操作超时统一返回 `unknown/retryable=false`，服务端不跨 transport 或断线自动重试。
- [x] 当时自动化回归为 `589 passed, 25 subtests passed`。Windows 计划任务模式实机验证：registry online、
      MBN/RMNET 数据 connected、漫游开、APN profile `MDD-2529-ctnet`、严格隔离 ready、反向 SOCKS
      ready；页面数据/漫游/SMS/Call 均为 on。APN 候选完整返回 `ctnet/ctwap` 的 CID、PDP type、
      auth 和 username 字段；短信列表/历史读取成功。
- [x] 按授权只拨打 `22333322` 一次，Gammu 返回 dialing，随后成功执行 cancelcall 并在服务端记录
      ended；没有自动或人工重拨。该结果只证明通话信令，不证明浏览器双向音频。
- [x] R08 升级后 Windows 本机由持久 `AuxiliaryAtProvider` 所用的标准 AT 路径完成一次授权短信
      发送，网络返回 `+CMGS: 28 / OK`，接收方确认收到；目标是另一测试号码，不是模块本机号码，
      因此不能写成“自发自收”。SMSC 保持 SIM 提供的 `+85362101201`，未修改。组合 Provider 现会
      在 Windows MBN 运行时明确返回 `0x8000000A` 时自动使用已独占的 AT 信令后端，不绑定型号、
      COM 号、国家或运营商，也不会自动重试收费操作。
- [x] 已完成无费用根因诊断：`CREG=2`（CS 未注册），`CGREG/CEREG=5`（PS/LTE 漫游已注册），
      原配置 `CGSMS=1` 却强制 MO SMS 走 CS；按 Quectel/3GPP 定义改为 `CGSMS=2`（PS 优先、CS
      回退）并回读成功，但正确号码的后续实机发送仍为 error 350，证明 `CGSMS=2` 只是必要的
      通用修正而不是本机根治。不得再以此宣告发送已修复。
- [x] 已完成第二轮无费用 IMS 实机探测：在 LTE 漫游已恢复为 `CGREG/CEREG=5` 后，将
      `QCFG="ims"` 强制为 1 并连续观察约 90 秒，18 次均为 `1,0`，旧固件也不支持
      `QIMSREG` 查询；最终自动恢复 `QCFG="ims"=0,0` 并重启 Agent。当前固件
      `EC20CEHDLGR06A07M1G` 的活动 MBN 没有可用 IMS 会话，同时 CS 未注册，这与 error 350
      完全一致。下一步必须取得与 **HDLG** 硬件分支严格匹配、含运营商 VoLTE/IMS MBN 的移远
      官方固件/配置及校验值后另行受控升级；禁止混刷 HCL/FDK/FILG 或来源不明镜像。
- [x] 2026-08-20 在 R08 实机定位并修复“页面仍显示 Windows 漫游缓存、但短信/通话突然全部
      不可用”的独立 UICC 故障：两个 AT function 均返回 `QSIMSTAT=0`、`CPIN=SIM failure`、
      `CREG/CEREG=0`；停止 WWAN AutoConfig 后结果不变，排除 Windows 独占。一次标准
      `CFUN=0` → `CFUN=1` 后恢复 `QSIMSTAT=1`、`CPIN=READY`、正确 ICCID 以及
      `CREG/CEREG=5`，服务端实测 `call.actual=on`、`sms.actual=on`。Agent 已加入与数据、短信、
      语音解耦的通用 UICC maintainer：只在已有 IMEI+ICCID、无注册且明确 SIM failure 时尝试一次，
      跨服务重启锁存，成功后解锁；PIN/PUK、未知状态、空槽和持续硬件故障均不重启循环。网页数据
      漫游仍保持关闭；移远 `roaming/voicecall=0` 经官方手册核对含义为允许漫游语音，未被改写。
- [x] UICC 恢复补齐启动时序：Windows MBN 枚举前若已确认 IMEI 的 AT function 读到
      `CFUN=0`，只补做一次持久化保护的 `CFUN=1`；直接 CPIN 失败且插卡状态为 0 时不再被过期
      的注册缓存掩盖。`CFUN=4` 等人工无线电状态不覆盖。实测所有 AT function 已沉默时 Windows
      PnP restart/disable-enable 均不能替代 USB 冷断电，因此这种明确状态只提示重新插拔，不宣称
      软重启可以修复；重新枚举后仍走同一通用自检和期望状态恢复。
- [x] 成功 attachment 后在 Agent 本地持久化 `IMEI -> last successful ICCID`，只作为 MBN 丢卡时
      一次有限 UICC 恢复的授权证据，不作为当前在线身份上报。旧版升级迁移仅接受 Windows 中唯一
      的合法订阅 ICCID；多个历史 SIM 时 fail-closed，不按 COM、运营商或营销型号猜测。
- [x] 通用 Agent 已把“信令 Provider 已安装”和“当前网络允许发送”解耦：每 60 秒最多一次只读
      承载探测；LTE 已注册但 CS 与 IMS 均不可用时仍保留短信读取能力，把发送标为 unavailable，
      并在调用 Gammu 前拒绝提交，避免继续产生收费尝试。检测不绑定 EC20、COM 口、Windows、
      国家或运营商；CS/IMS 恢复后自动重新判定。最新 R08 实机恢复后为 `sms_ready=true`、
      `call_ready=true`；数据漫游策略仍关闭，因此数据/出口单独保持不可用，不影响短信和通话。
- [ ] 短信页心跳抖动已做两层修复：设备能力心跳不再触发“换线路”清空会话、收件人和草稿；
      重复 WebSocket 数据保持原数组引用；页面再以实际可见字段做 memo，诊断时间戳/心跳变化不再
      重绘 SIM 下拉框与全部气泡。最新静态包 `index-CbLZks4D.js` 已部署，仍需用户刷新后确认视觉
      验收；确认前不能把该项标为完成。
- [ ] 飞行模式切换仍有状态语义问题：开启操作会断开数据，但该次页面样本仍显示
      `flight.desired=true / actual=off`；关闭后 radio、数据、代理和隔离均已恢复。必须用 Windows MBN
      radio postcondition 修正 actual 映射后再宣告该开关通过，不能仅凭 RPC 返回成功。
- [ ] 当前 CLI Adapter 是验证稳定领域边界的过渡实现，不等于下文的长期 companion 完成。生产版
      仍需一个持有单一 libGammu state machine 的独立 GPL companion，通过本地 ACL IPC 同时处理
      命令与 URC；解决 SMS error 350、来电状态持续跟踪和崩溃恢复后才能勾选该项。

#### Windows EC20 数据 / SIM 通道所有权结论（2026-08-18）

- [x] 当前全量自动化回归：`603 passed, 25 subtests passed`。
- [x] 通用 `AuxiliaryAtProvider` 已用标准 `CUAD/CCHO/CGLA/CCHC` 完成只读 SIM 探测；Windows 数据
      关闭且 Modem 干净启动时，实机可完整读取 ICCID、IMSI、SMSC 与 SPN。实现不绑定 EC20、COM
      编号、运营商、AID 或当前 SIM。
- [x] 同机并发验收推翻了“不同 USB function 必然可以安全并发”的假设：当前 Windows 驱动/固件下，
      MBN 数据已连接时打开 USIM logical channel 会导致 ADF.USIM 选择失败，并使已激活的数据上下文
      立即变为 DEACTIVATED。这是实机能力边界，不在服务端或页面加入型号特判。
- [x] Agent 已改为先恢复持久化的 radio/roaming/data 意图，再决定是否开放 VPCD；Windows 蜂窝数据
      活跃期间暂停 APDU/VPCD，设备页继续使用 MBN 身份和缓存 SIM 信息。数据关闭后才允许 APDU
      重新获得 SIM 通道，避免页面轮询破坏数据出口。
- [x] 最新实机验证已保持 MBN profile `MDD-2529-ctnet` 在线、严格 WFP 隔离就绪、反向 SOCKS5
      稳定可用；VPCD 在数据活跃后保持断开，没有再次抢占 SIM 通道。
- [x] ePDG 路由加入公共地址校验：运营商 ePDG DNS 返回 `127.0.0.1` 时明确标记不可用，且不得把
      loopback/private/non-global 地址写入线路路由。当前 SIM 因无公共 ePDG，不能把 VoWiFi 短信
      或通话标为可用。
- [x] 静态适配器能力与运行时网络能力分离：Gammu/ATD 存在只表示“可执行该命令”，只有 CS 注册或
      可用 IMS 承载实际就绪时页面才显示蜂窝 SMS/通话可用；`sms.send`/`call.dial` 在付费提交前
      fail-closed，状态轮询不自动重试。
- [x] 当前香港 SIM 的蜂窝短信发送和远程通话信令已完成实机验收：R08 下 CS/LTE 漫游均注册，
      数据保持在线时 AT 短信提交成功；只拨打 `22333322` 一次，CLCC 从 dialing 进入 alerting 后
      `ATH / OK` 挂断。服务端现显示 SMS/Call 均为 on，短信会话列表可读，通话状态最终为 idle。
      CLCC 解析只接受标准 `mode=0` 语音记录，不再把 `mode=1` 数据上下文误报为活动通话。浏览器
      双向音频仍未完成，必须等独立 UAC/NMEA 媒体桥验收后才能声明可用。

### Provider 边界

业务层继续使用下文的小接口，不引入庞大的全能 Backend；平台 Provider 只负责为这些接口提供
实现并隐藏 MBIM、QMI、D-Bus、COM、AT 端口和系统 Profile 等细节：

- `WindowsMbnProvider`：用 Win32 MBN/COM 获取状态、Profile、连接、短信和设备服务；仅在桌面
  MBN 能力确实不足且权限允许时，增加隔离的 `Windows.Devices.Sms` broker。
- `LinuxModemManagerProvider`：通过 ModemManager D-Bus 提供数据、短信和通话能力，底层协议由
  ModemManager 插件选择。
- `MacOSProvider`：只声明系统或已验证厂商接口真实提供的能力，不仿造 Windows/Linux 成功状态。
- `GenericAtProvider`：作为操作系统未接管设备时的回退；Quectel/其他厂商适配只覆盖 quirks、
  初始化或音频映射，不复制整套短信、通话和数据业务。
- `DirectMbimQmiProvider`：可选的 Agent 直管实现；仅在能够证明设备已从系统管理器安全释放时
  声明能力，并复用与其他 Provider 相同的小接口和领域状态，不产生 EC20 专属业务路径。

### Windows Provider 设计记录（2026-08-18）

- 参考 [ModemManager](https://github.com/linux-mobile-broadband/ModemManager) 的能力接口与插件覆盖层；
  Windows 使用微软 `IMbnInterface/IMbnConnection/IMbnSms` 的异步 request-id/completion 语义。
  原生 COM helper 由 MIT 许可的 Microsoft CsWin32 生成声明，不复制 GPL 实现。
- 当前旧 Quectel miniport 对 legacy `IMbnConnection.Connect` 返回成功回调却不激活；兼容层只用
  系统 `netsh mbn connect` 触发已有 Profile，忽略退出码和本地化文本，最终结果仍由原生 MBN 确认。
- 单一 Owner 为 `system_managed/windows_mbn`：AT 口只用于硬件发现/存活，不执行 SIM、APDU、短信
  或通话业务。Provider 有有界枚举窗口，避免 COM 口先出现时 Generic AT 抢占控制面。
- WFP 只允许打包 Agent、无任意联网功能的 MBN helper 和系统控制程序；普通进程仍被阻断。长操作
  在串行 worker 执行，WebSocket 主循环继续处理 ping，避免操作超时制造 attachment 重连。

### Windows Direct Voice 研究记录（2026-08-18）

- Quectel 官方《EC2x&EG9x Voice Over USB and UAC Application Note》明确覆盖 EC20 R2.0/R2.1：
  `AT+QPCMV` 只负责启停媒体路径，Voice-over-USB 通过 NMEA 串口传输 8 kHz/16-bit/mono PCM，
  UAC 通过 USB 声卡；拨号、接听和挂断仍使用标准 `ATD/ATA/ATH`。该文档只作为厂商媒体
  transport 覆盖层依据，不能把 EC20 型号判断写进短信、通话或页面业务层。
- 当前实机 MBN 能力为 `MBN_VOICE_CLASS_NO_VOICE`，所以 `system_managed/windows_mbn` 本身不能
  宣告通话；但受控启用模块固件已有的 UAC function 后，Windows 已通过系统 `usbaudio.sys` 枚举
  capture/render endpoint，且与 AT/NMEA/MBN function 共享 PnP ContainerId。组合 Provider 因而可
  在不断开 MBN 的情况下用独立 AT function 提供信令、用 UAC（首选）或 NMEA（兜底）提供 PCM。
  控制信令、媒体枚举和真实双向音频仍是三个独立能力，不能互相代替验收。
- 实机进一步证明 MI_04 的非持久化 PnP lease 本身可由原生看门狗安全持有和释放，父进程崩溃后
  也能恢复 ProblemCode 0；但当前旧 Quectel miniport 在 MI_04 恢复后不会重新注册 MBN，必须对
  整个组合设备执行受控模块重启才恢复。因此“单 function 自动切换”已从生产 Agent 撤回，不能
  仅凭 AT 拨号成功就标记 Direct Provider 完成。当前主线不再切换 MI_04，而是让其持续由 MBN
  持有；整机 `agent_managed` 仅为无法按 function 分工的后备方案。

## 当前验证基线（2026-08-20，单项证据，不代表主流程完成）

- [x] 本地自动化：`693 passed, 25 subtests passed`。这只证明领域合约和回归测试通过，不代表
      Windows 驱动实机主流程通过；实机完成标准必须单独满足。
- [x] Windows 通用打包 Agent 自动发现 EC20，控制 WSS 正常；Windows MBN 可读取真实
      ICCID/IMSI/IMEI。设备当前是 Quectel RMNET/NDIS 模式，业务层不依赖型号。
- [x] 旧验证版在 Windows MBN 接管时关闭了 AT SIM/APDU、短信和通话回退，避免当时的双进程串口
      争用；该限制现已由“组合设备按 function 唯一 Owner”设计取代，不能继续作为正式能力上限。
- [x] Windows WFP 守卫完成 IPv4/IPv6 ALE 过滤器安装和回读；仅放行打包后的 MDD Agent 身份，
      明确拒绝 `python.exe`，失败时不启用蜂窝连接或 SOCKS。
- [x] 已做守卫进程故障注入：旧反向代理立即拒绝新连接，Agent 断开蜂窝数据并上报
      `proxy.ready=false`，网关撤销反向监听且同步清除嵌套状态；下一次 `cellular.ensure` 自动
      重建隔离、连接和 TCP/UDP 出口。
- [x] 网关持久化的稳定入口映射为 `ICCID -> local SOCKS port`，不含 Agent、Modem、接口或
      session；两次网关重启前后端口均为 `37177`，UDP DNS 分别成功，证明依赖无需跟随 attachment
      重写。离线时监听关闭，端口映射保留。
- [x] Windows Provider 仲裁改为 fail-closed：MBN 在 USB/驱动恢复期间暂时未枚举时最多等待
      60 秒并保持 unavailable，绝不自动降级为 Generic AT，也不会错误发布 `sim_apdu=true`。
      受控 `AT+CFUN=1,1` 恢复后当前状态为 `windows_mbn/system_managed`，最终 UDP DNS 为
      `3450 ms`，稳定入口仍为 `37177`。
- [x] 实机无蜂窝 Profile 场景曾验证：`cellular.ensure` 明确返回不可用，网卡保持断开，守卫撤销，
      MDD SOCKS 不监听；系统中已有的 `easytier-gost` 不会被误认为 MDD 出口。
- [x] 当前 Windows 已存在 `MDD-2529-ctnet` Mobile Broadband Profile；不得再把失败原因显示为
      “未配置 Profile”。
- [x] 已定位模块 SIM 栈卡死：AT 为 `QSIMSTAT 0,0 / QINISTAT 0`，MBN 操作为
      `E_MBN_SIM_NOT_INSERTED`；受控模块重启后恢复为 `CPIN READY / QSIMSTAT 0,1 / QINISTAT 7`。
      Agent 只发布 `MBN_READY_STATE_INITIALIZED` 的身份，不能把 Windows 缓存 ICCID 当在线卡。
- [x] 设备页 4G 标签提供最小蜂窝 Profile 管理：优先使用系统已有 Profile；为空时读取 Modem
      `AT+CGDCONT?` 候选，只有唯一且排除 IMS/SOS/MMS 的数据 APN 才自动建立系统 Profile，
      候选不唯一时才让用户选择或填写名称、APN、认证、用户名和密码。网关不得持久化或回显密码。
- [x] Agent 复用各平台系统网络栈实现 Profile 查询/保存：Windows 使用 Mobile Broadband，
      Linux 使用 NetworkManager；macOS 未提供可验证的通用移动宽带配置接口时明确显示不支持，
      不能假装保存成功。Profile 保存到插卡主机的系统配置，不建立网关侧第二套账户数据库。
- [x] 网关/Agent 重启后的 desired-state 收敛对 attachment 启动竞态执行有界重试；只重放幂等的
      radio/roaming/data 操作，绝不重放短信或拨号。部署后重启控制容器，EC20 反向 SOCKS 在首次
      状态检查已自动恢复为 `proxy.ready=true`。运行期间若 Windows 数据承载或 Agent SOCKS 后续
      丢失，网关也会按 ICCID 检测 live/desired 偏差并以 5~60 秒有界退避自动恢复；Agent 明确返回
      `ok=false` 时不能误判为成功。Windows/Modem 状态采集不得阻塞控制 WebSocket 收包，否则
      `tunnel.open` 会排队到过期；状态采集使用单槽后台 worker，控制、RPC 和数据隧道保持解耦。
      Windows MBN 枚举偶发返回空列表时不得作为撤销条件；安全权威是原生 WFP 守卫和绑定蜂窝源
      IP 的 socket。守卫退出仍在 0.5 秒内立即撤销，源地址绑定保证连接无法回退到默认网卡。
      Windows `Get-NetIPAddress` 出现 miniport/CIM 通用错误时使用独立的 `netsh interface ipv4`
      地址库回退；状态采集只上报无法确认，不能直接关闭 SOCKS/守卫，实际恢复统一交给幂等
      desired-state 对账。隔离建立后接口名以守卫持有的已验证 attachment 为准，不在每次状态采样
      时重新依赖易抖动的 MBN 发现；已建立 SOCKS 的状态同样复用创建时验证并绑定的 source IP，
      避免打包进程内的 Windows 查询失败反复破坏正常数据承载。
      WFP 动态 sublayer 按 Agent 父进程 PID 派生唯一 GUID；新旧 Agent/守卫在升级或任务重启期间
      短暂重叠时，旧动态 session 关闭只能清理自己的过滤器，不得连带删除新守卫的数据隔离规则。
      2026-08-19 实机部署后未操作页面即恢复为 data/proxy ready，连续观测超过一分钟未抖动；
      网关经稳定 SOCKS 入口访问外部连通性地址返回 HTTP 200，证明真实数据面也已恢复。
- [x] EC20 已完成 Profile 重启保留、漫游 LTE 建链、WFP strict、反向 WSS SOCKS5 TCP/UDP 出网
      闭环；蜂窝 IP `10.192.156.66`，HTTPS 验证出口 `63.140.1.56`，UDP ASSOCIATE + DNS 实测
      `2332 ms`，普通 Windows `curl.exe` 强制绑定蜂窝 IP 被拒绝。APN 候选仍为完整可选配置并
      允许自定义。
- [x] Windows 统一 Agent 管理面已完成实机验收：SCM `MddAgent` 是唯一设备 owner；SSH CLI
      与无控制台 GUI 通过同一个有限长、版本化、拒绝远程客户端的 named pipe 读取同一
      `state_revision`，不会启动第二个 Agent。配置/日志/DPAPI 凭据统一到 ProgramData，安装器
      健康后才禁用旧计划任务，失败恢复；非管理员 SSH 安装明确返回 `elevation_required`。
      当前首版 SCM 仍以 LocalSystem 运行以兼容 MBN、COM、PC/SC 和 WFP helper；独立两轮评审
      均已通过。两台 Windows 实机已用同一份 7 文件 manifest 包交叉验证：一台只接 EC20，另一台
      同时接 EC20 与 PC/SC 读卡器；均完成事务安装、自动启动、配置脱敏、doctor/self-test、运行时
      reconnect、SCM restart、8 路并发 SSH/GUI 同源控制面以及 Windowed GUI 启动验证。重启后
      EC20 自动恢复 online 及短信、通话信令、音频硬件预检和蜂窝能力；第二台的读卡器也自动恢复
      原稳定 VPCD 槽位，
      APDU/卡身份读取成功。该卡实测是普通 USIM，因此 eSIM Profile 列表为空不是桥接故障。第二台
      即使系统 Event Log 被禁用，服务仍通过有界文件日志正常运行；旧计划任务已禁用，首次迁移保留
      原安装身份，服务以受保护的 Program Files 二进制运行。本次未产生短信或通话费用。
- [x] Windows GUI 已复用 MDD 品牌图标并增加原生通知区生命周期：关闭窗口默认隐藏到托盘，
      托盘可重新打开、重启服务或只退出 GUI，Explorer 重启后图标自动恢复；GUI 生命周期始终
      不影响 SCM 服务和设备 owner。
- [x] Windows 服务、macOS CLI/GUI 与 Linux 统一 Agent 增加独立的主机级健康通道：稳定身份只用
      `agent_id`，不得依赖 ICCID、Modem、读卡器或 VPCD 槽位。每 10 秒严格发送一帧；语义状态
      变化发送完整快照，不变只发送心跳。服务端心跳只更新内存时间，不写盘、不广播，也不触发
      设备列表刷新；仅状态变化和 `fresh/delayed/offline` 阈值转换通知页面。Windows/macOS 使用
      运行时缓存做低成本采集，健康线程禁止 AT、PC/SC、音频探测和任何状态修复；Linux 当前只
      报告“在线、健康采集未实现”，旧 Agent 明确显示“此版本未上报”，两者都不标成设备异常。
      页面位于“诊断 → Agent 主机”，与“网关主机”和 SIM/4G/短信/通话业务状态分开；一台 Agent
      管理多个模块和读卡器时仍只显示一张主机卡。付费通话 cleanup quarantine 期间 reporter 必须
      继续心跳，不能提前关闭信令、释放安装租约或影响单次强制挂断保护。
      自研 WebSocket transport 通过协商后的 `agent.health.received` 每 10 秒进入一次有界接收，
      处理服务端 protocol Ping/Pong；只有当前 session 已应用的 seq/revision 才有回执。旧 Agent
      不会收到回执，新 Agent 遇到未声明能力的旧服务端也不会等待回执。节拍以 monotonic deadline
      计算，回执延迟不会累计漂移或补发突发帧。
- [x] VoWiFi 自动恢复不再把已确认的运营商/IMS 侧故障当作 Docker 故障快速重建：第六次报告后
      `REPORT/PACE/BACK_OFF` 都进入一小时低频探测，探测启动失败也保持该节奏；线路恢复 `OK` 或
      用户明确启停/保存配置时才清理对应账本。销毁前按精确容器 generation 采集诊断，并通过
      Asterisk graceful maintenance 拒绝新呼叫、保留进行中通话；只有 Asterisk 确认退出才删除
      Engine，否则撤销维护并保留容器。Web SIP INVITE、蜂窝拨号/接听以及所有容器启停入口与
      自动恢复共享 per-line gate，BYE/CANCEL 和付费通话安全清理不受阻断。
- [ ] Agent 拓扑只作为现有注册表的只读投影分阶段实现，不新建第二套设备数据库：根节点为
      `agent_id`，进程代际为 `agent_id + run_id`，Modem attachment 继续使用硬件身份与 session，
      Reader attachment 增加 claim generation，卡片分别以 EID/ICCID/Profile ICCID 表达。主机健康
      只能显示父级可达性，并对幂等自动恢复作负向 veto；不得据此判定 SIM/读卡器/通话结束，
      不得触发拨号、接听、短信、eSIM 写入或删除业务状态。
- [ ] 拓扑投影前先补三项身份安全门禁：VPCD 慢扫描结果必须校验 claim generation；槽位记录拆分
      “历史缓存身份”与“当前已验证身份”；同一 EID/ICCID 同时出现在多个 live attachment 时明确标记
      conflict 并禁止有副作用的模糊寻址。完成这些门禁后再评估 Linux 统一 Agent 和跨主机槽位协商；
      不用 IP、读卡器名称或槽位号伪造全局硬件身份。
- [x] Windows Provider 已完成通话/音频能力枚举，当前 miniport 实报 `MBN_VOICE_CLASS_NO_VOICE`；
      这只表示 MBN 网络 function 不提供语音 API，不等同于物理 SIM/模块不支持。正式通话路径改由
      独占 MI_02 的 Modem 服务提供信令、独占 MI_01 的媒体桥提供 PCM，并验证与 MI_04 MBN 数据
      同时运行；不再要求先实现整机 Direct QMI/MBIM Provider。
- [x] 早期一次有界 `22333322` 出站媒体验证只从 dialing 进入 alerting，并捕获 100800 帧非静音
      下行振铃；该证据本身不足以声明 `call_audio=true`。后续浏览器呼出已实际完成双向语音与麦克风
      验证（见 7.4），所以当前 Agent 只在本次启动的 UAC/PCM 硬件预检通过时发布该能力；来电界面
      已验证、实际接听仍待人工验收，这是业务 E2E 缺口，不再与硬件媒体能力字段混为一谈。每次
      有界验证后均执行 `ATH`、`QPCMV=0` 并确认 Agent/MBN 数据恢复，禁止自动重拨。
- [x] 设备页把带 `cellular_data` 能力的远程 attachment 合并为 4G/5G Modem，不再把它
      仅显示成 `Virtual PCD` 读卡器；同一 ICCID 只能出现一个业务设备视图。
- [x] 设备页可以远程启停蜂窝数据、RF/飞行模式和“允许数据漫游”，并展示 Agent 实报的
      注册状态、运营商、信号、接口、数据连接和失败原因；开关状态不能由页面自行猜测。
- [ ] 远程系统蜂窝模块不再把 VoWiFi Instance 的 `STOPPED` 当作设备总体状态：设备页分别展示
      4G、漫游、蜂窝短信、蜂窝通话和 VoWiFi 能力；短信页在 VoWiFi 不可用而蜂窝短信可用时默认
      选择蜂窝通道。4G/漫游和 APN 曾单项验证，不能据此把 SMS/通话或整个设备标为可用。
- [ ] 数据开关关闭、禁止漫游且当前处于漫游、飞行模式开启、Agent/守卫掉线时，都必须关闭
      MDD SOCKS 和蜂窝数据连接，且不能影响同一 SIM 的 APDU/VoWiFi 管理通道。
- [ ] Windows 原生 MBN `sms.list` 已实机成功，但 SMS 主流程未完成。2026-08-18 两次人工发送均失败；
      Agent 现已保留真实 HRESULT。重启 WWAN 后 `Ready to send SMS` 短暂为 Yes，恢复数据连接后又
      变为 No，原生 `GetSmsConfiguration` 返回 `0x8000000A (E_PENDING)`。在成熟后端能够同时稳定
      持有所需能力之前，不再付费重试，也不得显示“蜂窝短信可用”。
- [ ] 当前 Windows MBN 网络 function 明确报告无通话能力，所以没有再次拨打 `22333322`；待
      `ManagedModemService` 在独立 AT function 实报 `call_signalling`、NMEA media lease dry-run
      通过后，才做一次拨号、确认状态并立即挂断，禁止重试。

## 非目标

- [ ] 不建立独立的远程 Modem 产品、页面或数据库体系。
- [ ] 不让浏览器或未来网络电话应用直接连接 Agent。
- [ ] 不在 Agent 内实现另一套软电话、用户权限、历史记录或路由策略。
- [ ] 第一版不提供自动备用出口、`follow_device`、多网关集群仲裁或通用设备编排平台。
- [ ] 不为尚未遇到的网络条件预先开发复杂隧道；只有网关确实无法访问 Agent 时才补充
      所需的 TCP+UDP 反向传输。

## 1. 最小架构

### 1.1 稳定身份

- [x] 统一以规范化 ICCID 解析 SIM 当前 attachment；物理 SIM 和当前激活的 eSIM Profile
      使用相同规则。ICCID 不替换现有业务记录主键：线路继续使用 `instance_id`，短信继续使用
      `message_id`，通话继续使用 `call_id`。
- [x] EID 只标识 eUICC，不能代替当前 Profile 的 ICCID；IMSI、号码、COM 口、IP 和槽位
      不能作为业务主键。
- [x] `agent_id + modem_id` 只定位硬件，`session_id` 只标识本次在线连接：
      `ICCID -> current attachment -> session`。
- [x] ICCID 必须由 Agent 从实际 Modem/SIM 读取，服务端不得依据用户输入或旧缓存认领在线状态。
- [x] 同一 ICCID 已有存活 attachment 时，新的 session 不得静默覆盖；该 SIM 暂时标记为冲突，
      任一旧 session 断开或超时后再重新认领。

### 1.2 运行时注册表

- [x] 服务端增加一个小型 `ModemRegistry`，记录在线 attachment、能力和最后心跳；attachment
      不写入线路或代理配置。
- [x] 注册表按 `ICCID + capability` 解析当前远程执行端，并保留现有本地 ModemManager 路径。
- [x] 每次连接生成不可复用的 session token；断线后的旧 session 事件和响应必须被拒绝。
- [x] 只使用简单、可配置的心跳超时、短暂离线宽限和重连退避，不引入分布式租约系统。

### 1.3 小接口而非庞大 Backend

- [ ] 保留现有 `cellular_sms.py` 和 `cellular_call.py` 作为业务入口，把直接执行 `mmcli` 的部分
      包装为本地实现，并增加返回相同领域结果的远程实现。
- [ ] 按能力拆分最小接口，避免所有设备被迫实现一个巨大的 `ModemBackend`：

      ```text
      CellularDataBackend: status, ensure, endpoint
      SmsBackend:          list, send, ack
      CallBackend:         dial, answer, hangup, status, dtmf
      CallAudioBackend:    open, close（仅声明音频能力的 Modem）
      ```

- [ ] 业务层只使用 `operation_id`、状态和领域错误，不依赖 D-Bus path、COM 口或 Agent 请求 ID；
      本地 `modem_path/sms_path/call_path` 只能保留在实现内部。
- [ ] 统一最小错误语义：`invalid`、`unavailable`、`failed`、`unknown`；`unknown` 表示操作可能已经
      到达 Modem，调用方不得自动重试可能重复计费或重复拨号的操作。
- [ ] Provider 可以组合实现多个小接口，但业务层不得感知 Provider 类型；能力声明来自当前
      attachment 的 Provider 实报，并携带不可用原因及是否可恢复。

### 1.4 本机 Modem 服务边界

- [ ] `ManagedModemService` 是无页面、无业务数据库、默认不监听 TCP 的独立 companion；Windows
      使用带 ACL 的 named pipe，其他平台使用权限受限的 Unix socket。协议为有版本、有限长的
      JSON request/response/event，MDD Agent 是唯一允许的客户端。
- [ ] companion 内每个 AT function 只有一个长期存活的 libGammu state machine，统一串行化
      identity、network、SMS、USSD、call 命令并持续处理 URC；禁止每个请求启动一次 `gammu.exe`，
      禁止同时运行会打开同一端口的 Gammu SMSD/终端/第二 Agent。
- [ ] companion 只暴露领域所需的 `probe/identity/network`、`sms.list/send/ack`、
      `call.dial/answer/hangup/status/dtmf` 和事件订阅；厂商媒体命令只能由受限 media provider
      调用，不提供任意远程 raw-AT API。
- [ ] Windows 安装器一次提权安装 companion/守卫服务、固定版本依赖和卸载恢复信息；日常 Agent
      与 Web 用户不需要管理员权限。companion 链接 libGammu 时作为独立 GPL 程序分发并附许可证、
      对应源码与构建说明；MDD 通过进程外 IPC 调用，不复制或链接 GPL 代码。
- [ ] 端口选择基于 PnP parent、IMEI 和无副作用能力探测；优先选择支持所需 URC 且未被系统数据栈
      占用的 AT function。COM 编号、USB interface 顺序、产品名和驱动显示名称都只是 attachment
      属性，拔插或换型号后允许变化。

## 2. Agent 通信

- [x] 继续使用已有 WSS 鉴权和 Agent 身份能力；Modem 控制消息与 APDU 数据在逻辑通道上隔离，
      互相失败时不得误改对方状态。
- [x] 控制消息使用小型版本化 JSON envelope，至少包含 `version`、`id`、`session_id`、`modem_id`、
      `method` 和 `params`；响应回传同一 `id`，事件包含 `event` 和 `modem_id`。
- [x] 第一版只支持版本 1；版本不兼容时明确拒绝，不能猜测执行。
- [x] 每个请求必须有超时和大小限制；改变 Modem 状态的命令按 Modem 串行执行，并与 APDU/AT
      访问使用同一个设备锁协调。
- [x] Agent 最小能力声明：`cellular_data`、`socks5_udp`、`sms`、`call_signalling`、`call_audio`。
- [ ] Agent 最小状态：IMEI、ICCID、注册状态、运营商、信号、蜂窝接口、数据连接以及各项能力
      是否可用和失败原因。
- [x] Agent 最小命令：

      ```text
      cellular.ensure
      sms.list, sms.send
      sms.ack
      call.dial, call.answer, call.hangup, call.status, call.dtmf
      audio.open, audio.close
      ```

- [x] `sms.send`、`call.dial` 等有外部副作用的请求不能因超时自动重发；同一请求 ID 只能查询或
      返回已知结果。
- [x] `sms.list` 不得在服务端确认持久化前删除远端短信；服务端完成去重和持久化后调用
      `sms.ack`，Agent 再删除或标记已消费。重复或旧 session 的 `sms.ack` 必须安全且幂等。

## 3. SIM 蜂窝数据出口

- [ ] 蜂窝接口启用后必须进入“仅 MDD 可用”的独占隔离：插入机器上的浏览器、更新器、
      EasyTier/组网软件以及其他进程，即使主动枚举或绑定该网卡，也不能使用这张 SIM 出网。
      单纯提高 metric、修改默认路由或让 SOCKS 绑定源地址都不算完成。
- [x] 隔离使用统一的 `CellularIsolationBackend` 合约；Windows 已实现 WFP 守卫。过滤身份必须是
      MDD Agent 的专用可执行文件或专用服务账户，不能笼统放行 `python.exe`、用户桌面会话或整台机器。
- [ ] Linux netns/nftables 仍待实现和实机验收。macOS 不再把 PF、路由 metric 或专用账户当作
      首选安全边界；按专项 TODO 优先保证蜂窝 IP 从未进入宿主网络栈。
- [x] Windows `cellular.ensure` 只有在隔离规则安装并回读验证成功后才能建立数据出口；Agent 崩溃、
      守卫退出、接口重枚举或规则丢失时立即停止 SOCKS 并断开数据连接。无法证明隔离时必须
      fail-closed，且页面显示“本机流量隔离未就绪”。
- [x] Agent 本地 SOCKS 和跨单向网络的反向 WSS 入口均提供 TCP 与 `UDP ASSOCIATE`，并强制
      绑定蜂窝源地址；DNS、TCP、UDP 不得静默改走默认网络。UDP 中继只接受建立 SOCKS 控制
      连接的客户端 IP，并在首个数据报后锁定源端口。
- [x] 服务端为每个 `sim:<normalized_iccid>` 保留稳定本地入口；线路和其他依赖只引用该入口，
      不引用 Agent 地址、Modem、端口或 session。
- [x] Agent 在线时，稳定入口转到当前 attachment 的 SOCKS5；Agent、Modem、数据连接或隧道
      不可用时，稳定入口拒绝流量并 fail-closed。
- [ ] `CellularDataBackend.ensure` 负责以幂等方式建立或恢复蜂窝数据连接；本地实现复用现有
      ModemManager，远程实现请求 Agent 使用平台蜂窝接口。`status` 只报告状态，`endpoint`
      只在连接和出口健康检查均通过后返回可用入口。
- [x] 部署已证明 Agent 经 Mihomo TUN 单向到达网关，网关无法反向访问 Agent；通用 WSS 反向
      通道已完成 SOCKS5 TCP CONNECT 和 UDP ASSOCIATE，分别通过 HTTPS 出口与 DNS 数据报
      实机验证，当前能力如实上报 `socks5_udp=true`。UDP NAT 生命周期由 sing-box TUN/SOCKS
      入站统一管理，Python WSS 层只跟随 SOCKS 控制连接做透明桥接，不实现第二套 UDP idle/cache。
      TUN 对端虚拟 DNS 地址由 sing-box 劫持，按国家出口使用独立 LRU 缓存，缓存未命中仍经对应
      出口查询；部署前重复查询曾维持 90~116 条数据 WSS，修复后连续采样只剩 1 条 Agent 控制
      WSS，缓存命中、真实 DNS 回包及缓存未命中的 UDP 出口均已实测。
- [x] 复用现有 SOCKS5 UDP 检测、sing-box 国家出口和线路绑定逻辑；只增加把
      `cellular_sim` profile 解析为稳定本地 SOCKS5 入口的适配。
- [x] 管理 Agent 的连接不得依赖该 Agent 自己提供的蜂窝出口，避免出口故障造成控制链路递归中断。
- [ ] 模块拔出或 Agent 掉线时保留 profile、国家出口分配和依赖；同一 ICCID 在任意 Agent
      重新出现且健康检查通过后自动恢复。
- [x] 第一版不自动回退网关或 Agent 默认网络，也不自动选择其他 SIM。

### 3.1 代理库与页面

- [x] 在现有“代理库 -> 添加代理”中增加用户可见类型“SIM 蜂窝数据”，不新增远程隧道配置页。
- [x] 通用 profile 继续包含现有 `profile_id` 和可编辑 `name`；该类型新增的持久化字段只有：

      ```json
      {
        "name": "澳门电信 2529",
        "type": "cellular_sim",
        "sim_iccid": "89852312388530152529"
      }
      ```

- [x] 用户只选择 SIM，不选择 Agent、Modem、COM 口、网卡、地址或 session。
- [ ] SIM 选择器显示号码（如已知）、运营商、ICCID 尾号和状态；允许保存已知但离线的 SIM。
- [ ] profile 状态统一派生自同一个服务端状态源：`在线`、`正在连接`、`离线`、`不可用`、`冲突`。
- [ ] 设备页、线路页和代理页不得各自推断一套状态；依赖出口的线路可跳转到对应 profile。
- [ ] 删除仍被国家出口或线路使用的 profile 前列出影响并确认；只删除代理配置，不删除 SIM、
      短信、通话或线路历史。

## 4. 远程短信

- [x] `/instances/{id}/sms/send`、`auto/vowifi/cellular` 选择、消息数据库、发送状态、WebSocket
      广播和现有页面保持不变。
- [x] 短信业务入口根据线路 ICCID 从 `ModemRegistry` 选择现有本地路径或远程执行端。
- [ ] Agent 上报新短信提示；服务端调用 `sms.list` 拉取并继续负责去重、Instance 归属、持久化和推送。
- [x] 服务端只有在短信完成去重和持久化后才调用 `sms.ack`；Agent 收到确认后才能删除或标记
      Modem 中对应短信，避免服务端断线造成消息丢失。
- [x] 发送超时且无法确认结果时返回 `unknown`，不得跨传输或跨 session 自动重发。
- [x] Agent 离线时返回 `unavailable`；同一 ICCID 恢复后扫描仍保存在 Modem 中的短信并按现有规则去重。

## 5. 浏览器拨号、接听与未来电话应用

浏览器拨号和接听是核心功能，不是后续可选项。所有客户端只连接服务端，由服务端统一控制
通话、权限、历史和媒体；Agent 永远只是 Modem 信令及音频硬件适配器。

### 5.1 服务端稳定边界

- [x] 保留现有通话记录、状态广播、Softphone 页面、Asterisk 和浏览器 WebRTC；扩展现有统一
      通话服务支持 `dial`、`answer`、`hangup`、`status` 和 `dtmf`。
- [x] 浏览器和未来网络电话应用使用同一套服务端鉴权、通话对象和状态机，不为浏览器写专属
      Modem 控制逻辑。
- [ ] 服务端通话对象使用稳定 `call_id`；Agent 的 AT/ModemManager call path 和请求 ID 仅保留
      在当前 attachment 内部。
- [ ] 未来电话应用可通过服务端受支持的 SIP/WebRTC 或实时控制接口接入；本次不实现独立电话应用，
      但 API、事件和媒体不能依赖某个浏览器页面的组件状态。

### 5.2 远程蜂窝通话信令

- [x] 通话业务入口根据线路 ICCID 选择本地或远程 `CallBackend`，继续由服务端记录并广播
      来电、拨号、振铃、接通、挂断和失败状态。
- [ ] Agent 将 `dial/answer/hangup/status/dtmf` 映射到当前 Provider：Windows 优先 MBN/设备服务，
      Linux 优先 ModemManager；只有 `GenericAtProvider` 才映射到标准 3GPP AT 命令。
- [ ] 来电事件必须先在服务端创建或匹配 `call_id`，再通知浏览器和未来电话客户端；客户端不得
      使用 Agent call path 接听。
- [ ] 重复、迟到或来自旧 session 的通话事件不得创建第二条通话或覆盖新 attachment 的状态。

### 5.3 浏览器双向音频

- [x] VoWiFi 通话继续复用现有 IMS -> Asterisk -> WebRTC 链路，不做任何远程专用语音栈。
- [x] 蜂窝直拨时，Agent 只有探测到真实可用的 USB Audio、PCM 或模拟音频接口后才声明
      `call_audio=true`。
- [x] 服务端媒体协议选型已实证：现有 Asterisk 20.7 已加载 `app/chan/res_audiosocket`，原生
      AudioSocket 的 `0x10` payload 正好是 Modem 的 8 kHz/16-bit/mono little-endian PCM。
      临时测试从 engine container 经 `host.docker.internal` 连接网关侧 TCP relay，UUID 握手成功，
      3 秒内双向各传输 48000 bytes；测试 dialplan、监听器和通道均已删除。无需引入第二套 PBX、
      ModemManager、baresip、ARI 或自定义 RTP packetizer。
- [x] 有音频能力时，Agent 只负责把 Modem PCM 桥接到独立、每通话建立的已认证媒体 WSS；网关
      relay 原样配对 AudioSocket frame，不解码、不混音。Asterisk 继续作为唯一媒体锚点，浏览器及
      未来电话应用继续复用现有 WebRTC/SIP、录音、静音和权限逻辑；控制 WSS 与媒体 WSS 分离，
      避免音频背压阻塞心跳、短信、挂断和 APDU。
- [ ] `CallAudioBackend` 至少包含两个可独立探测的 provider：首选按同一硬件 container 精确匹配的
      UAC/WASAPI（模块自身是虚拟声卡，不要求主机有物理声卡）；Windows Audio 服务、UAC endpoint
      或打开流失败时回退 NMEA serial PCM。Linux/macOS 使用各自原生 UAC backend；未经该平台实机
      验证只上报 unavailable，不用 Windows 名称、COM 号或默认音频设备推断。
- [x] `audio.open` 由服务端为指定 `call_id` 分配媒体参数并启动，`audio.close` 在挂断、断线或
      超时后释放资源；Agent 不自行选择用户或浏览器目标。状态机必须先在 idle 阶段 prepare
      `QPCMV`，再拨号/接听；任一端结束都按“停止读写线程 → 关闭媒体 endpoint/serial →
      `QPCMV=0` → 恢复 GNSS 租约”的顺序幂等收敛，并拒绝迟到的旧 `call_id`。
- [x] 第一版只实现现有 Asterisk 可直接接收的最小 codec 集和单路通话，不建设通用媒体服务器。
- [x] 无音频接口的 Modem 仍可上报来电和执行通话信令，但 UI 必须明确显示“该 Modem 无可用音频”，
      不能显示为可正常语音接听。

## 6. 状态与恢复

- [ ] 同一 Modem 换 SIM：旧 ICCID 的 profile、线路、短信和通话历史不变并进入离线；新 ICCID
      作为新的来源出现，不能继承旧 SIM 依赖。
- [ ] 同一 SIM 换 Agent/Modem：重新附着原 ICCID，恢复原 profile、出口和线路，无需重新配置。
- [ ] 网关重启：持久化配置和稳定本地入口不变；等待 Agent 重连后恢复运行时 attachment。
- [ ] Agent 断线中的未确认短信或拨号操作保持 `unknown`，不能因重连自动执行第二次。
- [ ] 通话中 Agent 断线：服务端结束或标记失败的通话、关闭 RTP、通知所有客户端并保留记录。
- [ ] 页面业务状态只由 attachment、能力、数据连接和健康检查组合得出，不直接展示底层 socket 状态。

## 7. 完成标准

### 7.1 复用与兼容

- [ ] 本地 ModemManager 的短信、通话和数据行为保持不变。
- [ ] 本地与远程实现运行同一组 `CellularDataBackend`、`SmsBackend`、`CallBackend`、
      `CallAudioBackend` 和状态结果合约测试；不声明音频能力的实现可通过明确的 unsupported
      合约结果完成 `CallAudioBackend` 测试。
- [ ] 现有 REST API、数据库记录和 WebSocket 消息只做向后兼容扩展；不新增远程专用业务 API。
- [ ] 代码中只有 Agent transport/adapter 可以依赖 WSS、COM 口或远程平台细节。

### 7.2 数据出口

- [ ] 同一 SIM 完成：Agent A 在线 -> 拔出 -> Agent B 插入 -> 原 profile 和依赖自动恢复。
- [x] 验证 SOCKS5 TCP、UDP ASSOCIATE 和 DNS 均从指定蜂窝接口出站。
- [ ] 断开蜂窝接口但保留 Agent 默认网络，验证任何测试流量都不会从默认网络泄漏。
- [ ] 在 Agent 主机启动普通浏览器及 EasyTier/同类组网进程，并测试普通 socket 与显式绑定
      蜂窝接口；它们必须全部失败，同时 MDD 的 TCP、UDP 和 DNS 代理测试仍成功。
- [ ] 删除隔离规则、重启 Agent、重枚举蜂窝接口和强制结束守卫进程，验证出口立即 fail-closed，
      不会退化成“临时允许本机共用”。
- [ ] 多条线路可复用同一 SIM 出口；出口离线时全部 fail-closed，恢复后无需重新配置。

### 7.3 短信

- [ ] 远程短信可以发送、接收、持久化和推送，且沿用现有 UI。
- [ ] 验证发送超时、重复响应、旧 session 响应、断线补扫和消息去重。

### 7.4 浏览器电话

- [ ] 浏览器可以通过远程 Modem 拨号、接听、挂断和发送 DTMF：呼出、挂断和双向语音已实机
      通过；来电事件与接听界面已实机验证，实际接听仍待人工验收；DTMF 等待通话中人工验收。
- [ ] 有音频能力的 Modem 完成浏览器双向语音、静音和录音验证：呼出双向语音已通过，来电、
      静音和录音仍待人工验收。
- [ ] 来电可以同时通知多个已登录客户端，但只有服务端接受的首次接听生效，其他客户端同步进入
      已由其他端接听的状态。
- [ ] 通话期间刷新浏览器页面或另一个客户端接入后，可以从服务端恢复当前通话状态，而不是依赖
      原页面内存。
- [ ] Agent 在振铃、接通和通话中断线时，服务端、浏览器、Asterisk 和录音资源最终状态一致。
- [ ] 无音频能力时验证 UI 不会误导用户可以进行双向语音。

### 7.5 页面

- [x] 代理库显示“SIM 蜂窝数据”，创建时不持久化 attachment 或 session 字段。
- [ ] 验证在线、正在连接、离线、不可用和冲突状态，以及离线 SIM 可配置、删除依赖警告和
      跨页面状态一致性。

## 实施顺序

按以下顺序实现，每一步都保持已有本地功能可用：

1. 冻结当前 Windows 自制协议扩展；保留已经独立验证的 Registry、稳定 ICCID 入口、WSS transport
   和隔离代理，不再为 EC20/当前驱动增加业务层特判。
2. 建立成熟后端能力矩阵和可回放验收夹具：Windows MBN 数据 + 独占 AT function 的 libGammu
   companion + 独立媒体桥为当前优先；Linux ModemManager 是后续基线，macOS 后置。记录版本、
   许可证、权限和每个 USB function 的所有权。
3. 完成小型 Backend 合约及本地 ModemManager 包装，让本地与远程 Provider 运行同一套测试；页面
   只显示 Provider 实报并通过验收的能力。
4. 完成一次提权安装器、低权限 Agent、最小特权守卫服务和本地 IPC；拔插、崩溃、升级、卸载均
   能恢复宿主网络与设备状态后，才把该平台标记为可部署。
5. 验收组合 Provider：MBN/RMNET 数据、libGammu 短信/通话信令和 NMEA/UAC 媒体各自独占 function，
   但由同一协调者绑定和恢复；只有设备无法安全分工时才进入完整可回滚的 `agent_managed` 整机模式。
6. 依次完成远程 SMS、通话信令、音频到 Asterisk RTP；每一项实机通过后才开放对应页面动作。
7. 最后完成断线、换 SIM、换 Agent、换 Modem 型号、旧 session、无泄漏和多客户端验收。
# WebRTC 多宿主媒体入口（迁移后根因整改）

- [x] 管理监听、SIM 国家出口与 WebRTC 媒体入口分离；禁止以公网默认路由推断媒体地址。
- [x] 宿主编排器发布带代际的接口清单；浏览器只能确认当前清单里的入口，禁止提交任意 IP、DNS 推断或信任转发头。
- [x] 直接 IPv4 路径按管理会话和 WebSocket 独立改写 SDP/ICE；多个浏览器可选择不同物理/VPN 入口，不修改 SIM 国家出口或全局 Engine 地址。
- [x] 首次部署、Control 重启、接口清单或 Engine 代际变化后在页面重新提示确认并提供快速诊断；旧选择可展示，但旧验证不可继续授权真实呼叫。
- [x] Engine overlay 的事件钩子不再依赖脚本可执行位；使用结构化参数与 Engine incarnation/call linked-id，实现回调乱序幂等收敛。
- [x] 增加不接 IMS、不产生资费的本地 Echo：浏览器与 Asterisk 必须同时证明 exact canary 的双向 RTP，证明绑定入口、WS 和 Engine 代际且不可跨呼叫复用。
- [x] 每个真实浏览器呼叫进入 Asterisk 时先设置 10 秒本地绝对安全租约；仅对应 WSS 与 exact uniqueid 存活时每 2 秒续租。浏览器、网络或 Control 断开后无需依赖服务端内存任务也会到期挂断；受控关闭另做一次 exact uniqueid 挂断，绝不使用 `hangup all` 或影响另一用户/线路。
- [ ] 为互不互通的多个 LAN/VPN 客户端增加标准 TURN 配置（浏览器和 Asterisk 共用同一受管凭据），不能用重复 `ice_host_candidates`、默认路由或固定网段伪装多宿主支持。
- [ ] 把 VoWiFi UA 提升为登录会话级 CallCoordinator，使浏览器在通话页以外仍能收到真实 SIP 来电并接听；页面只订阅同一会话，不创建第二个 UA。
- [ ] TURN 完成后分别验收反向代理/NAT/IPv6；在此之前这些入口应明确提示需要 TURN，不能误报为已验证。
