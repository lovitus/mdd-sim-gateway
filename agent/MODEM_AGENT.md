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
- Linux：systemd Agent、PC/SC/eUICC、ModemManager 与明确支持的受隔离数据路径。
- Android：当前没有受支持客户端。未来实现必须使用统一 Agent 高层协议和每设备凭据，不能恢复旧 VPCD
  raw APDU 或共享 token 设计。

真实平台、设备、SIM、运营商和功能验收以 `TODO_CURRENT_RECOVERY.md` 顶部唯一游标为准；历史文档、旧
artifact、capability 字符串和进程存在均不能扩大支持声明。
