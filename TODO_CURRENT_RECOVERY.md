# 当前恢复任务：唯一执行游标

更新：2026-08-26，入口修复716已部署，真实API/节点检测/无收费回声通过；真实收费通话和长期稳定性仍未宣称通过。

## 当前用户要求与执行方式

用户最新反馈是“所有节点UDP测试失败、所有卡网页禁拨”，并明确要求独立问题并行推进、整批评审，
不能用大量模拟/局部PASS代替用户入口可用。本批已按此处理；规则与凭据使用位置已持久写入全局AGENTS。
用户已编辑goal为继续上述工作、优先最小可用实现；不要新建/重置goal，不按旧goal重做已关闭事项。
最新纠正：Registered不是通话健康。用户已长期授权指定香港EC20、英国giffgaff号码实拨，
授权与限次/挂断要求记录在Git外的私有凭据目录；不再索要授权。先实际拨号和确认物理挂断，
不能以注册、能力标志或模拟回归替代。浏览器当前明确URL策略拒绝，页面点击未完成，禁止绕过。

## 本批已修并部署

1. 后端正常契约是 `state=OK,label=Working`；旧前端依赖英文label白名单而误禁拨。
   新版以机器state为准；设备/通话/短信/线路选择器共享判断，Messages缓存也观察state。
   明确错误状态不会被“Working/Registered”展示文字误放行。
2. UDP测试动态加载host脚本时丢失包上下文，在发UDP前抛
   `ModuleNotFoundError: mdd_admission_authority`。已改为正常包导入，复用现成解析器/探测逻辑。
   服务器内部组件错误有准确中英文提示，不再误导用户检查密码或UDP支持；未改节点、路由或测试目标。
3. 已有30c删除前重建资格保护、取消后的停机回执和同ID策略恢复保持不变；未修改Agent或Engine代码。

## 唯一源码/产物/现场

工作树：`/Volumes/micron512g/tmp-project/codex-audit-tmp/mdd-forward-runtime-20260824`
分支：`codex/forward-runtime-20260824`。用户原始工作树未切换/覆盖；没有push。
运行源码：`716092c28482eeeb806044fd81ae71915f8a19ea`；后续任务板提交只是文档。

| 组件 | 当前产物 |
| --- | --- |
| Control | OCI `sha256:e620ab626aa2eca3e44ef16d711f28245f9b3c14d838f74d3881c16c04519fbc`；classic config `d5462c43…` |
| Engine | 仍是cf53335源码、OCI `sha256:2868e50ebe8403393e6fb55932692135f6d1c9bd67d5fb6ba9740df6cfae9618`；classic config `9180b98b…` |
| WebUI | `index-D96mFEH8.js`；7文件清单SHA `3986e11d3429a0d36b76f021e9aabe40d6f84547fb2a8d5c468faf82561a2e03` |

生产仍为原服务器。部署记录：
`/opt/mdd-gateway/data/deploy-records/codex-20260826-entry-ui-udp`

- Control：`81d97f8bc85abaf9a3be9d1d3227925d06c78b044da371c676ce9fed7001533f`
- UK1：`b253d495fbfd8a62df874e770dd8f490069eee22759122252705edf70bf73378`
- FR7：`bdc05f7663b6126668f16b75120ad18a7d13079b2a7c06ec3c73effd982a5f84`
- 计划SHA `5608415a…`；原core1600 → 原wrapper scope1+7 → finalize均完成；
  事务 `engine-replace-1787737003-6798ffec3ce0` committed，默认镜像和Control unless-stopped已恢复。
- 第一次finalize在写journal前因额外line4启动中、CLI未知而拒绝；等其自然停止后原样完成。
  未为通过检查停line4、改用户配置或放宽门。该失败保留，不能重跑已完成部署。
- 旧Control c600/source/SQLite及完整前后create spec已留存并离机同步；
  新13文件配置/一致SQLite快照SHA `dd858251…`。大镜像、源码和备份均有校验值。

## 实际验收证据（分清层级）

- 父回归28文件：`873 passed / 78 subtests`，13.91秒；16组前端测试、整批交叉审通过。
- 正常登录后的真实HTTP snapshot：UK/FR都是OK/Working，softphone enabled/running及
  available/outbound/inbound全true。旧版相同输入禁拨，新版原React组件解除禁拨。
  没有替换响应字段或伪造ready；SSR不是实际浏览器音频验收。
- 部署后4个已保存代理节点真实POST测试全部HTTP200/oktrue，260/1644/420/834ms。
  旧代码真实HTTP500原文已保留。两个cellular-SIM配置不是这4个代理节点，未为测试开启移动数据。
- 实际服务器、正常管理员会话的WSS→Asterisk Echo，UK/FR均收到按序精确PCM回声，
  正常关闭后0通道/0通话、容器代际不变。只使用canary接口，没有拨号、接听、SMS或APDU。
  麦克风/播放计数未伪造；这不等于真实话费/音质验收。
- 新前端在根路径与/mdd的14次HTTPS pin＋SHA校验通过，证明新资源实际已送达。
- 最早一个UK canary请求失败且curl原因未完整保存；原FAIL保留，一次有记录重试及部署后复验通过。
- 先前600秒WS、三模式WSS及TLS负控属于隔离模拟证据，不得拿来替代以上真实入口验证。

本批私有索引/结果：
`/Users/fanli/.codex/private/mdd-entry-fixes-20260826/RECOVERY_INDEX.md`。
管理员凭据已在工作区外的私有目录保存，文件0600、目录0700、整目录忽略Git；当前跟踪文件无该密码。
服务重启后从已保存文件正常登录，不能反复问用户，也不能把会话/凭据放进源码、镜像或公开包。

## 当前可靠性批次：代码复审通过，尚未部署

只改五个运行文件：Control的main/sim/status/vpcd_slots，以及Engine的swu_ike。
当前运行仍为上面的716，不要将新候选当成现场或重复部署716。

- NoResponse保留一次Asterisk原生重试与实际timer_b=128秒结果窗口；首次失败锚定截止时间，
  不随轮询延长。到期/下一次明确失败后仍走已有精确代际、身份、零通话和限频保护。
- 重键按SPI严格认证；新decoder安装确认后才切encoder，旧DELETE同MID有限重传，
  收到真实认证回执后保留旧入站5秒。未改出口、重键周期、初始EAP或responder开关。
- D2不再凭Engine配置伪造当前卡身份；空闲真实读取必须持有既有通话/生命周期/读卡锁，
  strict PC/SC事务明确LEAVE，并用Agent/VPCD/Engine代际及ICCID核对后发布。
- 整批复审发现并修复三处缺陷：拒绝本端角色IKE报文反射；新的可信Agent采集代际可给一次新身份探测；
  未知注册样本不清除首次失败期限。早期1121/1122回归仅保留历史，不能替代最终产物证据。
- 最终40文件1136项/125子测试通过，14.82秒；独立复审401项/52子测试通过，P0/P1为0。
  私有部署辅助包14项通过、core1600未改。以上仍不是实际拨号/浏览器音频验收。
- 原有UK代际在10:08:46Z成功重键；10:27Z仍Registered/0通话。已结束的被动抓包证明
  所采窗口内双向保活，不能反推旧两次故障，也不能把所有NoResponse都归因于重键。

当前私有游标：`/Users/fanli/.codex/private/mdd-reliability-20260826/RECOVERY_INDEX.md`。
唯一下一步：先完成用户指定号码的短时实拨与真实挂断核验；构建可并行，部署不能打断测试通话。
后续复用现有core1600/原EngineReplacement事务部署。
不要新建恢复框架，也不要重做Runner代理/入口修复。原生PC/SC调用缺少硬超时仍是明确限制，
不能靠杀持卡进程实现“安全超时”；owner内身份读取接口等更重方案留待后续，不宣称已完成。

## 其余未闭合项与验收边界

1. 真实浏览器麦克风/扬声器、运营商实际呼出/呼入和收费通话挂断仍需实测，不能因入口已修就全量报绿。
   指定号码已有长期授权；先每线一次、短时测试并确认实际挂断。浏览器URL安全策略拒绝后的
   独立API/后端验证须单独标注，不能声称完成页面点击或真实麦克风/扬声器验收。
2. UK长期NoResponse根因未闭合：上一轮9960在08:55:14 transport failed、31秒NoResponse，
   43秒安全停止，08:56自动启动4cd8（约15.5秒）。CHILD重键08:55:01完成，相关性不是因果证明。
   35分钟连续稳定门FAIL，不能重写PASS。旧TCP flags过滤器已证实漏IPv6；若继续抓取需修采集，
   不能因缺包推断没有断开。该研究在用户最新入口故障修复期间暂后置，证据都保留。
3. D2身份/活性：Agent健康心跳不等于每条读卡WS活着。park后原owner离开的一次rearm，
   与仍running时严格PCSC事务刷新分批处理；不凭配置/Registered/AUTH_OK伪造current。
   本批最后HTTP快照FR7虽然显示current=true/绑定7，但身份代际与连接代际不一致，是D2待修错误；
   UK未匹配到当前身份。两者都不能当作已确认的当前卡身份。
4. engine/notify.py的独立HTTPS事件hook仍verify=False，后续收敛为证书/pin校验；
   本次WSS媒体已严格验证不等于所有HTTPS调用已整改。
5. macOS仍默认PCSC-only/modem_disabled；完整4G/5G私有数据面、Linux统一Agent、旧研究树封存属后续。
   不改用户要求暂不动的旧Windows，不凭历史包digest回滚已接受的新Windows客户端。
6. 延期UI小修：首次softphone GET失败后，同iid快照/WS重连不会再次获取能力；会停留“能力检查中”。
   这是已确认的独立加载恢复缺口，但不能解释本次精确“后端未就绪”截图，不混入可靠性批次。
   旧活动SPA可能保留旧JS也尚未在用户标签实证确认，不能仅凭服务器资源正确就归因缓存。

## 已关闭，禁止重复执行

Runner D持久代理/传输工具、E3构建与EOF修复、E4媒体协议与租约、source45收尾、
30c安全恢复guard及本批716入口修复均已有记录。只接未闭合项，不从历史长任务板重启旧流程。
`TODO_ACTIVE_RECOVERY.md`、旧私有索引和Git提交保留历史；它们的旧“当前/下一步”不是现行指令。
