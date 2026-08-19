# EC20-CE HDLG Windows 批量升级手册

这套流程只适用于已经逐台核对为 `EC20CEHDLG` 硬件分支、当前固件属于
`EC20CEHDLGR06...`、目标为 `EC20CEHDLGR08A06M1G` 的模块。型号页面仅显示
“EC20F”不够，完整 `ATI/AT+GMR` 和模块标签必须一致。

## 绝对规则

- 固件、QFlash 和操作步骤可以复用；QCN/EFS 不能复用。
- 每台模块必须生成以自身 15 位 IMEI 命名的 XQCN 和 EFS TAR，并校验 SHA-256。
- QCN 含 IMEI、SN、RF 校准和 NV。只能恢复到产生它的原模块。
- 一次只连接一台待升级模块。稳定供电、直连 USB，禁用睡眠。
- QFenix 在本流程中只做识别和备份。不要用它刷 EC20 NAND 固件。
- 刷写开始后不得拔线、关 QFlash、休眠、重启或断电。
- 成功升级后不要把 R06 QCN 整包恢复到 R08；备份只用于同一模块的灾难恢复。

## 离线目录

Windows 完整离线包应包含：

```text
MDD-EC20-HDLG-Upgrade-Kit\
  Tools\QFlash_V7.0\
  Tools\qfenix.exe
  Firmware\R08A06\
  Firmware\R06A13\
  Firmware\EC20CEHDLGR08A06M1G.zip
  Firmware\EC20CEHDLGR06A13M1G.zip
  Scripts\
  Docs\
  Backups\<IMEI>\<timestamp>\
  SHA256SUMS.txt
  Fleet-Upgrade-Log.csv
```

工具和固件路径应使用英文且不含空格。Windows 10/11 以管理员身份运行 PowerShell 和 QFlash。

## 每台设备的流程

在管理员 PowerShell 中：

```powershell
Set-ExecutionPolicy -Scope Process Bypass
cd "$env:USERPROFILE\Documents\MDD-EC20-HDLG-Upgrade-Kit"
.\Scripts\00-Verify-Kit.ps1
.\Scripts\01-Inventory-And-Backup.ps1
.\Scripts\02-Prepare-For-QFlash.ps1 -LaunchQFlash
```

校验必须输出 `KIT_VERIFY_PASS`，备份必须输出 `BACKUP_PASS`。否则停止，不要刷写。
完整 XQCN 会逐项扫描普通 NV、SIM NV、RFNV 和 EFS；当前实机约需 25 分钟，长时间没有新输出不等于
卡死。如果完整 XQCN 已成功生成、但随后快速 TAR 因工具警告中断，可在排除问题后运行
`.\Scripts\01-Inventory-And-Backup.ps1 -ResumeLatest`；它只会复用同一 IMEI 下最新且大小有效的 XQCN。

QFlash 中：

1. `Load FW Files` 选择
   `Firmware\R08A06\update\firehose\prog_nand_firehose_9x07.mbn`。
2. QFlash 会自动列出 ENPRG/NPRG、Firehose programmer、rawprogram 和 patch 文件，保持勾选。
3. COM Port 选择脚本输出的 `QFLASH_DM_PORT` 数字部分。例如 `COM13` 选择 `13`。
4. Baudrate 选择 `460800`；Configuration 保持默认。
5. 不要选择 AT、NMEA、蓝牙串口或 Qualcomm 9008 端口。
6. 点击 `Start` 一次，等待蓝色 `PASS`。

PASS 后等待 AT/DM/NMEA 端口重新出现，再运行：

```powershell
.\Scripts\03-Verify-After-QFlash.ps1
.\Scripts\04-Restore-Mdd-Agent.ps1 -RequireData
```

第三步必须输出 `VERIFY_PASS`，并确认固件为 `EC20CEHDLGR08A06M1G`。脚本会把结果追加到
`Fleet-Upgrade-Log.csv`。该文件和 `Backups` 都是现场私有数据，禁止提交 Git。
备份脚本会在 DIAG 读取后执行一次标准 `AT+CFUN=1,1` 复位，避免 Windows WWAN 长期停在
`Stack is off`。恢复脚本不再只检查进程；使用 `-RequireData` 时必须等到真实移动数据重新 Connected。

## 常见问题

- QFlash 串口列表为空：确认设备管理器存在 `Quectel USB DM Port`，关闭 Agent、Gammu、串口终端。
- 只有 `Quectel QDLoader 9008`：停止批量流程，不要反复点击 Start。使用精确固件和该设备自己的
  备份进入人工恢复。
- QFlash 显示 COM7：它常是蓝牙串口；必须选择脚本给出的 Quectel DM 端口。
- 刷后无 IMEI、无信号、FTM/CFUN 5：立即隔离该设备，保留日志，只能用其自己的 QCN 和精确回退包。
- Windows MBN SMS 报 `0x8000000A`：R08 下独立 AT 短信可能仍可用；MDD 组合 Provider 会在
  MBN 明确未就绪时使用已独占的 AT 信令后端。
- 数据在线但 VoWiFi/SIM APDU 暂停：这是 Windows WWAN 对 SIM 的所有权边界，不是固件失败。

## 来源和维护

- Quectel QFlash User Guide：
  <https://forums.quectel.com/uploads/short-url/ApQc64SUOmNIU7ibGcQNPB5prxD.pdf>
- Quectel Windows 驱动和当前 QFlash 下载入口：
  <https://www.quectel.com/product/lte-ec25-series/>
- QFenix 源码、版本和许可证：<https://github.com/iamromulan/qfenix>

第三方镜像取得的固件必须先和维护者保存的 SHA-256 对照；正式批量部署应向 Quectel 或授权供应商
取得与完整 SKU 相符的包、发布说明、回退政策和校验值。
