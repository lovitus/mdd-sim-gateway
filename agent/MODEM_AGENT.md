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
Linux 当前未持有 bearer 时的停止接口可能不执行操作，不能从返回值推断承载已关闭；
原 NM/nft 隔离仍保留，实际断开未确认会继续显示异常。Windows 复用现有精确 SIM 的 MBN
断开操作，不另造驱动或改系统全局网络配置。本批不是所有平台实际断开的验收声明。

本批失败方式：未执行默认关闭却显示就绪、把 unknown/no-op 当成功、无节制重复断开、
断开错误的 SIM、打断活动业务，以及等锁期间覆盖用户刚保存的配置。
行为回归见 `go-runtime/internal/agentpolicy/default_data_test.go`；当前尚待 GitHub hosted
红到绿及整批验证，未部署。已有关闭策略每轮重复 reconcile 的优化另记延期，不扩大本批。

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
