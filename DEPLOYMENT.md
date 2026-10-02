# MDD VoWiFi Gateway - 完整部署与维护手册

## Current scope / 当前范围

Windows、macOS、Linux 远程 Agent 仍是产品要求；Android 预览里程碑已按后续用户决定交付，当前状态见
[版本化台账](docs/status/README.md)。开启 4G 必须先证明隔离就绪，
失败即拒绝数据，不回落宿主网络，不改变用户开关。macOS 本轮不做公证、不用 `.p8`，现有签名继续保留。
既有部署/浏览器/隔离/恢复报告需按范围核对，不因本轮未复验作废；Git main、已构建工件和已部署版本必须分开。
完整决定见 [当前范围](docs/decisions/2026-09-20-current-scope.md)。下文能力门禁不授权自动启用或部署。

本项目通过 SIM/eSIM 读卡器或受支持的蜂窝模块提供通话与短信管理。VoWiFi 使用运营商 ePDG/IMS；蜂窝通话使用远端 Agent。费用按运营商套餐及漫游规则执行，不保证免费。

## 2026-10-02 Windows 换机接入排查（未完成）

最新观测覆盖下述历史过程：用户随后授权完全停止 VMware；其用户进程、相关服务及可停止
USB 驱动已停，原生 Quectel 接口一度恢复，但 AT 打开报设备无法正常工作。用户再次插拔后
曾出现原生 USB 描述符失败/Code 43；第二次插拔同一端口后恢复 AT/Modem 枚举，AT 打开变为
资源占用。尚无证据确认占用者，不能把枚举成功当作读卡恢复。Core 新鲜快照仍缺少目标 Agent，
其原进程仅有本地控制监听、没有 WSS 连接；Windows 现场故障仍未解决。其他两条读卡器线路
仍有读卡和 IMS 注册证据，不扩展为通话验收。没有重启 Agent、改用户策略或进行付费测试。
最新模块检查只在诊断 PowerShell 观察到 Proxifier 模块，未在 Agent 找到对应模块；此前
“Agent 内存在 Proxifier”的表述不能作为当前事实或根因结论。临时退出 Proxifier 的独立
对照仍待授权，不因 VMware 停止授权而操作。两条本次遗留诊断进程已按 PID/路径/启动时刻
核实并清理，原 Agent 未动。以下按发生顺序保留早期尝试，不是当前待执行步骤。

当前主任务仍是 EC20 移入另一台已有 Agent 的 Windows 主机后不能自动发现、注册。
新增备用管理地址和下文嵌入式手动部署说明不能代替这一验收；备用地址属于同一主机，
不应创建第二个 Agent 身份。具体地址和原始证据仅保存在工作区外的私有游标。

已验工件仍在运行，但 Windows 尚未生成该 EC20 的 modem/AT 接口。首次只读枚举只能看到
VMware USB Code 43；一次精确节点重枚举后，底层描述符与当前 VMware 日志同时确认
`VID 2C7C / PID 0125` 的 Quectel 身份。没有发现该设备的 VM 自动连接规则，也没有正常
连接至 guest 的证据；日志中的 `Found device` 不能作为 guest 已接管的证据。

用户明确授权后，短停 VMware Workstation Server 与 USB 仲裁服务，并在确认停止后对
该故障节点重枚举一次。最终读回已从 `vmusb.sys` 转为 Windows 原生 `usb.inf`，但仍是
`Device Descriptor Request Failed / Code 43`，没有 modem/AT 接口。因此设备已回到宿主
USB 栈，不等于恢复可用，也不能据此断言是硬件损坏或 MDD 自动配卡缺陷。
两个 VMware 服务已恢复原运行状态及启动配置，两台 VM 的进程未变，读卡器仍 Started，
MDD 持久蜂窝隔离规则逐项不变，一次性独立回滚任务已移除。临时工具与远端诊断文件在
私有归档哈希核对后已移除，无残留诊断进程。服务在显式恢复前已自行运行，
故不声称整个采样窗口持续排除了 VMware。另一次按设备 ID 的端口复位工具调用因找不到
可操作端口而在执行前退出，不能把它记录成已经完成断电复位。

另一个独立问题是原生套接字客户端在连接本机控制端口时就卡住，尚未进入 MDD HTTP 处理。
早期报告把 Proxifier LSP 归到 Agent，最新模块核对已在本节开头纠正；加载拦截模块
本身也不证明因果。处置后一次 Core 读回仍未见目标 Agent，SCM Running 不能代替注册。

没有重启 Agent、关闭 VM、改驱动/VM 自动连接配置、修改用户开关、放松隔离或产生付费操作。
随后用户要求完全停止 VMware。官方控制工具在 SSH 与已有登录会话中均于只读 VM 枚举
阶段超时，尚未发出 guest 关机或挂起，不能声称两台 VM 已停机；临时控制客户端和任务
已清理。当前下一步是通过已发起的远程桌面、在主机接受连接后安全关机/保留状态挂起 VM，
再完整退出 VMware 并读回 EC20。没有强杀 VM，也不修改其启动设置或卸载软件。
原生 USB 仍失败时再接续 EC20/转接板完整断电后的枚举对照。Proxifier 的短时退出/恢复
另行请求具体授权，不将 VMware 停机授权扩大成网络软件授权。
原始报告及逐文件哈希仅保存在私有目录，不发布主机身份或日志。安全处置依据为
[VMware 自动连接语义](https://knowledge.broadcom.com/external/article/343950/automatically-connecting-usb-devices-at.html)
和 [Windows 精确节点重启](https://learn.microsoft.com/en-us/windows-hardware/drivers/devtest/pnputil-command-syntax)。
以下九月三十日记录保留其当时部署验收范围，不是十月二日实时健康证明。

## 2026-09-30 版本对齐记录

最终结果：PR #18 已正常合并，五个 Agent 已更新至已验 `bca3dce` 工件，Linux 通话能力及对应
readiness 已恢复。Core/Provider/egress 保持下述 `2e45bab` 工件，Android 保持 v97；
本批修复没有改变这些角色的运行行为。下文先保留首次对齐证据，再记录修复后的最终读回。

用户授权核对现有客户端和服务器，更新落后版本并验证，问题继续交由原 ChatGPT 评审会话协作。
这不是新功能、额外付费测试、长期耐久验收或改变用户 4G/借用意图的授权。

代码基线是已合并的 `main@9f9953fa0e6a33eb8a1c1971d368fc06cf6d7812`。
复用已成功签名的 [GitHub run 36584798641](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36584798641)，
真实工件源码为 `2e45bab3c9a644335cb265b4635ba3f2297e7a5f`，不能改称由 main 构建。
其 Go、WebUI、Android、Provider、Agent 和测试目录与该 main 完全相同；差异仅在记录和临时验证入口。
此版本对齐复用了已验工件，没有本地构建；随后能力修复的独立 CI 见下文。

| 运行角色 | 本次结果 |
|---|---|
| Linux Core、provider-apply、egress | 已更新；三个实际 PID 的可执行文件 SHA-256 与上述工件一致 |
| 七个已运行的 VoWiFi Provider | 已更新；逐个 `/proc/PID/exe` 核验，不以入口软链接代替进程证据 |
| 两台 macOS Agent | 已更新；保留原 GUI/CLI 模式、launchd owner、Developer ID 身份、配置和卡片归属 |
| 两台 Windows Agent | 已更新；SCM 实际进程和文件哈希一致，无另一个旧 MDD GUI/helper 进程 |
| Android 预览 v97 | 无需重装；已安装 APK 与已验收工件相同，Android 运行源码及构建输入与 main 一致 |
| Linux Agent | 已从 `a10d9702c48f` 更新到上述工件；通过现有 Core 受控重启，实际 PID 文件哈希、新代际、原卡身份和持久策略均已核对 |

Linux Agent 的实际 SHA-256 为 `6092b9f28d66790819eb705c766d3091fb9448b238cc86d0854a4c122f2e5617`。
稳定启动入口已指向新工件，原版仍保留；本次两个临时 systemd 覆盖已经移除，隔离守卫保持启用和活动。
重启请求只发送一次，绑定当时代际和 operation，由既有 Core/Agent 业务门禁、Provider drain 和维护 fence
裁决，没有直接调用裸 Stop/restart，也没有改变 4G、漫游、借用或 APN 意图。HTTP 返回匹配操作的
`state=restarted`；首份新拓扑尚无 modem，后续一次有界等待读回原 modem、卡片和相同策略 revision。
未因此再次重启，也未把首份空拓扑当作丢卡或持久策略已改变。

只读冒烟：六个 Agent 在新 Core 上重新上报；线路操作可用性与更新前相同，两条原先已注册的
VoWiFi 线路恢复注册。十条线路、十二个设备、五十六条通话记录、十八个短信会话、三个 eUICC、
出口、通知、设置和诊断接口均读到实际数据；系统状态返回零 alerts/errors。
配置、卡片归属和通知设置保持不变，Provider 维护租约已全部释放。Linux Agent 换版后的最终对比
另发现下述通话信令差异，因此不能将先前这份就绪性基线直接扩大为全部换版后的能力验收。
Registered 和这些 API 结果不代表新做了语音、短信送达或浏览器页面验收；本次没有付费操作或通知重放。

Linux 服务端在停止状态写入者后备份；服务端保留旧版和失败证据，本地私有副本哈希也核对一致。
安装曾因部署脚本继承私有文件 umask 而被精确文件模式校验拒绝，已核验回退后的实际旧进程；
仅修正公开工件安装子进程的 umask 后再完成更新，没有弱化清单校验或倒灌数据库。
该安装器对 restrictive umask 的处理留在现有安装器延期项，不扩成本次产品开发。
私有主机、凭据、原始输出和备份只存工作区外的受限私有目录，不发布到 Git。

纠正早先的停止理由：`/v1/host-modem/idle` 的 409 符合该入口要求承载断开的契约，
但承载连接不等于用户正在传输数据，也不证明流量泄露。为 Agent 换版选择这个入口并据此要求用户
关闭 4G，是执行路径选择错误。原评审会话指出已有 `POST /v1/agents/{agentID}/restart`，
具有自己的完整业务保护，现场也实际广告该能力；本次按这一既有路径完成，不弱化 host-idle 门禁。
早先“评审超时未答复”也已纠正：等待器漏读了同一 turn 后追加的回复，并非评审没有答复。

升级前后各一个 60 秒被动窗口：蜂窝接口 RX/TX 字节和包增量均为零，抓包均为零；
没有主动生成流量。nftables 仍仅允许指定 Agent cgroup 与 socket mark，其他宿主输出及双向转发被丢弃；
主路由表没有蜂窝默认路由，保留的 IPv6 link-local 路由不是默认路由。
这是两个观察窗口内未见传输/泄露的证据，不是长期或全平台防泄漏验收。

凭据发送前逐连接核对既有服务器证书 DER SHA-256，继续校验 TLS，不使用忽略证书选项。
证书没有变更，不再作为部署或 API 验证的阻塞。Chrome 页面本轮仍未复验，保留未验状态，
不将 API 检查冒充页面通过，也不据此阻断已授权的版本对齐。
本次运行版本对齐已完成；源码、实际工件和上述验收边界分别记录。

### 换版后发现的通话能力负缓存

Linux modem 已重新识别，AT、短信、数据正常，但 `CallSignalling` 从 true 变为 false，
对应 `cellular_call` 被真实阻断。核对 equipment 后，经同一个 ModemManager 的一次只读 CLCC
事务成功，返回的 mode=1 是数据承载，不是语音通话。没有拨号或修改开关。
原 `a10d970` 与当前版本的 AT owner/manager 源码相同；初次探测的错误未保留，具体原因未知。
已确认的既有缺陷是可选 CLCC 首次失败后一直缓存 false，后续 AT 健康并不会重新验证该能力。

本批修复仅在既有 Reconcile 健康检查后，通过同一 owner 有界重探 CLCC；成功才更新能力。
失败不关闭健康 owner，按 30/60/120/240/300 秒封顶退避，之后仍有恢复机会；不新建后台循环。
能力读写同步，取消后的响应不能发布成功。沿用 Linux MM 精确 SIM/设备身份门禁及独占通道，
不直接开 tty，不动 SMS/PCM/APDU、承载、APN、radio、通话生命周期或持久策略。
Manager Snapshot 的失败详情只含错误类别与下次重试时间，不暴露原始响应或号码。
Linux 数据模式的 `dataFact` 仍丢弃这一 Detail，但正常传递能力布尔值；该非阻断诊断接线问题
保留在现有延期台账，不声称最终拓扑或页面已经展示重试诊断。

失败方式包括永久负缓存、无节流重复探测、取消后误报成功、能力读写竞态，以及把 CLCC 数据条目
当成语音通话。现有夹具覆盖这些行为，并沿用 MM 换卡拒绝/数据模式禁止直接 tty 的边界测试。
精确候选 `bca3dced5c8c2d8dc6cb90fc6c5d778cc3b4378b` 已通过完整签名
[GitHub run 36660466342](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36660466342)。
反例 artifact `11074347146` 的 ZIP SHA-256 为
`39680f51cf423d9e3d71452bd12cc06d1542c1291e0c220b975b108f789d619c`。
下载摘要和原 JSONL/stderr 均已核对：只撤销新重探分支时，恰好三个可编译行为失败，
分别是同 owner 能力仍为 false、到期没有第二次探测、取消路径没有重探诊断。
这些反例证明缺少重探，不证明旧版存在高频请求或取消后误报成功。
恢复代码后，三个新增方法及两个既有 MM 方法全部通过，零跳过、stderr 为空，无 race/编译/panic 报告。
常规完整 CI 门禁也通过，不将未实际运行的 opt-in 硬件项目算作验收。

原评审已独立核对实现、原始红绿工件和清理等价，批准最终 `b71b81c`。
临时入口移除后，永久源码及测试仍与已验候选逐字相同，长期 workflow 恢复原样；
正常 PR [run 36662189390](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36662189390) 也通过。
[PR #18](https://github.com/lovitus/mdd-sim-gateway/pull/18) 按精确 head 正常合并为
`e30a45fb666fa2f2ec5eba7596df4fecb331513a`，未绕过保护；实际部署来源仍是 `bca3dce`。

### 修复后的最终读回

五个 Agent 已更新。两台 Mac 保留原 GUI/CLI 模式与 Developer ID，launchd 实际 PID 的执行映像
与启动项、已验候选一致；两台 Windows 的 SCM 实际进程 SHA-256 均为
`a3e7df9e67f10f275ade2b452a8c8a63fbe0aacf0d57efe1b0c4a399ab4c42b3`。
Linux 的 `/proc/PID/exe` SHA-256 为 `96a30301faf367645eeb4f1ead592ccfbe95e9492bbe37962b1dae1fe0a496ca`。
它仅收到一次匹配操作与代际的 Core 受控重启；此前完整包传输超时没有触及运行时，
压缩传输及完整清单/哈希校验成功后才开始切换。旧工件及失败证据保留。

Linux `CallSignalling=true`，对应 Core `cellular_call`、`cellular_sms`、`cellular_data` 均 ready。
原卡/设备身份、持久策略 revision/desired、配置及隔离文件哈希不变；nft 规则结构不变，
守卫启用且活动，IPv4/IPv6 主路由表均无蜂窝默认路由，Core PID 未变，维护租约已释放。
其余九条线路的操作就绪/阻断投影与基线一致，两条原 IMS-ready 线路仍 ready。
catalog 和通知配置不变，没有借用流量会话。最终快照另见一条 `preparing` 媒体会话，
没有新增通话记录；本批没有创建或终止它，不将其当作泄漏或换版故障，也不宣称所有客户端空闲。

后续只读核对（2026-09-30 17:26:41，Asia/Singapore；09:26:41 UTC）：上述 exact session 已不存在，
当次 native 媒体会话、借用流量会话和维护计数均为零。同一 call_id 的历史记录包含 03:22:57 UTC
的呼出开始/answered，以及 03:23:09 UTC 的结束时间。旧快照为 03:22:52 UTC，heartbeat
当时仅约三秒龄，不能据此称为卡死，也不能反推会话恰在通话结束时或由 watchdog 回收。
本次没有发起/终止会话或重启服务；历史记录不证明发起端身份或双向语音质量。
源码 watchdog 没有独立的 `preparing` 到期分支，保留为待验证边界。正常准备已有分段超时和
失败清理，但不等于证明所有资源在 35 秒内释放；没有据此宣称孤儿会话缺陷或追加 Core 修改。

本次现场关闭的是工件对齐和已发现的能力/readiness 异常，不是新的实际语音、浏览器或耐久验收。
没有拨号、发短信、通知重放、SIM/PIN 操作或用户开关修改。非阻断 Detail 接线延期保持不变。

---

## 目录
1. [系统架构与原理](#一系统架构与原理)
2. [服务端部署指南](#二服务端部署指南)
   - [方案 A：不可变 Go artifact 安装（推荐）](#方案-a不可变-go-artifact-安装推荐)
   - [方案 B：离线安装同一 Go artifact](#方案-b离线安装同一-go-artifact)
   - [方案 C：Nginx 反向代理默认 Go 入口](#方案-cnginx-反向代理默认-go-入口)
3. [客户端部署与支持边界](#三客户端部署与支持边界)
   - [Windows 客户端](#1-windows-客户端)
   - [macOS 客户端](#2-macos-客户端)
   - [Linux / 树莓派 / NAS 客户端](#3-linux--树莓派--nas-客户端)
4. [语音通话与软电话使用](#四语音通话与软电话使用)
5. [线路诊断与支持包](#五线路诊断与支持包)
6. [上游代码同步与维护 (Rebase / Cherry-Pick)](#六上游代码同步与维护-rebase--cherry-pick)

---

## 一、系统架构与原理

```text
+-------------------------------------------------------------------------------+
|                    客户端 (Windows / macOS / Linux)                          |
|  - 插卡设备: USB CCID 读卡器 / ESTKme / 9e 卡槽                               |
|  - 统一 Agent: Windows / macOS / Linux；能力按平台与硬件资格开放             |
+-------------------------------------------------------------------------------+
                                      │ 统一认证 HTTPS/WSS（无 VPCD 兼容入口）
                                      ▼
+-------------------------------------------------------------------------------+
|                    服务端宿主机 (Linux VPS / 本地服务器)                       |
|                                                                               |
|  mdd-core                 配置、鉴权、页面、状态事实、Agent 与浏览器 WSS       |
|  mdd-provider-apply       受限的线路 Provider 配置应用                         |
|  mdd-vowifi@<line>        每线路独立的 Go VoWiFi Provider                      |
|  mdd-egress               只监听回环 SOCKS 的用户态国家出口                    |
|                                                                               |
|  默认公开入口: HTTPS/WSS 8443；不要求额外 RTP/SIP 端口                         |
+-------------------------------------------------------------------------------+
                                      │ IPsec (ESP / UDP 4500)
                                      ▼
                       运营商 VoWiFi 核心网 (ePDG / IMS)
```

---

## 二、服务端部署指南

### 端口开放要求

* **Web 控制台及统一 Agent**：默认 `8443`（HTTPS/WSS）；外部反向代理可使用自己的入口端口。
* **PC/SC／eUICC**：复用统一 Agent 高层协议；旧 TCP VPCD／35963 兼容入口已删除，不应开放。
* **浏览器音频**：不需要另外开放 RTP UDP 或 Asterisk SIP/WebRTC 端口。

### 多网卡 / VPN 与同源浏览器媒体

浏览器音频只使用当前页面同源的 WS/WSS，不要求用户确认服务器网卡/IP，也不需要浏览器与
Asterisk 直连 RTP 或配置 TURN。VPN、域名、IPv6、反向代理和 localhost 转发仅决定如何访问管理
入口；SIM 的国家出口仍按该线路设置，不由浏览器访问地址推断。

默认 Go VoWiFi 路径为浏览器 → Core 同源 WSS relay → Go Provider 原生 SIP/RTP bridge；蜂窝路径为
浏览器 → Core → Agent PCM。修改版 Asterisk 不属于当前 release 或运行时。每通
电话有独立 owner，准备阶段不发送付费拨号/接听；
只有当前双向音频、实际采集/播放计数及新鲜挑战证据通过后才提交。重复请求不重放付费动作，
同线路跨端或跨通话模式争用会被拒绝；未确认终态前保留占用。

页面关闭、连接中断或媒体证据失效会进入精确终止流程；VoWiFi 由 Go Provider 的精确通话 guard
保留终止所有权，蜂窝由 Core／Agent 的精确调用与租约保护。Asterisk 不参与当前终止流程。界面区分“结束中”和“终止未确认”，不能将 HTTP 成功等同于物理挂断。
麦克风仍受浏览器安全上下文约束：通常使用 HTTPS，localhost HTTP 是浏览器支持的例外；这与
已经删除的媒体 IP 确认不是同一件事。升级后请刷新旧页面，旧 SIP/媒体确认接口已退役。

构建/交付记录应保存源码 revision、artifact SHA-256、release manifest 与生产安装 receipt；只有这些
可验证对象能证明运行来源。

### 受限网络中的下载代理

生产主机需要统一下载出口时，应把“业务出口代理”和“主机下载代理”分开。主机下载代理只负责
Release 和系统包；不得写入 SIM 线路或 VoWiFi 出口配置。

- `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY` 和小写同名变量用于登录 shell、安装脚本、curl、wget
  等下载工具；Git 和 APT 还应配置各自的持久代理。
- 如果上游只提供 SOCKS5，建议复用已发布的 sing-box 二进制，在本机回环地址提供一个独立 HTTP
  CONNECT 适配入口，下载工具指向该入口，适配器的唯一 outbound 指向 SOCKS5。不要为此重新编译
  网络组件，也不要让无认证入口监听公网或局域网地址。
- `NO_PROXY` 必须包含 localhost 和管理网段，避免 Core、Agent 与代理上游形成环路。适配器不可用时
  下载应失败，不能自动回退直连。
- 验证至少包括 curl/wget 连接到本地适配入口、适配器日志显示目标经 SOCKS outbound，以及一次
  release checksum 下载；临时配置随即清理。

代理地址属于单机部署数据，不应硬编码进仓库；具体值保存在目标主机权限受控的环境文件、APT
配置和适配器配置中。

---

### 方案 A：不可变 Go artifact 安装（推荐）

GitHub 的 `Go Runtime CI and Release` workflow 会生成一个 Linux tar。普通 main push 的临时 artifact
只用于 job 间验收；只有精确 `v*` tag 在 Linux fresh-host、Windows 和 macOS 门禁全部通过后，才把三平台
包及 `SHA256SUMS` 发布到 GitHub Release。下载后先核对 checksum。Linux tar 的外层包含
`install-release.sh`，内层只有一个严格 release 目录；manifest 记录每个二进制、systemd unit、
Provider 对应源码和许可证的大小、模式及 SHA-256。目标机不需要 Git checkout、Python 或 Docker。
目标宿主必须提供 Linux systemd、`realpath`、`curl`、`openssl`、coreutils `timeout`，以及标准的
`/usr/sbin/useradd`、`/usr/sbin/groupadd`、`/usr/sbin/nologin`。离线 tar 入口还需要 Bash、`tar`
和 `find`；缺少依赖时安装器会在改变服务运行状态前失败。

```bash
mkdir mdd-release && tar -xf mdd-<revision>-linux-amd64.tar -C mdd-release
cd mdd-release
sudo ./install-release.sh install "$PWD/mdd-<revision>"
sudo ./install-release.sh start
sudo ./install-release.sh status
sudo ./install-release.sh stop
sudo ./install-release.sh uninstall
```

六个动作边界固定：

- `install RELEASE_DIR`：校验完整 artifact，原子安装并切换不可变 release；仅 fresh host 创建
  `/etc/mdd` 下的管理员认证、Agent token、TLS 和 Core 配置。已有完整配置逐字保留，运行服务不重启。
- `start`：只启用并启动 `mdd-core`、`mdd-provider-apply`、`mdd-egress`。空 catalog 不启动任何
  `mdd-vowifi@` Provider；已有且明确 enabled 的 Provider 实例会在 Core 就绪后恢复。不会自动启动 endpoint Agent。
- `restart`：明确的维护操作，才会重启上述三个固定服务；部署或更新从不隐式调用。
- `stop`：先停止 apply helper，再停止当前严格识别的 Provider，最后停止 Core 与 egress；不禁用开机启动，
  也不停止独立 endpoint Agent。
- `uninstall`：在二次核对所有托管 unit 已停止并禁用后，只移除严格 manifest 对应的 Go release、稳定链接和
  MDD unit；保留 `/etc/mdd`、`/var/lib/mdd*`、安装/应用审计记录及 `mdd` 用户，便于原数据重装恢复。
- `status`：显示三个 unit，并以 `/etc/mdd/tls/server.crt` 同时完成 CA 和 SPKI pin 校验后请求
  `/healthz`；禁止 `-k`。非默认监听端口可通过 `MDD_STATUS_PORT` 指定。

首次安装必须从 stdin 提供 1–256 个 UTF-8 字符的管理员密码；TTY 输入默认隐藏，非交互部署应
由权限受控的 secret/file 写入 stdin，不能把密码放进 argv 或环境变量。密码不会进入 receipt
或日志。bootstrap 只生成 localhost、严格校验后的当前 hostname、`127.0.0.1` 和 `::1` SAN，
不猜物理网卡、VPN 或默认路由地址。首次生成的共享 Agent token 只用于迁移期；系统设置可按稳定
Agent ID 签发、轮换和撤销独立 token。逐台写入 owner-only Agent 配置并确认新凭据重连后，再切换
scoped 模式关闭共享 fallback；未知或撤销的 Agent ID 将被拒绝，旧活动控制/媒体/数据/USB-IP 会话
同时关闭。transition 期间若客户端更新失败，可显式 unenroll 该 ID 回到共享 fallback；scoped 模式禁止
删除身份记录。Core 不获得 `/etc/mdd` 的目录写权限，认证文件由现有 root helper 经本机 Unix socket
严格校验后原子替换；响应丢失时只用完整文档 SHA-256 读回确认，不盲目重放凭据 mutation。签发响应只
回显新 token 一次，凭据状态、日志和备份均不回显秘密。远程浏览器必须使用
该 hostname（并正确解析），且在受控的系统/浏览器
信任存储中信任这张精确自签证书；也可通过持有匹配受信任证书的 HTTPS/WSS 反向代理访问。
Agent 使用 SPKI pin；这些都不是让用户确认某个接口 IP，也不能以跳过证书校验代替。

### 方案 B：离线安装同一 Go artifact

在线与离线没有两套部署逻辑。将上面的完整 tar 复制到主机后执行：

```bash
sudo ./offline-install.sh install /absolute/path/mdd-<revision>-linux-amd64.tar
sudo ./offline-install.sh start
sudo ./offline-install.sh status
```

`offline-install.sh` 只解包并调用 artifact 自带的同版本 Go installer；它拒绝绝对归档条目、
路径穿越、链接/设备节点、多 release 目录以及不规范的输入路径；它不会探测或导入其他运行时。

---

### 方案 C：Nginx 反向代理默认 Go 入口

Go Core 的页面和 API 使用根路径（`/`、`/assets`、`/api`、`/v1`），推荐给它独立域名并整体
反代。旧 `/mdd` 前缀不受支持。以下示例连接默认 HTTPS
上游；信任文件和 `proxy_ssl_name` 必须换成实际网关证书的信任锚和 SAN 名称。保留原始 Host
（含端口），否则会破坏浏览器同源校验。不要假定默认还有 HTTP 8000 端口。

```nginx
server {
    listen 443 ssl http2;
    server_name your-domain.com;

    ssl_certificate /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    # 将该独立域名的完整根路径交给 MDD Go Core。
    location / {
        proxy_pass https://127.0.0.1:8443;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /path/to/gateway-trust.pem;
        proxy_ssl_server_name on;
        proxy_ssl_name gateway.internal; # 替换为上游证书中的 SAN 名称
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
```

---

## 三、客户端部署与支持边界

Windows、macOS 与 Linux 只使用 release 中的统一 Agent 包，具体命令见
[`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md)。旧轻量 Card Agent、Android VPCD App、Python
`mdd-card-agent.service` 和明文 35963 入口已经退役；不得从历史 artifact 或文档恢复并与统一 Agent 并行。

统一 Agent 在现有认证 WSS health 上协商 `agent-host-health-v1`，上报平台、架构、精确构建、运行方式及
配置所在文件系统的量化容量；不另开端口，也不为健康采集探测 Modem、PC/SC 或音频。系统设置页从同一
topology计算 reader/modem 与隔离状态，Core receipt 超时显示 delayed；scoped 凭据存在但当前无连接显示
offline。旧 Agent 没有该能力时明确显示“此版本未上报”，不得猜测为 Linux 或健康。

### 1. Windows 客户端
安装 [`agent/MODEM_AGENT.md`](agent/MODEM_AGENT.md) 中的统一 `MddAgent` SCM 服务；它同时管理
本机允许的 Modem 与全部 PC/SC/eSIM 读卡器，并通过同一个 CLI/GUI 控制面报告状态。只有安装/配置
变更需要管理员权限，日常状态和受控启停使用安装时创建的 Operators 组。

### 2. macOS 客户端

使用统一 `MDD Agent.app` 或 `mdd-agent`，当前部署默认 `modem_enabled=false`，仅管理多 PC/SC/eUICC
读卡器；不会枚举或接管 Modem，也不会索取其麦克风权限。Modem 代码保留，但其语音和私有数据面
尚未作为当前版本交付，不应自行开启或宣称 Intel/其他 Modem 已通过实机矩阵。
当前 macOS 安装器支持当前用户的 LaunchAgent（GUI／CLI 模式），见统一 Agent 手册；手动启动仍只能
有一个硬件运行时，重复启动固定退出 `9`。GUI 首次运行会把
Token 保存到当前用户的 `~/Library/Application Support/MDD Agent/config.json`；目录权限为
`0700`、配置文件为 `0600`。关闭状态窗口只隐藏到菜单栏，选择“退出 MDD Agent”才释放硬件。

先在系统设置为该客户端的精确 Agent ID 签发独立 Token。CLI 会优先使用配置文件中的 Token；文件未配置时，依次使用 `--token`/`--token-stdin` 和
`MDD_AGENT_TOKEN` 作为当前进程的临时回退。`config set token --stdin` 与 GUI 的 Token 窗口写入
同一配置文件，不存在两套状态。只有后续明确启用 Modem 的实验模式才检查音频权限；纯 SSH
没有桌面会话时仍受系统授权限制，不能将其当作 PC/SC-only 客户端的启动要求：

```bash
./mdd-agent config set server gateway.example.com:8443
printf '%s\n' "$MDD_AGENT_TOKEN" | ./mdd-agent config set token --stdin
nohup ./mdd-agent run >mdd-agent.out 2>&1 &
./mdd-agent status --json
```

发布包携带所需运行时，不要求客户安装 Python 或 Homebrew。包含实验 helper 不等于完成
Mac 蜂窝隔离/通话验收；当前支持边界以 PC/SC-only 和正式发布矩阵为准。

### 3. Linux / 树莓派 / NAS 客户端

嵌入式设备可另选[手动部署与 crontab 保活](agent/MODEM_AGENT.md#嵌入式设备手动部署)：自行下载、
核对、放置程序并配置连接，不要求 reader Agent 使用 systemd。现有运行/隔离逻辑不改，
ARM64/其他 libc 工件及无 systemd modem 支持不能从该说明推定。下面仍是原 systemd 安装方式。

当前 Linux release 已包含统一 Go `mdd-agent` 与 `mdd-agent.service`，支持 PC/SC／eUICC
读卡器的远程高层协议。服务端安装只放置二进制和 unit，不会自动启用 endpoint Agent；先在设备上
生成 owner-only 配置并写入服务端为该精确 Agent ID 分配的独立 token 与证书 SHA-256，再显式启动：

```bash
sudo install -d -m 0700 /var/lib/mdd-agent
sudo mdd-agent config init -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set server gateway.example.com:8443 -config /var/lib/mdd-agent/config.json
printf '%s\n' "$MDD_AGENT_TOKEN" | sudo mdd-agent config set token --stdin -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set tls_sha256 "$MDD_TLS_CERT_SHA256" -config /var/lib/mdd-agent/config.json
sudo systemctl enable --now mdd-agent.service
```

Linux 已有原生 ModemManager／AT 适配和受隔离数据路径，并非只有读卡器。数据路径目前只接受受支持的
静态 IPv4 bearer；DHCP／PPP／IPv6 保持 fail-closed。隔离由 nftables、设备组、cgroup 和 socket mark／策略路由
实现，不能把它写成已实现 netns 或已通过完整跨设备无泄漏矩阵。raw USB 功能有独立身份和授权门禁，
必须绑定精确 Agent、equipment、ICCID 与 generation；PC/SC/eUICC 不自动切换到 raw USB 模式。
平台／HIL 未闭环项以 [验收台账](docs/status/README.md) 为准。

### 4. Agent 本地 SIM PIN

SIM 配置页先读取当前卡的 PIN 状态和剩余次数。页面只在剩余次数大于 2 时开放一次验证，并明确区分
“仅验证一次”和“验证并保存到 Agent”。保存值只写入当前 exact Agent 的 owner-only 配置，Core 不持久化
PIN；旧 Agent 未协商 `sim-pin-config-v1` 时保存入口禁用。移除保存值需要当前配置 revision，只修改 Agent
配置且不向 SIM 发 APDU。任何 unknown 结果都不得重复提交，应重新读取状态和 Agent 保存状态。

---

## 四、语音通话与软电话使用

1. **网页端软电话**：
   * 在控制台侧边栏点击 **软电话 (Softphone)**。
   * 支持通过浏览器麦克风直接拨打或接听来电。
   * 麦克风需要 HTTPS 或浏览器认可的 localhost 安全上下文；媒体使用同源 WS/WSS。
2. **独立 SIP 客户端（MicroSIP / Linphone / Zoiper）**：
   * 当前不支持。旧 SIP WebSocket／Asterisk WebRTC 和媒体确认入口已退役；不能恢复旧端点
     或使用缓存凭据绕过当前 native owner 发起通话或短信。
   * 不要通过重新暴露 8089 绕过该门禁。未来如需独立 SIP 客户端，应实现独立的受控
     admission 和断线挂断生命周期，而不是复用内置浏览器凭据。

---

## 五、线路诊断与支持包

- “诊断”页先选择一条已保存线路，再显式点击刷新；页面不会自动轮询日志。
- 日志只包含最多 500 条类型化 Agent/Provider/Core 状态事件，不读取或暴露 systemd journal、Windows
  Event Log、macOS Console、原始 SIP、AT 或 APDU 内容。
- 页面可按来源筛选并下载同一份脱敏 JSON。全局支持包同样只包含结构摘要和总计有界的脱敏事件；不包含
  SIM 身份、电话号码、设备序列、凭据、网络地址、本机路径或密码字段。

回收站中的线路可以永久删除。操作前必须先禁用线路、Apply 使 Provider 退出，并确认没有 raw USB、通话、
浏览器媒体、蜂窝数据、余额查询或通知投递 lease。页面要求输入完整线路 ID，可选择保留已结束的短信与通话
历史；开始后该选择被冻结。中途失败时再次确认会续接同一 operation，不得 Restore 或新建同 ID 线路。
该操作只删除 MDD 控制面数据，绝不删除物理 SIM/eSIM profile，也不向运营商发送注销请求。

---

## 六、上游代码同步与维护 (Rebase / Cherry-Pick)

本项目的所有改动均遵循**原子化清晰 Commit 规范**，便于未来拉取上游（Upstream）更新并进行重放或 Cherry-Pick。

### 1. 关键功能提交索引 (Commit Map)
* `build: include compiled webui dist bundle for standalone deployment` — 前端 WebUI 独立分发产物
* `feat: complete dual auth, VoWiFi outbound fixes, offline package and cross-platform Go agents` — 双模鉴权、IMS 呼叫修复、离线安装包与 Go 跨平台客户端
* `fix: auto-derive smartcard reader IMEI and promote draft lines` — 读卡器 IMEI 智能推导与草稿线路自动激活
* `fix: allow present Virtual PCD readers in unified devices` — VPCD 虚拟智能卡通道设备识别
* `fix: precise lpac reader index resolution and rich profile display` — eSIM LPA 读卡器精准寻址与 Profile 状态展示
* `feat: embed sing-box, lpac binary and host module into container` — 内嵌分流代理与 LPA 核心依赖

### 2. 追平上游主干的标准流程 (Rebase / Cherry-Pick)
```bash
# 1. 添加上游原始仓库
git remote add upstream https://github.com/MddIdd/mdd-sim-gateway.git

# 2. 获取上游最新提交
git fetch upstream

# 3. 基于上游最新分支重放我们的增强提交 (Rebase)
git rebase upstream/main

# 4. 若遇到局部冲突，可使用 Cherry-Pick 单独拣选特定功能
git cherry-pick <commit_id>

# 5. 测试无误后推送到自己的私有仓库
git push origin main --force-with-lease
```
