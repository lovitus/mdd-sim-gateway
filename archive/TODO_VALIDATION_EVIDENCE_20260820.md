# 归档：单项验证证据（2026-08-20）

> 从 `TODO.md` 拆分归档，2026-08-27。这是某次实机验证的证据快照，单项通过不代表主流程
> 完成。当前执行游标以 `TODO_CURRENT_RECOVERY.md` 为准，不是本文件。

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

