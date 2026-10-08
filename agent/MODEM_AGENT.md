# MDD 统一 Go Agent

当前正式客户端只有 release 中的 `mdd-agent`，macOS 同时提供使用相同运行时的
`MDD Agent.app`。它按平台能力管理 PC/SC/eUICC reader、蜂窝 Modem、短信、通话、音频和受隔离
数据；不再存在独立 Card Agent、Python Agent、Android VPCD App 或明文 35963 入口。

## 共同安全边界

- 每台主机使用稳定 `agent_id` 和服务端为该 ID 单独签发的随机 token。不要在不同 Agent 间复制 token。
- token 只通过 stdin 或 GUI 写入 owner-only 配置，不放入 argv、环境日志、Git、镜像或支持包。
- Agent 在发送 token 前校验服务端证书 pin；不得使用 TOFU、跳过 TLS 校验或先发凭据后验指纹。
- Core 的 scoped 模式拒绝未知和 revoked Agent ID。轮换或撤销会关闭该 ID 的 control、媒体、数据和
  USB/IP 会话；旧连接不能继续使用。
- PC/SC reader 名、COM/tty 路径、网卡名和枚举顺序只作 attachment 信息，不能替代 card、equipment、
  process generation 或 SIM session generation。
- SIM session generation同时绑定平台插拔事件epoch：Windows MBN subscriber/ready通知、Linux
  ModemManager SIM对象/属性事件、macOS companion QSIMSTAT URC。事件源不可用时ready SIM保持unknown；
  不用TTL定时换代，也不因信号强度或普通注册状态变化使长期在线卡失效。
- `ready`、服务进程运行和 capability 协商不是硬件业务验收。SIM/PIN/APDU、短信、通话和数据仍需各自
  的精确身份、租约、状态读回与真实设备证据。

## 获取 release

只使用 GitHub workflow 生成的对应平台 Agent artifact，并核对随包 manifest/SHA-256。Windows 与 macOS
安装入口分别是：

- `go-runtime/release/install-windows-agent.ps1`
- `go-runtime/release/install-macos-agent.sh`

这两个入口执行 preflight、版本化安装、原子 current 切换、启动检查和失败回滚。不要恢复历史
PyInstaller spec、旧 package builder、计划任务、`run-*.bat/command` 或手工覆盖当前二进制。

## Windows

正式运行时是 `MDDAgent` SCM 服务。服务独占硬件；CLI/GUI 只是本机控制客户端，不能再启动第二个
设备运行时。配置通常位于 `%ProgramData%\MDD\GoAgent\config.json`，以实际安装 receipt 为准。

管理员 PowerShell 中先运行安装器 preflight，再安装候选。Windows 没有伪造的 `current` 链接；日常状态
从 SCM 的权威 ImagePath 解析当前版本：

```powershell
$image = (Get-CimInstance Win32_Service -Filter "Name='MddAgent'").PathName
$agent = [regex]::Match($image, '^"?(.*?\.exe)"?(?:\s|$)').Groups[1].Value
& $agent status --config 'C:\ProgramData\MDD\GoAgent\config.json'
& $agent topology --config 'C:\ProgramData\MDD\GoAgent\config.json'
```

更新 token 时，从 WebUI 为配置中的精确 Agent ID 签发新值，并通过 stdin 写入：

```powershell
$token = Read-Host -AsSecureString 'Scoped Agent token'
$plain = [System.Net.NetworkCredential]::new('', $token).Password
$plain | & $agent config set token --stdin --config 'C:\ProgramData\MDD\GoAgent\config.json'
Remove-Variable plain, token
Restart-Service MDDAgent
```

必须在 Core 读回新 process generation 和原 topology 后才删除迁移备份或切 scoped。Windows Modem soft
restart 是单独的破坏性恢复操作；只有管理员、精确 PnP/equipment/card/session、零活动通话/数据/raw
ownership 全部满足时才可执行，不能用 SCM restart 代替。

Windows MBN 与辅助 AT UICC 通道可能共享同一张 SIM 的所有权。配置启用 modem SIM APDU 时，Agent 启动
只上报 `sim_apdu_on_demand`，不立即发送 UICC 测试命令。用户可先保存 VoWiFi intent；若持续 4G 数据仍
连接，页面显示 data ownership blocker且不探测。关闭持续连接后，Core在唯一 modem、精确session、零
call/data/raw租约和非飞行模式门禁下请求一次准备；Agent重新核对实际bearer已断开后才运行CCHO/CGLA/
CCHC测试形式。新topology确认`sim_apdu=true`后，原durable intent才自动启动Provider。不要通过串口工具
手工发送这些命令，也不要把 SIM present 误报成 VoWiFi ready。

## macOS

CLI 与 `MDD Agent.app` 使用同一个 AgentHost 和同一份配置，不能同时占有硬件。正式安装器管理签名校验、
LaunchAgent、版本目录和 rollback。配置与 state 路径由安装 receipt/LaunchAgent 的 `-config` 参数确定；
不要假设旧 `MDD Go Shadow` 或历史 App 目录一定是当前路径。

只读核对当前 LaunchAgent：

```bash
plutil -p "$HOME/Library/LaunchAgents/com.mdd.agent.plist"
launchctl print "gui/$(id -u)/com.mdd.agent"
```

使用当前签名二进制写入 scoped token：

```bash
printf '%s\n' "$MDD_AGENT_TOKEN" |
  /absolute/path/mdd-agent config set token --stdin --config /absolute/path/config.json
```

配置文件必须为 `0600`。纯 PC/SC 模式保持 `modem_enabled=false`；只有已验证的目标硬件才显式启用 Modem。
缺少桌面会话或 TCC 权限不能伪装为音频 ready，但也不应阻断独立 reader 状态。

## Linux

### 现有 systemd 安装方式

release 包含同一 `mdd-agent` 与 `mdd-agent.service`，但服务端安装不会自动启用 endpoint Agent。先创建
owner-only 配置、写入精确 Agent ID/server/token/TLS pin，再显式启动：

```bash
sudo install -d -m 0700 /var/lib/mdd-agent
sudo mdd-agent config init -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set agent_id linux-agent-1 -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set server gateway.example.com:8443 -config /var/lib/mdd-agent/config.json
printf '%s\n' "$MDD_AGENT_TOKEN" |
  sudo mdd-agent config set token --stdin -config /var/lib/mdd-agent/config.json
sudo mdd-agent config set tls_sha256 "$MDD_TLS_CERT_SHA256" -config /var/lib/mdd-agent/config.json
sudo systemctl enable --now mdd-agent.service
```

Linux 的 PC/SC/eUICC 与已实现 ModemManager/static-IP data isolation 按 capability 暴露。DHCP、PPP、IPv6
bearer 或未完成宿主隔离的设备必须 typed fail closed；不得因为接口获得地址就把它变成宿主默认出口。

### 嵌入式设备手动部署

2026-10-02 用户决定：保留现有运行时、安装器和隔离逻辑，增加独立的手动部署流程。用户自行下载、
放置程序、配置连接和管理进程；不要求为了运行 reader Agent 安装 systemd，也不自动修改 crontab。
这不是服务器/Core 手动部署指南，不安装 Provider、代理出口或第二套 Agent 实现。

**先确认范围和依赖：**

- 使用对应架构和 libc 的 GitHub workflow 工件。当前 Linux 发布 job 只有 amd64；ARM64、ARMv7、
  OpenWrt/musl 不能使用 amd64/glibc 包，也没有因本说明而获得已验证支持。ARM64 需先提供经过
  GitHub-hosted 构建验证的匹配工件；不在目标板上现场构建或下载不明来源替代品。
- Reader 需要系统提供匹配的 PC/SC 库、`pcscd` 和读卡器驱动（例如 libccid），并允许运行用户访问
  pcscd。由用户按发行版方式启动和保活 pcscd；已有实例不要重复启动。Agent 和 pcscd 都不必由
  systemd 管理。缺少依赖/权限时保留真实错误，不把进程存在当作读卡正常。
- 无 systemd 的本流程当前只覆盖默认关闭 modem/raw USB 的 reader 配置。现有 Linux managed modem
  仍依赖已安装的持久 guard、systemd/cgroup 和其他隔离条件；手动放置二进制不会消除这些条件。
  不删除 guard 检查，不为了启动而改写已有用户开关，也不以 cron 代替开机前流量保护。
- `modem_enabled=false` 表示 Agent 不接管 modem，不代表系统不会使用它。无合格隔离的设备不要接入
  漫游 modem。本流程不修改 Android modem 支持范围。

**1. 下载、核对、放置。**

从项目 [GitHub Releases](https://github.com/lovitus/mdd-sim-gateway/releases) 选择明确版本，下载对应
Linux tar 和同一发布的 `SHA256SUMS`，先用 `sha256sum` 比对所选 tar 的那一条记录，再解包。若使用
维护者提供的 workflow 候选，必须同时取得精确源码 revision、架构、工件 SHA-256 和验证记录。
不要将历史 Latest Release、文件名中的架构或 manifest 支持的架构当作当前已构建/已验收证明。

外层的 `install-release.sh` 不在本流程执行。完整保留内层 `mdd-<revision>` 目录及 manifest、许可证、
源码归档，按 `manifest.json` 的 `agent` role 找到 `mdd-agent`，核对其 SHA-256。使用 `file`/`readelf`
检查 ELF 架构、动态加载器和依赖；不要把缺库、glibc 版本不兼容当成配置故障。

下面以 `/opt/mdd-agent` 为例：由用户选择固定运行账户，并提前创建归该账户所有的本地持久目录，
权限 `0700`；不要用易失 `/tmp`、网络文件系统或与另一实例共享的配置目录。之后所有命令和
`crontab -e` 均由同一账户执行，路径可以自行替换：

```text
/opt/mdd-agent/
  releases/mdd-<revision>/  # 已核对的完整内层 release 目录
  current -> releases/mdd-<revision>
  config.json              # 0600，同一运行用户所有
  state/                   # Agent 创建的持久状态，更新时保留
  logs/                    # 私有日志，由用户限制大小/保留期限
  run.lock                 # 本地进程锁，运行中不得删除
```

放置完成后将 `current` 指向所选版本，保留程序执行权限；不要覆盖正在运行的程序。
目录不需要处于默认安装器的 `/usr/lib/mdd` 下。

**2. 初始化并手动配置。**

以下仅适用于全新目录；已有配置不得再次初始化、换 ID 或丢弃 `state/`。示例 ID/地址由用户替换。
先创建稳定 ID，在 Core 系统设置中为该 ID 签发独立 scoped token。配置命令直接写入 `0600` 文件，
不需要手填 JSON，也不把服务器 token 写进命令参数：

```sh
umask 077
/opt/mdd-agent/current/mdd-agent config init --config /opt/mdd-agent/config.json
/opt/mdd-agent/current/mdd-agent config set agent_id embedded-reader-1 --config /opt/mdd-agent/config.json
/opt/mdd-agent/current/mdd-agent config set server gateway.example.com:8443 --config /opt/mdd-agent/config.json
/opt/mdd-agent/current/mdd-agent config set tls_sha256 VERIFIED_CERT_SHA256 --config /opt/mdd-agent/config.json
/opt/mdd-agent/current/mdd-agent config set token --stdin --config /opt/mdd-agent/config.json < /secure/path/agent-token
/opt/mdd-agent/current/mdd-agent config show --config /opt/mdd-agent/config.json
```

token 输入文件由用户安全提供，权限 `0600`，使用后按凭据管理策略处理，不保留在下载/共享目录。
证书值是通过可信渠道取得的**服务端叶证书 DER SHA-256**，不是 SPKI、公钥或整条证书链的哈希。
禁止从未经认证的网络取值后自动接受。`config init` 自动生成独立本地控制 token；不要复制其他主机
配置。中间步骤的 `ready=false` 可表示尚未填齐字段，最后 `config show` 应为 `ready=true`；它只验证
配置，不证明已连接。默认保留 `modem_enabled=false` 及 raw USB 关闭，不为了本说明修改已有配置。

**3. 先前台启动，再核对真实连接。**

```sh
/opt/mdd-agent/current/mdd-agent run --config /opt/mdd-agent/config.json
```

另一个终端以同一用户读取：

```sh
/opt/mdd-agent/current/mdd-agent status --config /opt/mdd-agent/config.json
/opt/mdd-agent/current/mdd-agent topology --config /opt/mdd-agent/config.json
```

确认 Core `/v1/agents`/设备页面出现正确 ID、新鲜上报和实际 reader/card。没有插卡时，不把空 topology
判成读卡成功。控制监听保持 literal loopback，默认 `127.0.0.1:35964`，不得开放给外网。若该端口已有
其他 Agent，先确认其归属，不换端口偷偷启动第二个硬件 owner。修改配置后需受控停止进程再启动；
不能假设运行中的 Agent 自动加载新配置。

**4. 用户手动配置 crontab 保活。**

完成前台检查并用 Ctrl+C 正常退出后，由用户确认 cron daemon 会在系统启动时运行，再以同一运行
账户执行 `crontab -e`。使用系统提供的 `flock -n`，先通过 `command -v flock` 确认实际绝对路径。
以下例子假设它在 `/usr/bin/flock`，`logs/` 已创建且为 `0700`；不要覆盖用户已有 crontab：

```cron
* * * * * umask 077; /usr/bin/flock -n /opt/mdd-agent/run.lock /opt/mdd-agent/current/mdd-agent run --config /opt/mdd-agent/config.json >> /opt/mdd-agent/logs/agent.log 2>&1
```

这一条在开机后和进程退出后最多约一分钟尝试启动；健康进程持锁时立即退出，不周期重启、不杀进程、
不检查网络来决定重启，也不操作用户 4G/借用开关。Agent 内部网络重连继续使用原有逻辑。不要再叠加
systemd、其他 watchdog 或重复 cron 项。无 `flock` 时先按发行版方式提供它，不用 `pgrep`/进程名代替
锁。日志保留错误但需限制占盘；持续配置错误应注释该 cron 项并修正，而不是无限堆日志。

进程存活但 runtime 已失败或被用户暂停时，cron 不会自动重启它；此时用 `status`、日志和 Core 上报
定位，保留原始错误，不把保活当健康验收。pcscd 的保活仍由系统/用户另行负责。

**5. 停用、升级、回退。**

先用 `crontab -e` 注释自己的那一条，并确认没有尚在启动的同项任务；保留其他项目的 cron。确认该
Agent 没有活动通话/读卡操作后，通过进程列表核对精确可执行路径、账户与 `--config` 参数，只对
这个 PID 发 SIGTERM，确认退出且锁已释放。`mdd-agent stop` 仅停止内部 runtime，不会结束宿主进程，
不能用它代替上述进程停用；禁止 `killall`、按模糊名字杀进程或删除活跃锁文件。

保留原版本及权限受控的配置、`state/` 备份，核对新包后在进程停止期间切换 `current`，重复前台和
Core 读回检查，成功后恢复这一条 cron。回退也先停进程；仅使用确认兼容现有配置/状态的旧版，不
丢弃或盲目回滚 operation/state 数据来强行启动。已有正式安装器部署不改走此流程。

本节是现有 CLI 的手动操作说明，不是新的安装器或实机验收报告；尚未据此完成嵌入式/ARM64 验收。
参考：[PC/SC Lite](https://pcsclite.apdu.fr/)、[flock](https://man7.org/linux/man-pages/man1/flock.1.html)、
[crontab](https://man7.org/linux/man-pages/man5/crontab.5.html)。

## 无本地 modem 策略时的安全默认

modem 策略的持久来源仍是当前 Agent 本地库，按 equipment 与 SIM 身份组合保存；
有本地策略时沿用它，包括 Core 暂时离线的情况。Web 修改复用现有 Core 到当前在线
Agent 的命令通道。不新增 Core 按卡持久化、跨主机配置迁移或新的连接协议。
换主机或换 modem 后如果当地没有记录，不承诺自动带回另一主机的配置。

保留现有已授权初始化模板、首次发现冻结和基线保护；最终没有策略时，4G、借用和
数据漫游的默认意图均关闭，不改变飞行模式、不改 APN、不把兜底写成用户策略。
已有宿主数据连接通过原生命周期锁及精确 attachment/equipment/card/session 校验尝试
断开。取得锁后再次发现用户已保存策略，就取消默认断开，交回正常策略协调。
活动通话/租约、身份改变、状态未知或平台操作失败时保留非就绪状态并退避，不盲目复位。

只有新鲜观察到 `disconnected` 才把这个兜底记为就绪；停止调用返回成功也不等于已断开。
Linux 未持有 dataClaim 时的 `StopData` 本身不执行操作；初始遗留 bearer 清理由 `acquire`
负责，不能仅凭停止调用返回值推断承载已关闭。原 NM/nft 隔离仍保留，实际断开未确认会
继续显示异常。Windows 复用现有精确 SIM 的 MBN
断开操作，不另造驱动或改系统全局网络配置。本批不是所有平台实际断开的验收声明。

本批失败方式：未执行默认关闭却显示就绪、把 unknown/no-op 当成功、无节制重复断开、
断开错误的 SIM、打断活动业务，以及等锁期间覆盖用户刚保存的配置。
行为回归见 `go-runtime/internal/agentpolicy/default_data_test.go`。完整签名
[GitHub run 36972429762](https://github.com/lovitus/mdd-sim-gateway/actions/runs/36972429762)
已通过，实际验证源码为 `c57131fe07b4d02523ab2d0144c3638e8387dad4`，tree 为
`dc02684c60366f9a0ea2ae5f7d35f6b109fbbf8a`。随后仅更正提交身份的 `70cc08b` 与它同树，
不能将 workflow 改称运行在后一个提交上。
同树候选保留于不可移动的 `archive/2026-10-02/modem-default-data-qualified` tag；
它仅保留验证入口和证据来源，不是 release、部署或恢复旧代码的授权。

同一次 hosted 运行把新增测试放到未修复 `f847b03`：两个顶层测试均编译成功后按行为失败，
分别暴露把未确认承载记为 ready、未进入受锁保护的默认断开路径。修复版本两个测试均在
race 检测下通过， scoped JSONL 没有 fail/skip，两份 stderr 为空。完整 workflow 仍包含
原 Linux Core race、Provider、跨平台及签名门禁，不推断其中所有可选硬件测试都已执行。
证据工件为 `default-data-evidence-c57131fe07b4d02523ab2d0144c3638e8387dad4`，原始文件
已保留；红/绿 JSONL 的 SHA-256 分别为
`e059444c9a193475f0c404ad85cfc3a44e1b024878fb1d97b4016ae1327ef4d0`、
`74f756537528e6fbb24d5864772123f752ca60652fdc44c31dca6a2da360c0dc`。
收尾仅移除临时反例入口并更新记录，运行代码与永久测试不变，长期 workflow 恢复原样；
收尾不是另一次构建。后续 PR #19 已合并；October 3 单台 Windows EC20 的无策略默认断流
已获授权部署，并由 Agent 与 Windows MBN 双重读回确认。工件摘要和原始证据摘要见
[部署回执](../DEPLOYMENT.md#2026-10-03-单机默认断流部署回执)。不扩展为跨主机恢复、
Linux 无 owner 断流、长期或全平台物理防泄漏验收。
已有关闭策略每轮重复 reconcile 的优化另记延期，不扩大本批。

### Linux 接管安全修复（October 3，行为验证通过）

走读确认三个接管缺口：重复 equipment 检查晚于断开操作；取得 MM ownership 前缺少
现有 VoiceIdle 门禁；已有 owner、没有 dataClaim，却重新看到 connected MM 对象时，
旧缓存仍可能报告 ready/disconnected。这是源码反例，不是已确认的现场流量泄漏。

候选先拒绝歧义身份、校验精确 USB 和持久隔离，再复用 MM VoiceIdle。忙、未知或读取
失败只阻止对应设备的 Disconnect/Inhibit，不重启 MM、不改 APN、无线开关或用户策略。
serial-only 仍使用原有 MM 已停止的前提，不要求不存在的 MM Voice 对象。
Voice 未导出时，仅同一次完整 inventory 证实 LOCKED 且 SIM PIN/PUK，或 DISABLED，
并且全部 bearer 明确未连接，才允许接管以保留 PIN/AT 恢复路径。字段缺失、类型错误、
活动/过渡状态，或 Voice 存在但查询失败都不使用此例外。启用状态却没有 Voice 的设备
仍缺少安全空闲证据，不借助需要 MM debug 支持的 Command/CLCC 回退。
探测超时后的迟到成功不授予许可；取消后不继续处理下一个 bearer。

新出现的冲突使该 owner 的缓存事实和 AT 操作资格失效；关闭其旧 AT 句柄不发送挂断或
无线指令，但这不等于所有驱动上的物理通话无影响证明。歧义 equipment 也会使已保留且
未出现在本轮 inventory 的普通 owner 失效。再次短暂不在 MM inventory 中不会清除这个标记。只有同一设备的新鲜断开读回
及成功 Inhibit 后，才重新发现 AT、建立新的 SIM 会话资格。普通已 inhibit 的设备缺席、
现有 dataClaim 和 raw USB owner 不走这条冲突清理。Disconnect 返回 nil 仍须等待读回。

失败清单：歧义设备被断开、振铃/活动/未知通话被接管、失效缓存再次发布、一次停止应答
被误当作断开、串口路径被误封，以及正常数据 owner 被接管。回归直接运行真实 Prober，
只替换 D-Bus、隔离和 AT 端口这些平台边界；不替换被测接管逻辑。
[首轮 hosted 37101427715](https://github.com/lovitus/mdd-sim-gateway/actions/runs/37101427715)
在精确候选 `446b910dc6910baadb266349bb183b6ed04e4aea` 通过：基线 `ef2479c` 的三个
顶层反例编译后按预期行为失败，六个通话子例失败，两个正常对照通过；候选整包 race 的
49 个测试/子例通过，无 fail/skip。逐项解析原始 JSONL 未发现额外失败、race 或 panic。
红/绿 JSONL SHA-256 为 `d62ef72d70e28c5f876f2bde00f1725de0fec2e8aa2a2df3f865f6f24015859b`、
`61b1834774d3e9922908e6770b8e77a0d18ba19c4ecbc77b9a3f6e2b71e02dcc`。
评审随后要求补齐保留 owner 的歧义、迟到空闲结果和无 Voice 的锁卡恢复；这些修正未被
上述首轮结果覆盖，另以 `446b910` 不改生产源码的反例基线统一复验。
复验 `37102420417` 在临时统计脚本失败：十个实际子例和三个顶层恰按行为失败，但脚本
把单层 `t.Run` 名称中的斜杠误当成另有两层父测试，错误预期 15 而非 13 个失败事件。
只修正事件集合计算，保留原始失败工件；该次未执行绿色阶段，不能算修复版本通过。

修正统计后的 [hosted 37102575007](https://github.com/lovitus/mdd-sim-gateway/actions/runs/37102575007)
在源码 `91947fc993d708f72649ecaceae5c2c61cb07207`、tree
`1f2bffb34fe8e53f673cfc07dae946e24de0a46c` 通过。十个新增反例子例及其三个顶层测试
精确按预期失败，21 个其余测试/子例通过；修复版整包 race 的 72 个测试/子例全部通过。
两份 JSONL 没有跳过、额外失败、race、panic 或编译异常，两份 stderr 为空。红/绿摘要：
`0d49cbb1a785e363d51c27e9575e3b05bc599184d37356e7f29ae8c94ddfc63d`、
`809f8c754dd1df5cdd1820ec8b3c6035b3ac8fef86f08e89597a8400a4eb555c`。
原始工件 `linux-acquisition-evidence-91947fc993d708f72649ecaceae5c2c61cb07207` 已保留。
候选和临时入口分别保存在不可移动的 `archive/2026-10-03/linux-acquisition-qualified`，
初版在 `archive/2026-10-03/linux-acquisition-initial-counterexamples`。它们仅供追溯，
不是恢复旧版本或部署的授权。交付移除临时 workflow/脚本，生产源码和永久测试与 91947fc
一致；长期 CI 不变。整批 CI 和三方最终合并结论以该 PR 回执为准。
本批未部署；Linux 物理验收仍未扩展，既有 Windows 验收不重开。

## 配置与状态核对

`config show` 必须隐藏 token/PIN。变更前后至少核对：

1. 本机配置 mode/owner 与 Agent ID 不变。
2. 本地 `status` 正常，只有一个硬件运行时。
3. Core `/v1/agents` 出现该精确 ID 的新 generation 和新鲜 topology。
4. reader/card/equipment/SIM session 没有被展示字段或历史缓存替换。
5. 活动通话、媒体、数据和 raw ownership 均符合本次操作门禁。

Agent 离线或 generation 变化时，Core 保持 unknown/blocked，不回退到另一台同卡或同 equipment 设备。
恢复动作失败或结果不明时保留原错误层级和本机安全租约，不自动重复付费或凭据操作。

## 当前支持边界

- Windows：SCM 服务、PC/SC、MBN/辅助 AT、受控数据/短信/通话及按 capability 启用的媒体。
- macOS：签名 App/CLI、LaunchAgent、PC/SC/eUICC；Modem/音频/私有数据面只按已验证硬件启用。
- Linux：现有 systemd 安装方式不变，增加上文 reader Agent 手动部署/用户 crontab 保活流程；
  ModemManager 与受隔离数据仍受原平台门禁约束，不宣称无 systemd modem 已适配。
- Android：原生预览客户端已交付 reader 和服务端线路通话/短信能力，见 [Android 手册](../android-agent/README.md)。
  本地 USB modem 不支持，本次不新增适配，也不恢复旧 VPCD raw APDU 或共享 token 设计。

实现与真实平台／设备／运营商验收分别记录在 [版本化台账](../docs/status/README.md)。历史私有报告、旧
artifact、capability 字符串和进程存在均不能扩大支持声明；无可审计证据时保留待验收。

## October 8 candidate: retained modem facts and policy readiness

The reviewed scope is limited to complete Linux data-mode AT projection and
existing-policy convergence. Preserve all public AT fields, including retry
Detail, SIMAPDU and SIMAPDUOnDemand; do not invent a ready capability. A Linux
connected-data projection must still yield Core's typed `sim_apdu_data_active`
when only on-demand APDU is available, retaining the saved VoWiFi intent.

An empty SIM session generation cannot be policy-ready. For stored policies
requiring data off, a connected/connecting observation cannot become ready just
because StopData returned nil; retain bounded backoff until a later disconnected
observation. This shared policy path also applies on Windows. Do not change the
existing no-claim Linux `disconnected` projection to unknown, change user intent,
override active-call/data-lease gates, or extend the physical isolation claim.

Failure checklist: missing APDU capability hides the actionable Core blocker;
ready AT loses retry detail; unowned SIM is ready or mutated; a no-op stop is
reported as disconnected; ordinary observations postpone retry forever; Windows
fails to converge after disconnect. Hosted behavioral counterexamples passed;
full CI and review remain pending. No hardware, user switch, production binary or credentials were
changed. The old default-data test's missing-generation expectation is updated
to the earlier identity gate; its recovery now requires a generation as well as
a disconnected observation, as explicitly required by the owner.

Hosted [37724071647](https://github.com/lovitus/mdd-sim-gateway/actions/runs/37724071647)
at `cc7b5b5af2bb02ff1928aca3d55a69d1750722b8` reverted production files to
unchanged `154947b` while retaining the new behavioral tests. Linux reproduced
five stored-policy subcase failures plus the AT-to-Core blocker failure; Windows
reproduced the same five shared-policy subcases. Post-fix Linux policy/modem
packages passed 126 test/subtest results and Windows policy passed 53, all under
race detection with zero failures/skips. Dependency download messages account
for red stderr; green stderr is empty. This is not physical Windows/Linux modem
or isolation acceptance. The temporary verification entry is absent from delivery.

| Evidence | JSONL SHA-256 |
| --- | --- |
| Linux red | `50883228ee095f9a45ceabfcd74220ed364f3f07be7e4b4e87c124eb9cc7dd7e` |
| Linux green | `8460d715e82bdb954bd15c1f9201c4eceea0a0d432dd7d93ae610c864921f2a9` |
| Windows red | `e13a8908a60b298110265b4a78f0035aa17267f5222a69fc306c97ade6e718b0` |
| Windows green | `09ebf353ec2315bdcf8178ccc18fd2b18b0f4564ab63bdd3d0e567969d2b6920` |

The final independent review of `8e4b9e5` found a remaining transition gap:
after a successful stop while connected, a later unknown, missing or disconnecting
observation could still become ready. The stored-off/flight-mode path now requires
`DataDisconnected`; Linux's existing no-claim projection is unchanged. Five added
table cases exercise the intermediate observation, unchanged retry deadline during
backoff, final disconnected convergence and preserved user intent. The existing
profile-retry test now explicitly supplies a disconnected bearer because it tests
profile application, not missing bearer evidence; its assertions are unchanged.
Hosted run37738205318 at `98a0831d0f4b9800d1ab061bb2fdc95aaa6a9b73`
(tree797b4a15e6cf6822f2d1dba3d439c192716c0de7) reproduced exactly five failing
transition subcases plus their parent on unchanged `8e4b9e5`, independently on
Linux and Windows. Each restored policy package produced 58 test/subtest race
passes, zero failures/skips and empty red/green stderr. The first run attempt
failed at Chrome startup before Linux proof; one failed-job retry produced the
Linux evidence. No browser gate or timeout was weakened. Full run37738205318
subsequently succeeded at that exact head. The temporary proof entry is retained
only in `archive/2026-10-08/inbound-policy-transition-qualified`; delivery restores
normal CI and preserves the qualified production/permanent tests unchanged.
The old full CI37736073811 is not evidence for the new condition. No hardware or
isolation acceptance is implied.

| Transition evidence | JSONL SHA-256 |
| --- | --- |
| Linux red | `def8bf5717b72b35df50a24b80b5322132b8a95d5755c9b05f4ed6cd6e740c8f` |
| Linux green | `41d85c50fd3a563d426cabfde4c1583d3ccabfe69c2a212d83394bf2200c0047` |
| Windows red | `ca114689211b53a1a804afc7e1ec11712a9fc87e28ebbb47bfdd7f0719fcce5e` |
| Windows green | `9e2e6f7a4d85064faf58a2aa4d3d83081349d11f3e8d2ba0e2010457d69ab9dc` |
