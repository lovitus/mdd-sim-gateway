# 已识别但不并入当前通话修复的边界

- 2026-08-31：Linux 受控数据借用当前只接受 ModemManager Bearer 明确返回的 static IPv4，已经覆盖
  EC20/QMI 主纵切。ModemManager 官方契约说明 DHCP bearer 还需要 DHCP client、PPP bearer 还需要 PPP
  会话，IPv6 通常还需要 SLAAC/DHCPv6；这些不能把空地址伪装成可用。后续在出现对应真实 Modem 前，
  优先复用成熟 Go DHCP/PPP/IPv6 组件，并继续保持 socket mark、非 main 路由表、先撤 permit 后断 bearer
  的同一防漏边界；当前三种方法 typed fail-closed，不阻断 static IPv4 whole-Modem 里程碑。

- 2026-08-31：当前 AgentLink 的 `TokenResolver` 接口已经支持按 Agent ID 返回凭据，但现有单机
  bootstrap 配置仍把同一个 `agent_token` 发给全部受信 Agent；因此 Agent ID 只是部署身份，不是彼此
  隔离的密码学身份。raw Modem 每条 USB/IP 流已有独立、一次性、分角色 token，不能串流，但持有全局
  Agent token 的恶意终端仍可能在合法终端离线时冒充其 Agent ID。当前私人受控 Agent 范围不阻断
  Windows/Linux raw Modem 功能纵切；在允许第三方或互不信任 Agent 接入前，必须把 bootstrap/配置迁移为
  每 Agent 独立 token（或客户端证书），并保留无明文日志、撤销和滚动更新契约。不要用 Agent ID 哈希或
  共享密钥派生冒充独立凭据。

- Go VoWiFi 的用户态 IMS Security-Agree 当前只接受 UDP 和无 IPv6 extension header 的精确
  transport selector；TCP/TLS 本地绑定、IPv6 extension-header walker 以及 ESP auth/replay drop
  诊断计数，待真实运营商或诊断页面出现明确需求时再单独实现。当前均 fail closed，不回落宿主网络，
  不阻断已经覆盖的 UDP Security-Agree 主路径。

- 持久租约在Control重启丢失RAM owner之后，历史记录仍仅有ICCID，没有原Agent/Modem身份。
  本批只保护仍有确切RAM owner的续接、终止及后台恢复，不宣称覆盖跨硬件迁移后的旧孤儿记录。
  后续单独评审身份持久化/旧数据兼容与迁移测试；当前Agent本地付费租约保护不撤销。
- 浏览器媒体 WS 已支持同一 owner/session 在固定 10 秒内重接；Control 进程重启会丢失 RAM
  session，仍只能依靠 Asterisk/Agent 的停费租约收敛，不能跨 Control 重启恢复旧媒体通话。
- EC20 双向音频已移除 Control 额外 20ms pacing。若部署后的真实 50 秒通话仍出现持续破音，
  再单独评审跨浏览器/USB 音频硬件时钟的自适应 jitter/resample；本批不预先实现该复杂机制。
- 已知远程卡离线、且没有活动RAM owner时，部分旧状态入口仍可能误查本地ModemManager。
  后续统一离线设备路由识别；不得以无响应伪造idle，或用模糊历史映射指挥另一台设备。
- 2026-08-27：新增 `reconcile_orphaned_usim_recovery_fence`（治好了 iid7 Free FR 卡在
  "本地 VoWiFi Engine 未继续推进注册" 的一种具体成因：Engine 侧裸 fence 从未被
  Control 的 campaign 认领就跨代际存活）之后，怀疑这是一类更广的问题：`run/` 目录下
  还有其它"只按文件是否存在判断、不核对 engine_run_id"的产物（例如 admission 相关
  标记），理论上都可能被同一根因（Docker 自愈重启 vs Control 生命周期的双重所有权）
  绊住。本次只补了这一个已经实锤复现的具体案例，没有做全量审计。后续应通读
  `run/` 目录所有产物的读取点，逐个确认是否已按 run_id 校验新鲜度，而不是等下一次
  具体线路卡住才发现下一个实例。
- 2026-08-27：远程 VPCD 读卡器（`card_agent.py` 经 WebSocket 桥接）的 WS 链路一旦
  断开，`control/app/main.py:api_vpcd_ws` 的 `finally: vpcd_registry.release(claim)`
  会立即无条件释放本地 vpcd↔pcscd 会话，即使 Agent 几秒内就重连、物理卡从未离开。
  这会让 pcscd 把纯粹的网络抖动报告成"卡被移除"，进而在恰好撞上 IKE/EAP-AKA 或 SIP
  REGISTER 的窗口时制造一次可避免的重新认证。真正的修法是让本地 vpcd 会话在一个有
  界的重连宽限期内保持存活并支持"续接"（类似 `call_media.py` 里浏览器媒体已有的
  `browser_reconnect_deadline`/resume ticket 模式），但这会直接触碰 `vpcd_slots.py`
  的 claim/release/`current_identity` 状态机——这个模块已经因为类似的细节问题反复
  出过事故，不适合顺手改。当前已用一个小得多的办法先吸收掉这类抖动：把
  `"Card was removed."` 归入 `pcsc_card_reset` 分类（`engine/ami_usim.py`），让它复用
  既有的一次性有界重注册与孤儿 fence 回收管线；未来如果这类抖动的影响面扩大到需要
  真正的会话续接，应作为一个独立、经过复审的 VPCD 会话续接批次实现，不要顺带塞进
  别的修复里。
- 2026-08-29：Linux deb/rpm/apk 包装延后。已核对 nFPM v2.47；后续包只应携带当前
  versioned release directory，并调用同一个 Go `install-release` 契约，不能在 package lifecycle
  shell hook 中复制账户、权限、链接切换、回滚或服务启停逻辑。当前可重复安装、升级和回滚已由
  纯 Go 安装器及 root-only receipt 覆盖，增加发行版包不阻断下一批 PC/SC shadow 验收。
- 2026-08-29：生产 release/receipt/Core SHA 均可精确追溯，但 `/v1/system/runtime` 的
  `build_version` 仍显示 `(devel)`。后续在统一 release 构建入口用 Go ldflags 注入提交和 release ID，
  并增加安装后契约测试；这只是展示／追溯冗余缺陷，不阻断已由 digest 与 receipt 证明的当前运行
  版本，也不应插队打断蜂窝短信主纵切。
- 2026-08-29：后续蜂窝流量借用必须先实现 Agent 对数据面的独占接管，并采用持久化、默认拒绝的
  宿主转发／出口策略；即使 Agent 进程退出或崩溃，曾接管模块的漫游流量也不能回落给宿主、VPN、
  打洞软件或其它进程。服务端同样不得把蜂窝链路设为宿主默认出口。当前 Windows MBN 只读观测
  不具备这种独占保证，因此不得把现状宣称为防泄漏，也不得在该保护完成前启用流量借用。
- 旧的独立 `agent/go-agent` 在 Go 1.26 `go vet` 下报告 `net.Dial` 地址由
  `fmt.Sprintf("%s:%d", ...)` 拼接，不能正确处理 IPv6；该模块不参与当前 Go runtime/Mac Agent
  发布。后续恢复该旧入口时改用 `net.JoinHostPort` 并补 IPv4/IPv6 契约测试，不混入 eUICC 功能批次。
- 2026-08-29：eUICC 通知只有在当前 delivery 明确得到服务器确认、但卡内移除失败时，页面才提供
  一次纯移除恢复；若此时浏览器/Control 同时丢失结果，当前选择保留卡内通知，不凭猜测删除或重发。
  只有真实现场反复出现这种双重故障时，再单独评审不含激活码/凭据的 durable acknowledgement
  ledger；现在没有可信触发频率，不为假设场景增加持久状态机。
- 2026-08-30：Android 工程的 `agent/android/gradle/wrapper/gradle-wrapper.jar` 缺失，
  `./gradlew --version` 实测失败为 `ClassNotFoundException: org.gradle.wrapper.GradleWrapperMain`。
  共享 `GRADLE_USER_HOME` 已配置，但 wrapper 缺失是独立的发布可复现性问题；后续 Android
  构建批次应从已信任的 Gradle 生成并提交 wrapper JAR，核对校验和 wrapper 版本后再验证。
