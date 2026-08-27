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
  [archive/TODO_ACTIVE_RECOVERY_20260824.md](archive/TODO_ACTIVE_RECOVERY_20260824.md)
  （2026-08-27 归档）仅保留历史，不执行其中旧的下一步。继续工作或会话压缩后恢复时
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

## 工程规则：先研究，后实现（调研过程已归档）

> 08-18 的调研/选型过程记录已归档到 [archive/TODO_RESEARCH_LOG_20260818.md](archive/TODO_RESEARCH_LOG_20260818.md)，
> 其结论已经沉淀进下面 [1. 最小架构](#1-最小架构) 起的章节。此处不再重复调研细节。

## 当前验证基线（2026-08-20）

> 单项证据已归档到 [archive/TODO_VALIDATION_EVIDENCE_20260820.md](archive/TODO_VALIDATION_EVIDENCE_20260820.md)。
> 单项通过不代表主流程完成，不是当前执行游标；当前游标见 [TODO_CURRENT_RECOVERY.md](TODO_CURRENT_RECOVERY.md)。

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
