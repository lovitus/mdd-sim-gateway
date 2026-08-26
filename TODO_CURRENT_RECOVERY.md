# 当前恢复任务：唯一执行游标

更新：2026-08-26，部署后短窗通过，但英国线路随后自动删除；当前继续修复，未交付完成。

本文件只记录当前事实和下一步。`TODO_ACTIVE_RECOVERY.md` 保留全部历史流水；其中旧的
“当前游标 / next_action / 待部署”不能当作新指令重新执行。App goal 刚经只读查询为 **paused**；
本轮是用户手动授权继续，不新建 goal、不重置旧目标、不重复已关闭工作。

## 当前结果

- **当前阻断：英国 line1 Engine 已被 Control 自动停止并删除，不能宣称两线恢复完成。**
  07:21:48 UTC SIP TCP transport failed；07:23:27 REGISTER 无响应并安排30秒重试；
  07:23:36–37 `health-freeze:reg_unanswered` 删除本代 Engine。随后日志错误声称保留引擎。
  删除前没有检查重建资格；删除后因远端身份 unknown 拒绝自动启动。不是已证实的拔卡或 Docker 崩溃。
  原始 TCP 断开原因尚未知，不能归因运营商、出口、重键。Control 和 France7 在事故后仍运行。
- 事故前短窗曾验证两线 `AUTH_OK / CONNECTED / Registered`、五类准入 ALLOW、零付费活动、
  RestartCount 0；这是当时的采样结果，不是长时稳定性通过。
- 浏览器语音已经使用同源 WS/WSS；旧 IP 确认入口关闭。根路径与 `/mdd/` 的 14 次静态文件
  HTTPS pin＋SHA 校验均通过；裸 `/mdd` 正确 307 到 `/mdd/`，保留 query。
- France7 是正常补探测身份后自动启动，不在部署替换 scope 内；Created 在 Engine 默认版本
  promotion committed 后约 8.05 秒，未手动 start7。法国卡 current 身份和 session 代际一致。
- UK 事故前运行的读卡器未被强制 APDU 重读；其 current 身份快照仍为 unknown。这与此前 IMS
  注册/通话准入是不同证据，不得借配置填成 current，也不得把 unknown 擅自理解成卡已拔出。
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
| Control | `45e835e5a2a786c9e81254eb7cb8bb8dcb64aaca` | `sha256:068ff8384b198e33662b8f39e60112674fac062517375c11db7d8cc349bec137` |
| Engine | `cf53335c0c245cdaaaf75f6b6aef369ec39b0a9b`（Engine目录与45完全相同） | `sha256:2868e50ebe8403393e6fb55932692135f6d1c9bd67d5fb6ba9740df6cfae9618` |

Docker classic 的 **config ID** 分别为 Control `fc9b1a59…`、Engine `9180b98b…`；与 containerd
的 manifest ID 不同是存储后端语义，不是代码不一致。按归档中的双向映射及源码 SHA 核验，禁止据此重做 E3。

生产仍在 `root@10.44.0.23`。最终记录：
`/opt/mdd-gateway/data/deploy-records/codex-20260826T1445-browser-media-e4-postflight-fixes`

- Control：`a34f4293b0294fecfe12cb630eabb6bf219250e92654bea0312824bb6d2813d2`
- UK 原 Engine（已被上述自动恢复删除）：`fede4b712c4d6ffcfa525e36cb00ab5c4dc0a82700728669ca410a6118a7de96`
- FR Engine：`fac1f6f71de7e214d98770581a1f73ed3b81256378bceaadf38f037724e7a26a`
- Engine事务 `engine-replace-1787727232-382436381b85` 已 committed；默认镜像正确，Control已恢复
  unless-stopped。旧 Control/source/SQLite 保留并已离机同步。
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

1. **现在先修恢复路径**：预评审 → 删除前同代/当前配置/重建资格检查，unknown 保留引擎；
   日志依据真实停止/保留结果；保留资格齐全 NoResponse 的有界恢复，不改503逻辑 → 回归与独立复审
   → 新现场快照和有记录部署 → 恢复英国并观察。不要重跑已完成1445计划或只重启线路凑绿。
2. **之后用户页面验收**：真实呼入/呼出、音质、主动挂断和页面/网络断开的收尾。
   本轮未发起任何真实收费电话/SMS/手工APDU测试；未经新的明确授权不得自动拨号。
   Browser对该URL有产品策略阻断，不换浏览器/代理绕过；已用既定pin完成非浏览器验收，不能冒充麦克风验收。
3. D2只续未闭合项：PCSC各通道的独立活性/有界恢复（Agent health心跳不等于读卡WS活着）；
   运行中UK的权威身份更新；底层PCSC没有统一硬超时。本次取消保持锁直到真实worker结束，
   不代表能强制终止一个卡死的系统调用。可优先研究现成库，不扩无证据的兜底。
4. line4缺EAP的握手异常已有独立证据，按实际需求另查；不重启试错、不改用户的出口/运营商配置来凑绿。
5. 两个旧direct-helper测试的startup状态fixture隔离问题已有基线RED证据，后续修测试，不放宽生产恢复门。
6. G：主流程实机验收后封存旧研究树；先做自包含可校验备份，永不清理用户原工作树。
7. macOS完整4G/5G Modem与私有数据面、Linux统一Agent仍是后续；保持Mac默认PCSC-only，不能宣称全能版本已完成。
