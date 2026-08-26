# 当前恢复任务：唯一执行游标

更新：2026-08-26；用户最新实测：所有节点UDP检测报错，所有卡网页不可拨。优先修真实入口，不得以历史局部PASS冒称可用。

## 当前批次：真实入口禁拨与UDP检测

- UI已定位：后端正常契约 `state=OK,label=Working`，前端只认几个英文label而把正常线路禁拨。
  修复按state判断，设备/通话/短信共享组件同步；Test media本身不能凭此就称已验证。
- UDP已定位：动态加载host脚本丢失包上下文，`ModuleNotFoundError: mdd_admission_authority`
  在发UDP前就失败；UI又吞成通用代理配置提示。线上独立进程仅纠正import后，同一GB节点及出口
  的两次真实UDP诊断分别293/260ms通过；未改节点、路由、服务。
- 按用户新要求：这两个独立问题并行实施，整批交叉审、一次构建部署、真实入口冒烟。
  不再每几行另开评审，不扩展时长模拟测试。规则已保存全局AGENTS.md。
- 本批交叉审PASS，父回归 `873 passed / 78 subtests`（13.91秒）、16组前端测试通过。
  正常登录后的真实HTTP snapshot确认1/7都是OK/Working、softphone语音能力全部true；
  同一未改响应驱动旧/新React组件，旧版禁拨、新版解除禁拨；不是伪造ready的测试。
  实机无收费WSS→Asterisk Echo：1/7均按序回声通过、结束后0通道、代际不变；未伪造麦克风证据。
  UK首个请求失败但未保留完整curl原因，原失败保留；一次有记录重试通过，不把首轮改写为成功。
  UDP真实认证HTTP请求在旧代码上实证500 Internal Server Error，修复尚待统一镜像部署后复验。
  新构建前端入口index-D96mFEH8.js，dist清单SHA3986e11d；源码/产物必须随本批一起冻结。
- 30c误删保护仍保留；UK约30分钟后NoResponse及自动重建的根因仍未闭合，证据不丢，
  但当前先解决用户“连拨号入口都用不了”的问题。下方为前一批记录，禁止重跑。

## 最新执行点（优先于下面的部署短窗记录）

- UK9960在08:55:41 UTC因 `reg_unanswered` 申请恢复，08:55:43安全停止，08:56:00正常自动
  重建为 `4cd8b5f3663ff300dc3c48143f10558cf6f03c7dcd23fbe39a22c085ab232f02`。没有人工启动。
  新guard没有再次把线路停死，日志真实写stopped；但不能把35分钟连续稳定门判PASS。
- UK旧run在08:54:41发起CHILD rekey，重传后08:55:01完成；仍须对照SIP transport失败时刻，
  不得臆断重键、运营商或出口责任。完整观测日志已保留；检查TCP flags抓取是否漏IPv6。
- 此刻继续分析这次实证事件，**不要重跑已经完成的build/core/wrapper/finalize**。
  Control c600、FR b75未变；当前真实UK状态需fresh核验，下面9960是部署后初代历史。

本文件只记录当前事实和下一步。`TODO_ACTIVE_RECOVERY.md` 保留全部历史流水；其中旧的
“当前游标 / next_action / 待部署”不能当作新指令重新执行。App goal 刚经只读查询为 **paused**；
本轮是用户手动授权继续，不新建 goal、不重置旧目标、不重复已关闭工作。

## 当前结果

- **当前：英国 line1、法国 line7 均 Registered、五类准入 ALLOW、零付费任务/活动通道，RestartCount0。**
  英国在事务释放后正常 hotplug 自动启动，并经真实身份探测得到 current＋同代；未手动 start1。
  法国本轮在 scope7 内受控替换，IMS已注册，但其读卡身份仍 unknown；不能伪报全部身份就绪。
- 上轮英国的实际故障（现已修复误删路径，不改写历史）：
  07:21:48 UTC SIP TCP transport failed；07:23:27 REGISTER 无响应并安排30秒重试；
  07:23:36–37 `health-freeze:reg_unanswered` 删除本代 Engine。随后日志错误声称保留引擎。
  删除前没有检查重建资格；删除后因远端身份 unknown 拒绝自动启动。不是已证实的拔卡或 Docker 崩溃。
  原始 TCP 断开原因尚未知，不能归因运营商、出口、重键；35分钟只读 TCP/运行观察正在进行。
- 事故前短窗曾验证两线 `AUTH_OK / CONNECTED / Registered`、五类准入 ALLOW、零付费活动、
  RestartCount 0；这是当时的采样结果，不是长时稳定性通过。
- 浏览器语音已经使用同源 WS/WSS；旧 IP 确认入口关闭。根路径与 `/mdd/` 的 14 次静态文件
  HTTPS pin＋SHA 校验均通过；裸 `/mdd` 正确 307 到 `/mdd/`，保留 query。
- 1445轮 France7 曾在 promotion 后8.05秒正常自动启动；那是历史现场，不能套到本轮 scope7。
  unknown 与 IMS 注册/通话准入是不同证据，不得借配置填成 current，也不得擅自理解成卡已拔出。
- Windows f00bdd 的最终只读观察：call.status 回执约0.8秒新，call/audio ready均true、contract v2
  无错误。当前上报且服务端接受的包digest是 `acf2f7dd332641a6d58181fddc1dccde70720a49256a592129ddedccad7f62c6`；
  这不是历史C记录中的旧包号，来源未在本轮重新审计。**未修改Windows，不得凭历史包号擅自回退**。
- 两台 Mac 均仍使用正式 `50da938a…` 包、CLI host、`pcsc_only`、`modem_enabled=false`，各两个
  reader/card present。fanli 节点通过现有 CLI reconnect **一次**恢复读卡通道，host PID 未变；
  新 run `7e026138a61148ea864ee68a42ea4d5e` 对应 slot11 空 eUICC（有 EID、无 ICCID）与 slot14
  实体卡（matched4），均 current＋同代。未开启 Modem、麦克风或 raw USB。
- 香港 line4 因原配置 enabled 在读卡重连后自动启动；收到 IKE_AUTH 响应但缺少 EAP payload，
  后被 Control 既有 `health-freeze:tunnel_setup` 正常停止并删除，运行约 132 秒、exit0。
  不是本轮人工 stop，也不是 Docker 崩溃/循环重启。**不能据此归咎出口或运营商**。

## 唯一源码和产物

工作树：`/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-forward-runtime-20260824`

分支：`codex/forward-runtime-20260824`。用户原始工作树未被切换、覆盖或清理；未 push。
后续任务板提交只是文档，不能误当运行产物版本。

| 组件 | 运行源码 | 生产 OCI manifest ID |
| --- | --- | --- |
| Control | `30c6d6ce0342bfc4b1b337211132dfdc5f2e1bf1` | `sha256:c65466aa59c3b41a50dc81dcbbec3bdb05366cdbd53f1931f4229202673ed5b2` |
| Engine | `cf53335c0c245cdaaaf75f6b6aef369ec39b0a9b`（Engine目录与45完全相同） | `sha256:2868e50ebe8403393e6fb55932692135f6d1c9bd67d5fb6ba9740df6cfae9618` |

Docker classic 的 **config ID** 分别为 Control `ed594367…`、Engine `9180b98b…`；与 containerd
的 manifest ID 不同是存储后端语义，不是代码不一致。按归档中的双向映射及源码 SHA 核验，禁止据此重做 E3。

生产仍在 `root@10.44.0.23`。最终记录：
`/opt/mdd-gateway/data/deploy-records/codex-20260826-uk-recovery-guard`

- Control：`c600bc510ce1df524f186c4f78de8106cf01f3cdc8a9f51e9ded569068a8c528`
- UK Engine：`99600cc24e8715d531a58b583a49262c51637b2c26244148937f61531e47c78b`
- FR Engine：`b75c8fcf2674d4261ad0cfc70b0926445d1b438ba6611394720903bee458f0e8`
- Engine事务 `engine-replace-1787732536-b21fc5de3ba9` 已 committed；默认镜像正确，Control已恢复
  unless-stopped。旧 Control a34/source/SQLite 保留；新13文件配置/一致SQLite快照已离机，SHA96bd2ecd。
- **完成依据**是 `finalize-control.json.phase=complete` 和 `engine-replacement.last.json` 中的
  committed。`cutover.json` 的 `control_verified_engine_replacement_prepared` 是历史交接点，
  不是要求再次执行的未完成步骤。

## 已关闭，不得重做

1. Runner D 持久下载/镜像/构建代理及增量传输整改：完成；只用私有 registry 的 fresh job。
   默认工具 `/Users/fanli/.local/bin/runner-transfer`；实际地址和原始输出只在 private。
2. E3 编译、EOF/masquerade 修复及事务部署：完成。
3. E4 呼出/呼入、多端唯一 owner、续租、终态收据、真实 SQLite 入站方向、旧 SIP/IP 入口关闭：完成部署。
4. 本次三个收尾补丁均已部署：ASGI前缀及审计；精确 USIM-fenced idle maintenance；维护结束后
   单次真实身份探测。普通 paid admission 未放宽，真实 APDU/CAS 失败不无限重试。
5. 旧1345记录的 metadata continuation `5088c4ea…` 和 begin-only helper `06b25a2b…` 已执行并关闭。
   **禁止重跑这些绑定旧容器/旧事务的脚本**。最终1445流程没有使用它们。
6. Windows C 与 macOS PCSC-only 正式部署已有完整记录；不因此更新用户要求暂不动的旧 Windows。

## 本轮已部署：UK 自动恢复删除前资格门

- 预评审 PASS：沿用现有每线锁、lifecycle SH 许可和有界 idle-stop，不新建恢复协议。
- 真资格＋FakeDocker 回归已复现旧代码：身份 unknown 仍执行 `restart_policy=no` 后停删。
  修改只涉及 Control 的 main/engine 模块及测试；Engine 镜像、运营商/出口和503策略不改。
- 独立复审另实证两个边界：取消后真实 stopped 结果必须提交再释放锁；同ID换新进程后，
  必须还原本操作改掉的 restart 策略，同时禁止对新进程执行停机/删除。
- 新部署包复用原 core `1600b482…`，只改 source base45 和当前唯一运行 scope7；
  原1445记录完整保留。新旧产物不得混用；英国缺失不是 scope 内待替换容器。
- 最终父回归25文件 `785 passed / 62 subtests`（13.09秒），日志SHA `ffc4ebb0…`；
  两名评审交叉审查产品和测试均 PASS。第一次全量100秒卡住已定位并修正三处旧Mock签名，
  原11条竞态断言/0.5秒门保留；失败日志未覆盖，不作为通过证据。
- 正式镜像源文件58＋VERSION＋前端7文件逐字一致；root/prefix共四场蜂窝wire≥16秒、
  三模式VoWiFi≥8秒通过；全部模拟硬件、网络隔离、零真实付费动作。
- 批准计划 `a8b6db74…`，原core1600→原wrapper scope7→finalize全部完成。
  首次finalize遇额外line4正在启动、CLI unknown，在写journal前拒绝；等待该线自然停止后原样完成。
  未为通过检查停line4/放宽门。当前短窗both operational=true，all identity ready=false。

## 验证和私有恢复材料

- 最终24文件：`749 passed, 62 subtests`；父复跑日志 SHA
  `e891b3f8ada4bd8baf0013a2c74f59af6cb55f5e727a18b7fa6f5be858a6851d`。
- 正式镜像：Control66文件逐字匹配；root与/mdd各两场真实HTTP/WS/RPC/SQLite（模拟硬件），
  每场16秒、一次模拟付费动作、9次续租、关闭约1.5秒后owner/lease归零；VoWiFi三种关闭模式通过。
- 事故前独立短窗复审：两线Registered/ALLOW/0calls、前后代际CAS、restart0、14资源pin＋SHA通过。
  短窗Control CPU 2.12%–5.07%；不宣称已完成长时稳定性或真实电话验收。
- 构建产物：外置盘 `mdd-e4-postflight-fixes-20260826/`；Control归档SHA
  `040ce181e5e672d44eadf08e46c33af7aedd1add957a653a7ac1914ddebafc76`。
- 私有证据/运行前后备份：`/Users/fanli/.codex/private/mdd-e4-postflight-fixes/`。
  另有完整45源码归档、配置/认证/TLS私钥/一致SQLite离机快照（SHA `6d29327b…`）。**不得上传Git/公开附件**。
  这是应用恢复资料，不是OS/代理unit/所有Agent安装包的整机备份；不要把旧运行事务直接回灌。

## 下一步及延期边界

1. **现在继续只读观察和独立线上复审**：35分钟观察器已启动，跨上轮约27分钟故障窗。
   不重跑已经完成的本轮/1445 core、wrapper或finalize；新原始证据在 private/mdd-uk-recovery-guard。
2. **之后用户页面验收**：真实呼入/呼出、音质、主动挂断和页面/网络断开的收尾。
   本轮未发起任何真实收费电话/SMS/手工APDU测试；未经新的明确授权不得自动拨号。
   Browser对该URL有产品策略阻断，不换浏览器/代理绕过；已用既定pin完成非浏览器验收，不能冒充麦克风验收。
3. D2只续未闭合项：PCSC各通道的独立活性/有界恢复（Agent health心跳不等于读卡WS活着）。
   身份问题拆成两批：① 已park且未真实probe，确认原owner离开、无Engine占该reader后，一次rearm
   接回现有read＋generation CAS；② 仍running的Engine需要另验idle下严格PCSC事务刷新。
   当前Agent presence、Registered、AUTH_OK均不能充当新VPCD代际的身份凭据；sim._Tx存在无锁退化，
   不能直接删running限制。底层PCSC没有统一硬超时；保持取消锁不等于能强停系统调用。
4. line4缺EAP的握手异常已有独立证据，按实际需求另查；不重启试错、不改用户的出口/运营商配置来凑绿。
5. 两个旧direct-helper测试的startup状态fixture隔离已在本批显式补齐，定向通过；不修改生产恢复门。
6. G：主流程实机验收后封存旧研究树；先做自包含可校验备份，永不清理用户原工作树。
7. macOS完整4G/5G Modem与私有数据面、Linux统一Agent仍是后续；保持Mac默认PCSC-only，不能宣称全能版本已完成。
