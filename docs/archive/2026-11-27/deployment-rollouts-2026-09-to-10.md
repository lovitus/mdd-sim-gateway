# Historical Deployment Rollout Records (2026-09 to 2026-10)

> Archived from `DEPLOYMENT.md` on 2026-11-27. Preserves historical rollout receipts and SHA-256 provenance; see root `DEPLOYMENT.md` and `docs/status/README.md` for current status.

## 2026-10-23 eSIM 历史位置页面验收

生产 Core `d3ebc0221b6c` 的实际 Chrome 页面已确认在线／历史读卡器分组；历史项标明只读和
观测时间，同一 SIM 的“查看当前接入”跳转到在线 ARM 读卡器。当前 Agent 未上报 eUICC 管理
信息时明确提示未确认能力，不继承旧缓存。另一个含 profile 的历史桌面快照中，禁用、重命名、
永久删除均禁用，也没有停止当前线路的按钮；独立删除记录仍可查看，不冒称整个区域没有副作用。
本次没有点击读取、停止、profile 操作或通知重放，不改配置／pin／用户开关／隔离，无付费操作。
这补齐 PR25 的实际页面显示／导航证据，不证明本次物理移机或 EID 硬件操作／恢复。
原始截图和 AX 留在私有目录；三个截图 SHA-256（历史跳转前、当前接入、含 profile 的历史项）：
`d02df7eaf7b549c1fcea8fbd32215f1964632cbcf196e618e2e8542e00eb1407`、
`85d615826dfd686398a0159a3839a1f35ea5a05fe8b4e1788d08b235145ac3b8`、
`8962e1c24c24d5fafc956a03a127cee4a843e06cf8708653797f103841165fa2`。

## 2026-10-28 v2.1.0 正式发布（非生产升级）

PR53 `d0fc89c` 通过原终审和正常 Go `40636084731`／Android `40636084681`；
实际测试 `c5838e4` 与合并 `90e9881a8551fa36b23e52f0aa01ef070f752bf1` 同树
`617cc7469500b66ff48541f90114924deebe6ba4`。两个正式 tag 均固定在该合并提交。

| 发行 | 原 tag workflow | 真实结果 |
| --- | --- | --- |
| [Android 2.1.0-preview](https://github.com/lovitus/mdd-sim-gateway/releases/tag/android-preview-v2.1.0) | [40638015971](https://github.com/lovitus/mdd-sim-gateway/actions/runs/40638015971) | 全部通过，v228，原包名／稳定签名，非 debug；prerelease |
| [Go v2.1.0](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v2.1.0) | [40639141778](https://github.com/lovitus/mdd-sim-gateway/actions/runs/40639141778) | 全部通过，五类包、两个 Linux 别名及两份清单共八资产；Latest 读回确认 |

先完成 Android 的七个公开文件下载及校验，再发布同提交 Go；Go 八个公开文件均已下载校验。
两份 RELEASE.json 的 source/tree 与上述提交精确一致；Linux 两个别名哈希相同。
macOS arm64／Universal 本次实际签名均为 Developer ID、Team `8WPJLUNLY8`，未公证；
Windows 仍未做 Authenticode 签名。ARM64 包含 CLI/helper/原 unit/首装工具，不扩大实体硬件验收。
Go 首次等待达 600 秒上限，第二次对同一 run 返回成功；没有重跑 workflow。
Android／Go 索引 SHA-256 分别为 `be9d9729c15da87fafb8fe11479738ab1e983f0adf6c7fa4803d93ec91849b9d`、
`ee54fa29f68f112d7cbede753379c7614456647d6d5e87d225cd8f847bfa5850`，各文件哈希见公开清单。
未覆盖旧 tag/资产，未升级或重启现场设备，未改变配置、pin、开关、隔离或借用，未重复付费测试。
PR48、Windows 根因未明、手机跳过及硬件／耐久延期保持原处置；不能以发布成功关闭完整目标。

## 2026-10-28 出口维护与独立 Windows 诊断

用户专项授权的共享出口维护已完成一次受保护应用：仅修正已批准的 GB 节点入口，
HK consumer 重新使用原 donor 当前端口；保留 donor session/generation、配置、pin、
用户开关和隔离。Core、Agents、Providers 未重启；共享出口子进程按维护计划替换。
01:58 UTC 有界观察及真实 Chrome 确认两条当前 GB 线路已恢复 IMS 与页面能力就绪。
本地 SOCKS 认证没有发 CONNECT，不代表蜂窝 WAN 吞吐；未拨号、发短信或扩展预算/故障验收。
原评审接受限定回执；原公网路径故障原因未因此确诊，不重复该维护。
私有 apply 回执 SHA-256 `538b96a494bc11d3333d0df66ec350d0b0b118a8feb76e254b443469054880fc`。

另一台 Windows 的独立故障已有范围明确的恢复：04:27 UTC 原 EC20/SIM/AT
就绪而控制链路失联；旧 EasyTier 高句柄及残留 Proxifier 模块是异常证据，不能等同
端口耗尽或唯一根因。用户授权的一次正常退出/原样启动已经执行。07:10 UTC 旧
EasyTier 退出后，原 MDD Agent **未重启**便重新连上 Core；07:21 UTC 服务端确认
原设备/卡/代际，数据断开和隔离保护、用户开关及配置保持。恢复早于新 EasyTier
进程启动；读回显示使用原有 TUN 路径，不能称为新 Mesh 已恢复或根因已修复。
原评审完整接受这个限定结果。用户后续自行处理远程桌面并要求不再恢复，已停止
该维护，不重复重启或付费测试。第四台 Windows 的 PR47 UI 升级仍未完成。
原始身份、配置和日志只保留私有证据；诊断报告 SHA-256
`c8e0e2564eba3c489313e39a56045d1417b811e5b0c249c3fbfe3e7fba431f06`。

## 2026-10-22 PR46 合并、Core 部署与限定页面验收

批准头 `d0ed08d`、正常合并 `5a6c7e2`、实际工件 `d3ebc0221b6c` 的完整 tree
均为 `c6afc0cd2bd3b53cce0d473546ce997c1fb88a25`；原终审、Go `39747311051`
和 Android `39747310982` 通过。复用该 Go 工件，22 项 manifest 及整包摘要核对完成，
没有重建或替换公开 release。整包 SHA-256：
`b4688766a70a72a99445ffbbfd0e4746e3c9d2448ce2f40eeb118f257c484999`。
运行 Core SHA-256：`a43db29692cd8e52277f89df32e3e2fbe1db53f4c5d8aff08de572bf9404b501`。

用户专项授权原 owner 短暂停借用并恢复。准确租约在原清理链在线时停止、Core/Agent
清理确认后，维护内离线备份并记录哈希，再由标准安装器发布 shared release。
已核对的 Core/apply/egress owner/Agent/十个 Provider 中仅 Core PID 改变；其余
已核对进程的运行映像和 PID 未变，不据此断言出口子进程未重建，也不把
shared 发布当作这些进程已升级。同宿主 Agent 的 PR45 独立启动目标及 guard 参数保留。
配置/pin 文件、catalog/IMEI 池/通知/egress 配置和策略 revision6 保持；临时 pin、维护
清零，updater watcher 恢复。原 owner 恢复借用，按同一 generation 协调新端口，
部署前已有的 HK `runtime route changed` 收敛为 `runtime_confirmed=true/pending=false`。
原 `catalog37/applied36` 差异未应用。两条 GB 线路恢复窗口曾报 `userspace_stack_failed`；
保留失败快照，09:23 UTC 自动恢复通话/SMS 就绪，无人工重试，不声称零中断或实际通话通过。
六个原连接身份同代际返回（包含一个 Android）；Mac CLI 短时缺席后自返，不证明连续在线。
更正此前 08:44 回执的“六台桌面/Linux”：实际是五台桌面/Linux 加一个 Android 身份，
并非完整六台桌面机群都在线；另一 Windows 身份在切换前快照也未出现，未新增操作。

用户处理浏览器信任后，Chrome 硬刷新、重新登录并实际回看概览、设备/VoWiFi 历史、
通话选择/+号输入、短信历史及原 SIM 不可证时的拒绝回复、IMEI 绑定、eSIM 缓存门禁、
通知已保存配置/既有 delivered 日志、诊断及添加 Agent 表单；未签发新身份或保存配置。
09:27 UTC 固定 pin 复核实际服务的 `app.js` 为 627020 字节，SHA-256
`7b5ee310345381ddd77b51e55e3615351f0069c4afd8d1c3a95f7fe790c956a8`，与资格资源一致。
当时无 `swu_authentication_failed` 现场样本，未制造失败；精确新文案的中英回归仍归属
下方 hosted proof，不冒充现场负例。无付费、主动网络测试、PIN/profile、手机操作或延期扩展。
原评审完整回执接受本次限定收尾；其结论依据执行回执，不冒称独立访问了私有现场证据。

### 本批资格及既有诊断记录

本批只修改 WebUI 认证失败提示和当前台账，不改认证、重试、路由、用户开关或隔离。
候选提示只表示 ePDG 返回认证失败，不能据此认定 SIM、国家、IP 或具体运营商策略；
历史结构化详情不再覆盖成过度归因，原始码仍可查看。原评审已批准限定方案；
候选构建、浏览器验证与生产版本分开记录；随后经用户专项短停授权完成上方部署。
限定 proof `ffc8a88` / `39746284080` 已通过，保留于
`archive/2026-10-22/status-closeout-qualified`；中英实际组件回归和生成的 embed 已核对。
旧版反例在主状态缺少文案处失败，不声称每个历史断言都分别取得旧版反例。

10 月 22 日诊断处置：日志显示 04:08:43 UTC 停止单元成功、06:01:50 UTC
提前清理单元成功；07:12 UTC 只读检查已无本次目录、探针、实例或临时单元。
事件及结束回执已不可取，不能证明完整 48 小时、零事件、零丢失或准确提前停止原因。
当时 Linux AT、策略和借用就绪，数据 connected、隔离 protected；最初断口原因仍未查明。
这取代“采样仍运行”的旧状态，不重开采样、不制造故障。私有证据只保存在工作区外。
08:20 UTC 固定 pin 的只读复核仍为同一 Linux Agent 代际、策略 revision6、
借用 ready、数据 connected/guard protected；catalog、通知配置及 Core 映像/PID 未变。
这是现有链路状态读回，不是新文案生产页面、实际音频或数据吞吐验收。

简化部署已验与未验分开：PR41 的真实 Chrome 创建身份、Mac GUI 导入、0600 落盘、
退出后 CLI 指定配置复用及撤销已通过，见下方原回执；这不覆盖空配置 `run` 的首次交互、
默认路径落盘和再次无参数发现。本批上述 proof 使用公开 v2.0.0 Linux amd64 包，
SHA-256 `dc5fd8cd76321ea3c04be8d4b17d402d9add7365a6f6d4edcd8d7afc8184ab69`，
对独立 loopback Core 完成真实 PTY `run` 首次交互、Web 登录、证书 pin、默认路径 0600
落盘、正常退出、无配置参数再次发现并复用身份、最后撤销测试凭据。成功回执非空且逐项核对。
未接入生产、硬件或现有配置；不把该 Linux 旅程推广为三平台安装、USB 或业务验收。
初始夹具缺接口、CLI 解析错误被管道掩盖、PTY 匹配超时均已保留，不算通过；
对应 run/tag 见 docs/branches.md。临时入口已移除，最终正常 CI 与终审见本节开头。

## 2026-10-19 PR45 Linux 策略恢复

PR45 批准源码 `9bf6571`、完整 CI `39212919170` 和原终审通过，正常合并 `90fde687`。
按用户专项授权，仅故障 Linux Agent 升级到工件源码 `bab742e5269a`；三者完整 tree
均为 `1df9481c16bd09b288613edd4949bf4058aa91b7`，22 项 manifest 已逐项核对。
实际 Agent SHA-256：`a467f974f919433d22a6c535e3afe1de08b71c116bcb489b6d05ecf942545acf`。
原借用 owner 停止、清理确认后，经原受保护重启入口切换；旧工件及失败证据保留。
08:35 UTC：AT/通话信令/SMS 就绪，策略 `ready/policy_ready`，原借用租约与出口恢复。
出口子进程连接新租约端口，配置摘要与 executor 回执一致，`runtime_confirmed=true`；
Chrome 诊断及 FR/GB/HK 出口页面确认当前展示，没有执行网络测试或保存配置。
原有 catalog/applied `37/36` 未应用差异保留，没有为恢复出口顺带应用其他配置。
配置/pin/guard 文件哈希、策略 revision6/用户开关、完整 catalog/通知配置及 Core PID 未变；
仅目标 Agent 与获准暂停的 egress PID 改变，其他 Agent 代际和 Provider PID 保留，零遗留维护。
ARM64 已在前一批升级 v2.0.0；本批不重启，原进程的读卡/卡路由/IMS/短信能力新鲜就绪。
**因果边界：**08:31 UTC 停借用后旧版 AT 已恢复，早于换包；本次证明受控恢复及新版
持续连接与借用共存收敛，不是生产强制断口自动恢复的完整复现。该路径的确定性证据见
`archive/2026-10-19/linux-policy-qualified` / `39212493452`；最初 MM 断口原因仍未知。
首次 SSH staging 超时发生在远端执行前，重连成功后才暂存；原失败未覆盖。
无 Core/ARM/手机升级、付费/PIN/profile 操作或延期测试扩展；隔离读回不代表全时段无泄漏。
本节取代下节当时的 Linux 未解决状态；公开 v2.0.0 tag/资产没有修改。

## 2026-10-19 v2.0.0 生产与 Agent 升级

按用户后续授权，使用下节已发布的同源 `9a139f81647a83224f04b8c30f06279176061bfb` /
tree `32bcc859197f28c650f80f0766ea53e6104ca4f3`，沿用 Go `39110411762`、Android
`39109678131` 的合格公开工件；未重建、移动 tag 或替换资产。后续文档提交不是运行源码。
Core/apply/egress、10 个运行中 Provider 及六台桌面/Linux Agent 已升级：两台 Mac、
两台 Windows、同宿主 Linux、ARM64 reader-only Linux。服务器核对的 14 个实际映像是
前述 13 个服务进程加同宿主 Agent；该 Agent 已计入六台，不重复相加。
已核对实际进程/映像、原配置与证书 pin、Agent 身份和当前卡身份；Mac 原签名保留。
服务器最终 Core/Agent/Provider SHA-256 分别为：
`8fed8b0bbc09365e7ea591e02908d64650bb8babbee684203b1c79592731fe9e`、
`7e957e9c5856cc68a7bb004e50f459d4177e373765582644f2f263de8cfcd509`、
`068ca47b6da1d368c452534dfac3f11e8a7c12ded32a7bca6a3cd08bc2f195a3`。

原借用 owner 正常停止且远端清理完成后才停相关服务，之后按原配置恢复 donor；
最终安装 journal 为 applied，零遗留维护租约，本批临时启动覆盖已移除，更新监听器恢复。
Linux donor 与 AT/通话信令/SMS 能力 ready，但策略仍为
`recovering/cellular_connection_reconcile_failed`，原因未定，保留在既有延期项。
这是当时的状态；后续 PR45 受控修复与验收范围见上节，不再代表当前故障。
升级前 `cleanup_failed`/AT unavailable 与此后状态分别记录，不断言同根因或故障已修好。
guard active、main 表无蜂窝默认路由仅是本次读回，不是所有路由/流量或持续无泄漏证明；
原 donor 仍有正常数据会话，零遗留维护不等于全局无数据会话。用户开关、线路和通知配置未变。

真实 Chrome 已检查概览、设备、IMEI 池、通话、短信、eSIM、网络出口、通知、设置和诊断；
历史记录及保存状态可读，拨号 `+` 输入后清空未呼叫。当前 giffgaff 读卡位置为 ARM64 Agent，
两条当前英国线路卡路由/IMS 就绪；这不证明新通话/SMS 或耐久。零付费、PIN、profile、通知重放。
指定手机保留数据安装公开 v182，原签名/UID、版本及 APK 摘要相符：
`779f9db3b975421add643dcd9e9a4ba9318eb7a969e38a9b7ddeb06196b2555b`。
更新后应用重连未确认；用户明确当前手机无读卡器，本轮应用内、重连及读卡验证跳过，
不列为待交付门槛，不宣称手机业务通过。其他未授权 Android 身份未操作。

失败记录保留：服务端首轮因独立 Agent 链接不符被拒并恢复旧服务，原安装 journal 经既有
recover-release-install 收敛后才正常安装；无数据库倒灌。首台 Mac 重名拒绝后的错误补偿
确实发生 `59f8dd -> 38af -> 9a139` 切换，不写成无影响；中间业务影响未单独量化。
随后私有等待把源码 SHA 错当语义版本导致假超时，纠正观测后验证实际映像，没有因此重装。
旧版本、离线备份和原始失败证据保留在私有记录。原评审接受上述限定收尾；
未解决及延期项不批量关闭，本批仅记录更新，不再运行 CI 或扩展现场测试。

## 2026-10-18 v2.0.0 正式发布（非生产升级）

PR44 `e9de823` 通过原终审及正常 Go `39108641098`、Android `39108640988`；
正常合并源码 `9a139f81647a83224f04b8c30f06279176061bfb` 与批准源码完整 tree
均为 `32bcc859197f28c650f80f0766ea53e6104ca4f3`。两个发布 tag 固定指向同一合并；
按项目约定不移动 tag、不替换公开资产，不代表 GitHub 平台锁定（两份 Release 的 `immutable=false`）。

| 公开发行 | 发布 workflow | 实际结果 |
| --- | --- | --- |
| [Android preview 2.0.0](https://github.com/lovitus/mdd-sim-gateway/releases/tag/android-preview-v2.0.0) | [39109678131](https://github.com/lovitus/mdd-sim-gateway/actions/runs/39109678131) | 成功；versionCode 182，原稳定签名与 application ID，非 debug；prerelease，不占 Latest |
| [Go v2.0.0](https://github.com/lovitus/mdd-sim-gateway/releases/tag/v2.0.0) | [39110411762](https://github.com/lovitus/mdd-sim-gateway/actions/runs/39110411762) | 成功；四个平台包、Linux 版本别名、索引和校验清单；Latest 读回为 v2.0.0 |

两份 Release 各 7 项公开资产均已下载，完整 `SHA256SUMS`、`RELEASE.json` 的源码/tree
及包内 metadata 已核对；Linux 版本包和 revision 包逐字节相同。Mac 沿用原 Developer ID，
未公证；Windows 未做 Authenticode 签名；ARM64 仅 reader、glibc >=2.39。
签名与校验文件随各自 Release 提供，安装入口见 [README](README.md#-快速开始)。
两次 tag CI 均在第二个有界等待内完成，没有重跑 workflow。

10 月 18 日这批只是公开发行和下载入口收尾，没有升级/重启 Core、Agent 或手机，没有付费或卡片操作。
当时实际手机仍为下文 PR43 v178，Core 和各 Agent 按各自已接受回执；后续授权升级见上节。
历史硬件、录音、耐久及系统更新连续性未验项保留，不因发布成功视为通过。
原评审随后核对实际发布元数据及 `5b8f782` 文档回执，确认本批可以收尾；
仅澄清上述固定 tag 的措辞，无需改发布设置、重新发布或追加测试。

## 2026-10-16 ARM64 reader Agent

PR40 候选 `5552ebe` 及原评审通过；正常 CI `38860462972` 在一次同源码失败任务重跑后成功。
首次失败是既有读卡并发测试的槽位等待超时，保留原证据；调度竞争只是待证解释，未修改门禁。
已验证工件源 `83af89c3` 与候选 tree 同为 `147ebb73`，保留于
`archive/2026-10-16/linux-arm64-reader-qualified`。本条记录当时的独立交付，未代表当时已合并 main。
仅新授权 Debian 13 ARM64 主机安装 PC/SC 依赖及现有 headless Agent；独立 scoped 凭据、
固定证书 pin、0700/0600 配置和单一 systemd owner，modem/raw USB 关闭。
06:50-06:51 UTC 本机与 Core 新鲜读回确认唯一当前读卡位置、卡身份、EID 和三个 profile；
真实 Chrome 设备/eSIM 页面指向新主机。原英国线路配置未变且自动恢复注册，另一英国线路亦就绪。
这不等于新通话、短信、profile 修改或耐久验收；未输入 PIN，未升级/重启其他 Agent 或服务端。
Tar SHA-256 `57247e826390fa24343e468f2a449ae30b68e109cab3f1d3b51fd358e5d398c0`；
实际 Agent 映像 `a8b9edbdc5e36a2d7e320ba5169535560d72af2ee4f678defe06892a6fd0a414`。
包和实机逐项哈希相符，unit enabled/active，重启次数为 0；原始主机/凭据证据仅存私有记录。
10 月 18 日集成源码 `a42cb98` 原终审、Go `39102084430`、Android `39102084411` 通过；
正常合并 `26e1997` 与新工件源 `0a4cfd93` 同树 `632ef673`，交付 tag 见分支台账。
新工件内外哈希已核对，详情见 [PR40](https://github.com/lovitus/mdd-sim-gateway/pull/40)。
本次仅合并打包支持，没有重新部署、重启设备或付费/卡片操作；上述已安装版本保持不变。

## 2026-10-18 PR43 Android USB 权限恢复

`3ceada0` 原评审、Android `39099064731`、Go `39099064725` 和签名构建
`39099884933` 通过；正常合并 `3108bea` 同树 `c25caed5`，交付 tag 见分支台账。
仅更新用户指定手机到稳定签名 v178，旧 APK 和初次 CI 失败证据保留。
APK SHA-256：`2d41ce1d8f4fde7f4e78f5f1920742fdaa5eaffab2ac4edf9e0002f616603730`。
更新前实机读卡器仍枚举、缺少 USB 权限；新版启动后自动弹出系统授权，
没有点击 Readers 手动连接按钮。确认原读卡器授权并选择系统默认打开后，
普通扫描恢复卡片，实际 Readers 显示识别 1、需处理 0；05:30:28 UTC 固定证书
读回确认原 Agent 身份、新进程、卡路由及 IMS200、语音/短信能力就绪。
原可用/共享意图保留；没有更新 Core、修改线路或执行付费、PIN、profile 操作。
系统确认由本次验收代为完成，不代表绕过权限；未来拔插免提示、USB/OEM 耐久和
新通话/短信业务未验。本批仅保留同进程 attachment 尝试预算，未增加跨进程持久化，
不作跨进程保证。原评审已接受本次恢复验收边界；临时镜像已退出，未停止手机共享。

## 2026-10-17 PR41 首次接入与 PR42 新卡修复

PR41 `0ababe4` / 完整 CI `39015455331` 及原评审通过，正常合并 `160c52a`；
部署工件 `aa89ce241f5f` 同树，归档 `archive/2026-10-17/agent-setup-delivery`。
真实 Chrome 添加 Agent、macOS 接入包导入及配置落盘、退出后同身份重连通过。
测试身份已撤销并确认旧 token 返回 401；原 9 份有效凭据、其他进程及业务配置不变。
原 donor 已恢复，维护与临时启动保护移除、守卫与更新监听器正常。
初次恢复路由 apply 的 409 留存，随后原恢复路径完成；没有重复安装或放宽门禁。
Mac 使用隔离路径的开发签名工件，未覆盖生产签名 App；无硬件、付费或耐久验收。
哈希与完整边界见 [PR41 回执](https://github.com/lovitus/mdd-sim-gateway/pull/41#issuecomment-6234721009)。

PR42 `ba6fc803` / Go `39013478171` / Android `39013483690` 已部署：目标手机
v174 的真实 Detect/Save/Enable 达到 IMS200，Core 文案及借用恢复也已完成。
先前失败与回滚见 [PR42 回执](https://github.com/lovitus/mdd-sim-gateway/pull/42#issuecomment-6231895384)。
后续在 PR41 部署前，手机 USB 重新连接后缺少 MDD 授权，Core 已无 reader；
这是后续 PR43 修复前的历史状态；恢复过程见上节，不再等待用户手动连接。

## 2026-10-16 PR39 出口恢复与借用额度

`d267b527` 原评审及完整 CI `38731761585` 通过，正常合并 `521067f`；
工件 `756558dc` 与批准源码及合并的完整 tree 均为 `5b3ad521`。
已更新 Core/apply/egress 和 9 个 Provider；6 个原 Agent 进程代际、用户开关及配置保留。
原 donor 由原 owner 关闭并确认远端清理后才停止 Core；离线备份留存、未倒灌，
维护租约释放、更新监听器恢复。私有脚本先前误查已停服务的两次失败均在安装前回退。
真实 Chrome 登录、额度读取及用量增长已验；保留原隐含 1 TiB，活动 owner 时禁止重置。
三个国家出口 ready，两条英国线路 IMS 200；不代表真实通话或耐久通过。
用户随后确认：HKmobile 实体卡及读卡器是主动移除，不是待修复的失联故障；
另一张香港卡此前的 `swu_authentication_failed` 符合预期，不再要求恢复注册。
这不代表认证成功，也不推定运营商的具体拒绝策略。主状态仍有原始码显示，
待在后续前端批次补齐“运营商拒绝 VoWiFi 认证，未返回具体原因”的中英文提示，
并在详情保留原始码；本次不为文案重启正常线路。
02:38 UTC 固定证书读回和真实 Chrome 逐卡确认：两条当前英国卡的卡路由、
隧道、IMS 和短信均就绪，页面显示 VoWiFi 正常；不重复计算历史读卡器条目。
生产额度写入/耗尽、跨国故障注入未验；没有付费、SIM 操作或 Agent 升级。
精确哈希、备份、失败及验收边界见 [PR39 回执](https://github.com/lovitus/mdd-sim-gateway/pull/39#issuecomment-6206543093)。

## 2026-10-15 PR38 SIM 核验与实际启动

`59f8dd2` / 完整 signed CI `38705192004` attempt 2 及原评审通过，正常合并
`0d74354`，完整 tree 相同。已更新 Core 和指定签名 Mac；其他 Agent 未升级。
首次 Mac 部署因过早检查其他 Agent 回连而自动回退；修正私有等待后同产物成功。
真实 Chrome 按主机及当前卡识别，保留原 unknown，显式新只读核验后启用，
catalog/applied 均为 25，IMS 200，IMEI 正确回显；无 PIN/eSIM 修改或付费操作。
随后确有运行中断。系统日志确认旧 MDD Wi-Fi cron 周期性主动重连宿主网卡；
用户授权哈希备份后仅暂停该任务。一次十分钟只读窗口中 Agent/卡路由保持在线，
末尾现有恢复器自动恢复 IMS 注册，但连续就绪时长判据超时，不能称为耐久通过。
首个底层隧道错误及后续 IKE 失败归因仍未完整留存；瞬断时编辑器连续性也待处理。
源码、工件/进程哈希、失败与回滚边界见 [PR38 回执](https://github.com/lovitus/mdd-sim-gateway/pull/38#issuecomment-6188828740)。
以下 PR37 及更早条目为历史，初始化未通过的结论已由本条按范围更新。

## 2026-10-15 PR37 当前卡一次启用入口

`6c0177c` / 完整 CI `38696701616` 及原评审通过，正常合并 `fd1847a`；
部署工件 `a1755279` 与其完整 tree 相同。只重启 Core，6 个原 Agent 代际恢复，
其他组件进程、配置及待应用状态保留，维护锁和更新监听器恢复。
首次维护准入返回 `line_not_drained`，未切换程序，已确认部分租约释放；
有界只读等待后一次受保护部署成功。首次逐线拒绝原因缺失，不补造原因。
真实 Chrome 已登录并按 ICCID/Agent 选中目标卡，新保存/启用入口可用。
一次启用在只读核验处返回 `unknown/reader_provision_response_invalid`；
读取原回执未重发，独立检测返回 `reader_readback_secure_element_failed`。
卡仍在场，线路仍为停用草稿；没有继续启用、应用、启动、PIN/eSIM 修改或付费操作。
新卡启动未验收，需修正 SIM 核验对 eUICC 信息读取的耦合及错误覆盖。
精确摘要、备份和边界见 [部署回执](https://github.com/lovitus/mdd-sim-gateway/pull/37#issuecomment-6186935747)。

## 2026-10-14 PR36 新卡配置流程

`df63ab6` / 完整 CI `38543615650` 和原评审通过，正常合并 `8fe8dbe`；
部署工件 `022c7e9` 与其完整 tree 相同。规范安装入口只重启 Core，6 个此前在线
Agent 原代际恢复；其他组件 PID/映像、目录、IMEI 池、通知及用户配置不变。
原维护锁释放，空闲更新监听器恢复启用/等待，既有待应用配置和通话历史保留。
固定证书读回 20 个页面数据接口、11 条线路接口及线上资源通过；零付费/SIM 操作。
Core SHA-256 `8f21a14af2eb0c9aa35c0a7dd35e4284ad03713df6d05ab14784f43dd37d92fc`；
app.js `e70b54cbd9d7b0193984b379c8814b3d3837384dc292415524e8cf1e40164758`。
首次上传超时未改变服务；同产物压缩重传后成功，旧版/离线备份/失败证据均保留。
生产 Chrome 仍返回证书警告，未绕过；实际页面点击和新卡初始化/启动未验。
当前停用草稿未自动启用。完整脱敏 [回执](https://github.com/lovitus/mdd-sim-gateway/pull/36#issuecomment-6165782776)。

## 2026-10-14 PR29-35 整合部署

五批通过 PR34 整合；首次部署发现更新器维护接口鉴权头不匹配，安全回退后由 PR35 修复。
最终 `431fa6b` 完整 CI `38516014673` 及原评审通过，正常合并 `77f23e0`；
实际 GitHub 工件 `261d8e7` 与其 tree 均为 `b7f33640`。只重启 Core，其他组件映像/PID
不变，4 个此前在线 Agent 原代际恢复；目录、IMEI 池、通知及配置不变，原维护锁释放。
Core SHA-256 `790e9ebb57406a2b4353e8eb22a804f4f0fcaa2b6fca7f8bb08c353de924ea09`；
updater `79cd1d1edc42df54598863404704bdf43e764a1bd86e08990816a3e4ddfed56e`。
实际维护 IPC 返回 200；独立核对无请求/状态/维护文件及 worker 后，监听器启用并等待。
固定证书读回 20 个页面数据接口、10 条线路可用性接口及线上 app.js 摘要通过。
原待应用配置及通话历史不变；旧版、离线备份和失败回执保留，无数据库倒灌或付费/SIM 操作。
生产浏览器点击待用户处理证书提示；新卡启用、真实 eSIM 操作、完整应用内更新重启链未验。
当前 eUICC inventory 为空，前后 Agent 快照均无 EID，不据此推定原因或声称 eSIM 验收通过。
完整脱敏 [回执](https://github.com/lovitus/mdd-sim-gateway/pull/35#issuecomment-6161777301)。

## 2026-10-12 PR28 硬件页 IMEI 回显修复

PR28 精确 `f1c2708` 三方通过，完整 CI `38199997050` 成功，正常合并 `9a9aa78`。
Core-only 工件 `2b66b073` 完整 tree 与批准/合并源码一致；实际进程 SHA-256
`77619a88ef7fd55ba8490553087c3b1ab7a79517ba82df91be3c8693d4f99e1c`，
线上 app.js SHA-256 `0a569e39bd5feeafa98fa0ff7409a5107ef4bfdc2c4e14505f8d208c29cef3ff`。
5 个此前在线 Agent 原代际恢复，其他组件 PID、用户配置、线路、池及通知不变；租约全部释放。
离线 catalog 备份 SHA-256 `05c3639af492b5b0033700df34cf7ee60594f46345c7a79a809c32e71632eda6`。
固定证书读回确认原 pending 绑定及在场设备，未重复绑定/执行硬件或付费操作。
实际 Chromium 页面回归使用合成 HTTP 数据；生产点击仍受浏览器证书校验限制，未冒充已验。
脱敏部署回执见 [PR28](https://github.com/lovitus/mdd-sim-gateway/pull/28)。

## 2026-10-11 PR27 设备页 IMEI 保存补修

PR27 三方通过后正常合并 `ef0d37d`，完整 CI `38113306638` 通过。
仅更新 Core，实际工件 `9b33e9c` 的完整 tree 与合并源码一致；进程 SHA-256
`73c7d2711344cb55ff42a17aaa50ebdf39496bfaff52c9bf0f718577b63d46ff`。
线上 app.js 与 CI 生成资源一致。7 个 Agent 原代际重连，其他服务 PID、线路、池、
通知及用户配置未变，维护租约释放；新卡仍在场、已识别，原 pending 绑定保留。
离线 catalog 备份 SHA-256：`d12b3aa73d585d208bdb57583bd75e6dcecef41195fbb6d79bf85cda5b72681c`。
没有重复绑定、硬件动作或付费操作。浏览器点击仍未验收，不将保存函数/HTTP 回归
和线上资源读回混称页面点击通过。脱敏回执见 [PR27](https://github.com/lovitus/mdd-sim-gateway/pull/27)。

## 2026-10-11 PR26 Core 部署与绑定验收

用户授权后仅更新 Core。复用完整 CI `38076747031` 的 `4d57296` PR test-merge 工件，
完整 tree 与合并 main `ffffb49` 相同；实际进程 SHA-256 为
`07b3c0b980d577b375e48b21b3b1072743e3789fe5df601b473a8aa63aeb103d`。
先前传输中断未切换服务；首次切换因私有部署脚本无法解析纳秒时间戳而回退，旧版和租约均恢复。
修正脚本后成功：7 个 Agent 原进程代际重连，其他服务 PID、原线路及通知不变，全部维护租约释放。
只切换 Core 的稳定程序入口，共享 release 链接及运行中的 apply/egress/Provider/Agent 保持原状；
未把本次 Core 更新描述为整机所有组件更新。旧程序、配置及离线 catalog 备份保留，没有倒灌数据库。
离线 catalog 备份 SHA-256：`b85d077b1b6bc323e7df995fc6011007826aea156f9d35e6ed443e7cb3c60f1f`。
真实新卡按用户指定池条目绑定返回 200，退出并重新登录后读回一致；未创建或启用线路、
未下发硬件命令或产生费用。线上 WebUI 资源与已审版本一致；浏览器仍有证书警告，未绕过，
不把 API 验收冒充页面点击验收。完整脱敏执行回执保留在 [PR26](https://github.com/lovitus/mdd-sim-gateway/pull/26)。

## 2026-10-09 PR22–24 授权部署完成（保留回退记录）

**最终状态 09:22 UTC：Linux 服务端/Provider 及全部五台 Windows/macOS/Linux Agent 已对齐
`38af2d4`。** 用户重新插拔后，原 eSIM 的 EID、卡身份及 ATR 恢复，Agent 无需重启即可重新识别。
用户现场确认只有一只 eSIM 读卡器；此前两个 USB/PCSC 枚举条目不能等同两只实体 eSIM 读卡器。
接续部署第一次因 SSH 跳转握手超时触发回退，Core 启动后的 API 短暂拒绝连接也已恢复并释放租约。
核对主机密钥及实际 Core 摘要后改用既有 Mesh 直连，再按同一 RPC 屏障完成两台 Mac 升级。
最终实际 Mac 进程摘要与原签名工件匹配，原卡、eSIM profiles、目录、通知和用户意图不变；
全部租约为零，FreeFR 身份及 IMS 注册恢复，原停用线路保持停用。没有新构建、Android 更新或付费操作。
最终私有摘要 SHA-256：`e4c52a7f197ae4f3e2b7957c43181d8484dac4ef2d6fb4338a861ac6b28c1fe1`。
公开 [最终部署回执](https://github.com/lovitus/mdd-sim-gateway/pull/24#issuecomment-6078158307) 取代此前部分部署状态。
以下首次部署、回退和诊断记录保留为历史，不再代表 Mac 待升级或读卡器仍失联。

用户明确授权部署。复用已通过的 main workflow `37771205410`，实际工件源码为
`38af2d406dd8925750c3bf4d946d8bec0350c422`；三平台下载 ZIP 与 GitHub artifact digest 一致，
macOS 保留原 Developer ID Team。没有本地构建、重新触发 CI、更新 Android 或付费操作。

- Linux Core、apply、egress、7 个运行中的 Provider、Linux Agent 和两台 Windows Agent 已更新；
  核验实际 PID 映像摘要、新 Agent 代际、配置、卡身份及持久策略，不以软链接或 Running 代替。
- Linux Agent 和两台 Windows 分别只发送一次 Core 受控重启；Linux guard 保持启用/活动，
  main 路由表无蜂窝默认路由，两台 Windows 持久 WFP 规则逐项不变。
- Linux 安装前两次分别被旧 Agent 固定入口和未完成安装事务拒绝，均回退并核验旧进程；
  随后在旧 Agent 启动保护下使用现有显式恢复入口完成安装，没有修改或放宽安装器门禁。
- 两台 reader-only Mac 在全局 Provider 租约及 Core/apply 停止屏障内安装了新包。
  确认旧 RPC workers 排空后才换版；新进程映像/签名正确，但 CLI Mac 第二读卡器持续
  `discovering`、PC/SC 返回 `No smart card inserted`，未通过原卡身份读回，因此两台均回退。
  原版 `bca3dce` 下该读卡器仍有相同错误；这不证明物理原因，也不证明新版本导致。
- 回退末次观测遇到 SSH banner 超时；后续已核验原版 Mac 进程，恢复 Core/apply 并释放全部租约。
  最终配置/通知设置与部署前一致，原已注册线路恢复注册。原有 apply pending 未改变，未自动 apply。
- 14 个只读 API 返回 200，但不计为读卡、通话、Android 或运营商现场验收。Mac 升级未完成；
  已请用户重新插拔指定读卡器，不继续盲目重启、修改 PIN 或操作 eSIM。

离线状态备份仅保留在私有目录，复制后 SHA-256 核验为
`acb8ef753b69cd9c2e0e0a9a47433afa4461f74cafdb136cb23d51fb8b8ce828`；没有倒灌业务数据库。
实际 Linux Provider SHA-256 为 `aff07ad712b2769558abca5c44db7cad34ba919539ed0c42e621dca6bbbb83a6`。
部署范围、回退及剩余项见 [PR #24 执行回执](https://github.com/lovitus/mdd-sim-gateway/pull/24#issuecomment-6077130105)；
原始日志、主机和凭据不进入 Git。

同日跟进诊断（08:30 UTC）：独立 PC/SC 客户端取得 `PRESENT|MUTE`、ATR 长度 0，
共享连接返回 `0x8010000c`；系统智能卡报告亦为无卡。切换窗口的系统 CCID 日志先报
`kIOReturnNotResponding`，再报 `kUSBHostReturnPipeStalled`，最后上电/复位失败。
故障定位到本机 USB/智能卡层；驱动残留、设备/供电/接触及重连触发原因仍未区分，不能归咎物理故障。
新旧版本的 PC/SC、reader worker、SIM session、Agent host、macOS 安装器和 Go 依赖输入无差异。
另发现正常 Mac 的 SIM 元数据仍保留启动时 `pin_required`；状态读取显示保存的 PIN 配置存在、
剩余次数为 3（不是已验证结论），一次原会话读卡刷新即读出 IMSI 并恢复 `ready`，没有提交 PIN。
软件后续应区分 MUTE 与卡缺失/monitor-ready，并在已有认证成功路径同步元数据；本轮只诊断，未改运行代码。
最终进程代际/配置不变、租约为零；故障读卡器仍未恢复，其他 Mac 卡身份可读。
私有最终诊断摘要 SHA-256：`a0039b20c40e298f9ccb8781f0a1ab05a1adba4ca5a80db08461b65f863b7597`。
完整脱敏诊断见 [PR #24 跟进回执](https://github.com/lovitus/mdd-sim-gateway/pull/24#issuecomment-6077392380)。

## 2026-10-03 单机默认断流部署回执

[PR #19](https://github.com/lovitus/mdd-sim-gateway/pull/19) 经实施方、固定 Codex reviewer
和既有 ChatGPT reviewer 对同一 `f236872` 通过后，正常合并为
`cab01c7bbc79b9ea09c710c7fd06e395095eb7f5`；最终 head 的 hosted run `36979155244`
通过。用户随后单独授权原受影响 Windows Agent 的升级、验证和必要回退。

本次只部署已资格化的 Windows amd64 工件，来源
`c57131fe07b4d02523ab2d0144c3638e8387dad4`，运行代码与合并版本一致。
这是 Windows `unsigned-development` 包，不能把 workflow 的 macOS 签名门禁当作 Windows 签名。
tar SHA-256 为 `0aa7fef882257f12ad6474daf203dd5eafe9736aaedf753da4ea319e34bd21f4`；
实际运行 CLI SHA-256 为 `848aa53013f0d89429dbb339fa2364a1944019e62afdae62bc436e347289362e`。
全部 128 项 manifest 校验及候选目录权限检查通过，旧 release 和配置备份保留；没有复制运行中
策略库冒充一致性备份，也没有倒灌数据库。

执行方式为预置 SCM 的下次启动路径，再通过现有 Core 受保护 restart 完成唯一一次停启。
首次 `sc.exe` 调用剥掉双引号，精确路径断言在 POST 前拒绝继续；核对旧进程、代际和当前目标后
只恢复原路径，未发生重启。改用 `Win32_Service.Change` 仅传 `PathName`，精确读回后发送一次
受保护请求。没有裸 `Stop-Service`、安装器自动回退或重复 POST；没有改变账户、启动方式或
SCM 恢复策略。预置路径与 Core 维护门禁并非原子切换，不能据此声称所有时序均被覆盖。

真实读回（UTC）：
- 升级前 02:59:46，Windows MBN 为 `Connected`，有一条连接；Agent 的无持久策略默认意图为关闭。
- 换版后 03:04:43，Core 收到新 Agent 代际及原两张卡，modem 为 `data=disconnected`，
  `data_guard=protected`；默认策略仍未持久化、revision 仍为 0，用户意图和配置哈希不变。
- 03:05:23，Windows MBN 独立读回 `Not connected`；旧进程已退出，实际新进程摘要匹配候选。
  12 条持久 WFP 规则的 XML 与升级前逐项相同。旧二进制、配置备份和 SCM 恢复配置均核对一致。
- Core 二进制、线路目录、通知配置以及其他 Agent 的版本和进程代际未变；受影响线路无通话、
  借用数据或 raw 会话，维护已释放。未拨号、发短信、改 PIN/APN/radio/用户开关或强制开启流量。

**该 Windows EC20、无本地策略的默认断流场景通过。** 不扩大为全平台、长期无泄漏、
跨主机卡片配置迁移、真实语音/SMS 或代理组合共存验收。Linux 无 owner 的 `StopData` 限制及
长期套接字积累根因仍未关闭；中国大陆卡 VoWiFi 继续排除。本次没有触发新 CI 或升级其他设备。

原始证据仅保留在私有目录，公开以下摘要用于追溯：
- `oct03-pr19-after-restart.json`: `92d42bac2d28f86e4ad3a59d0cbd1a76c76bffd78933025d5f0de8cd2bf6a259`。
- `oct03-pr19-host-after.json`: `ccce1634ce3647564681b5678fbed4b61c9eb7fb578931482385462744710659`。
- `oct03-pr19-wfp-mbn-after.json`: `ef86b431bdd7570f9845e129b0c11b4bb93bca4eaf24f6a96be1ab89182721e0`。

## 2026-10-02 Windows 换机接入恢复（历史；长期根因未关闭）

当前结果（07:23 UTC）：目标 Windows Agent 已重新连接 Core，原读卡器和移入的 EC20
均按原卡片身份投影到既有线路。Core 新鲜事实为 `agent_connected`、`card_present`、
`hardware_ready`；modem 的 AT、短信和蜂窝语音能力就绪。没有活动通话、借用数据会话或
raw 会话。本次没有拨号、发短信、修改 PIN/APN/用户开关，也没有安装新 MDD 工件。
这些是接入和能力读回，不是实际通话、短信投递或长期稳定性验收。

恢复时间线与因果边界：
- Tailscale 单独停启释放大量端点，但当时本机连接仍卡住、服务恢复失败。
- 经用户单独授权，正常退出 Proxifier 后，原 Agent 的本机接口和 modem 读回恢复，
  Tailscale 服务恢复 Running。随后按原配置重新启动 Proxifier，三个有界采样的本机 API
  均正常，但 WSS 当时仍重试；配置内容逐字相同。这支持 Proxifier 参与本机套接字故障，
  不证明谁造成长期端点积累。
- 用户随后主动卸载 Proxifier，并明确要求保持卸载；这不是本助手执行的卸载。
  旧 Agent 持续报 `connectex: not a socket`，而新启动客户端可连接同一 Core 端口。
  保留原进程和错误证据、核对受影响线路无活动操作后，仅重启一次现有 Agent 服务。
  首个 30 秒读回为 WSS connected，随后 Core 确认原卡与既有线路恢复；Agent 配置哈希未变。
- Tailscale 服务及界面仍运行，后续原生 CLI 确认 BackendState=Running、Self.Online=true、
  Health 为空；VMware 保持用户要求的停止状态。未修改 Mihomo、EasyTier、
  动态端口范围或持久蜂窝隔离。不能把用户卸载后的恢复写成原代理组合已通过共存验收，
  也没有证据把长期积累归因于 Mihomo 循环。

当时存在一个独立产品缺口：旧 Agent 没有该 equipment/card 的持久策略，却报告
`data=connected`、`data_guard=protected`，同时展示默认关闭意图。隔离报告不等于已断流，
更不等于已完成物理防泄漏验收。SIM/APDU 的 `card_route` 仍报 `sim_apdu_data_active`，
VoWiFi 未就绪；不是全部业务已恢复。
这张中国大陆卡不启用 VoWiFi，遵守既有用户决定，不将其扩成新的 VoWiFi 验收任务。
当时 PR #19 尚未合并或部署，旧工件恢复不为该修复签收；后续合并、授权及实际 bearer
断开结果见上方 October 3 回执，不再把这里的历史下一步当作待执行任务。

以下按发生顺序保留恢复前的证据；其中“未恢复”“下一步”是历史状态，由上文覆盖。

最新观测覆盖下述历史过程：用户随后授权完全停止 VMware；其用户进程、相关服务及可停止
USB 驱动已停，原生 Quectel 接口一度恢复，但 AT 打开报设备无法正常工作。用户再次插拔后
曾出现原生 USB 描述符失败/Code 43；第二次插拔同一端口后恢复 AT/Modem 枚举，AT 打开变为
资源占用。尚无证据确认占用者，不能把枚举成功当作读卡恢复。Core 新鲜快照仍缺少目标 Agent，
其原进程仅有本地控制监听、没有 WSS 连接；Windows 现场故障仍未解决。其他两条读卡器线路
仍有读卡和 IMS 注册证据，不扩展为通话验收。没有重启 Agent、改用户策略或进行付费测试。

后续原生客户端报 `WSAENOBUFS`，系统事件 4231 与大量 `BOUND` 端点相互印证。
Tailscale 服务和界面累计约一万个 TCP 端点，Mihomo 另有约两千四百个；不能把累计占用
直接叫作当前连接风暴。用户授权 Tailscale 停启对照后，全机 `BOUND` 从 12,758 降至约
2,739，证明这两个进程确实占用了大量端点，但 Agent 回环连接仍卡住，不能宣称主故障修复。
恢复 Tailscale 时服务报 1053/启动超时，当前服务未恢复；原配置及登录身份未删除，
界面已回到原用户会话，一次性界面恢复任务已移除。没有重启主机、扩大动态端口范围、
重置 Winsock，或修改 Mihomo、EasyTier、Proxifier 和 MDD 隔离规则。

更正模块检查方法：早先按 `prox` 筛选漏掉实际名为 `PrxerDrv.dll`/`PrxerNsp.dll` 的模块。
精确名称读回确认 Agent 和 Tailscale 均加载了 Proxifier LSP；这推翻“未加载”的负面结论，
但加载本身仍不证明因果。独立原生回环探测在连接调用返回异步任务之前卡住，尚未到达
MDD HTTP。Mihomo 配置有 Tailscale 守护进程直连规则，仍需区分 TUN、系统代理和 LSP
各层；没有证据认定某一个已是根因。Proxifier 短停恢复对照已单独请求，尚未执行。
下一步只做这一有依据的对照和恢复，不重复 USB 复位或修改用户卡片策略。
排查依据：[Microsoft 端口耗尽诊断](https://learn.microsoft.com/en-us/troubleshoot/windows-client/networking/tcp-ip-port-exhaustion-troubleshooting)
和 [Tailscale Windows 服务与日志](https://tailscale.com/docs/reference/tailscaled)。
以下按发生顺序保留早期尝试，不是当前待执行步骤。

本次主任务起因是 EC20 移入另一台已有 Agent 的 Windows 主机后不能自动发现、注册。
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
Proxifier 模块归属已经过本节开头的精确文件名核对；加载拦截模块本身不证明因果。
处置后一次 Core 读回仍未见目标 Agent，SCM Running 不能代替注册。

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

