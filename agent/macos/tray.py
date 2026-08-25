"""Native macOS menu-bar host backed by rumps/PyObjC."""

from __future__ import annotations

from pathlib import Path
import threading
import time


def run_menu_bar(host, client, icon_path: Path) -> None:
    try:
        import rumps
    except ImportError as exc:  # pragma: no cover - packaging readiness guard
        raise RuntimeError(
            "macOS GUI requires the bundled rumps/PyObjC menu-bar runtime") from exc
    from .window import MacAgentWindow

    class MddMenuBar(rumps.App):
        def __init__(self):
            super().__init__("MDD", icon=str(icon_path), template=True, quit_button=None)
            self.window = MacAgentWindow(client)
            self.modem_enabled = bool(host.runtime.modem_enabled)
            self.status_item = rumps.MenuItem("状态：正在读取…", callback=None)
            self.menu = self._menu_items()
            self.timer = rumps.Timer(self.refresh, 2)
            self.timer.start()
            # Enter AppKit first, then perform TCC only for the hardware generation that
            # actually enabled modem support.
            self.permission_timer = None
            if self.modem_enabled:
                self.permission_timer = rumps.Timer(self.check_microphone_on_startup, 0.2)
                self.permission_timer.start()
            self.refresh(None)

        def _menu_items(self):
            menu = [
                self.status_item,
                None,
                rumps.MenuItem("打开 MDD Agent", callback=self.open_window),
                rumps.MenuItem("重新连接设备", callback=self.reconnect),
                rumps.MenuItem("自检", callback=self.self_test),
            ]
            if self.modem_enabled:
                menu.append(rumps.MenuItem(
                    "授权通话麦克风", callback=self.authorize_microphone))
            menu.extend([
                rumps.MenuItem(
                    "禁用 4G/5G Modem" if self.modem_enabled else "启用 4G/5G Modem",
                    callback=self.toggle_modem),
                rumps.MenuItem("配置网关", callback=self.configure_gateway),
                rumps.MenuItem("设置 Token", callback=self.configure_token),
                None,
                rumps.MenuItem("退出 MDD Agent", callback=self.quit_agent),
            ])
            return menu

        def refresh(self, _sender):
            try:
                value = client.call("status", deadline_ms=1000)
                runtime = value.get("runtime", "unknown")
                modem_count = len(value.get("modems") or [])
                self.status_item.title = f"状态：{runtime} · {modem_count} 个 Modem"
                current_mode = value.get("modem_enabled") is True
                if current_mode != self.modem_enabled:
                    self.modem_enabled = current_mode
                    if self.permission_timer:
                        self.permission_timer.stop()
                        self.permission_timer = None
                    self.menu = self._menu_items()
                    if current_mode:
                        self.permission_timer = rumps.Timer(
                            self.check_microphone_on_startup, 0.2)
                        self.permission_timer.start()
            except Exception:
                self.status_item.title = "状态：本地控制不可用"

        def open_window(self, _sender):
            self.window.show()

        def reconnect(self, _sender):
            try:
                client.call("reconnect", deadline_ms=60_000)
            except Exception as exc:
                rumps.alert("MDD Agent", str(exc))

        def self_test(self, _sender):
            try:
                result = client.call("self-test", deadline_ms=30_000)
                detail = "通过" if result.get("healthy") else "发现异常"
                rumps.alert("MDD Agent 自检", detail)
            except Exception as exc:
                rumps.alert("MDD Agent 自检", str(exc))

        def authorize_microphone(self, _sender):
            """Request TCC from the stable logged-in application identity."""
            self._check_microphone(show_authorized=True)

        def check_microphone_on_startup(self, _sender):
            if self.permission_timer:
                self.permission_timer.stop()
            self._check_microphone(show_authorized=False)

        def toggle_modem(self, _sender):
            target = not self.modem_enabled
            try:
                client.call("config.validate", {"changes": {"modem_enabled": target}})
                client.call("config.set", {"changes": {"modem_enabled": target}})
                rumps.alert(
                    "MDD Agent",
                    ("4G/5G Modem 已启用；请选择“重新连接设备”应用配置。" if target else
                     "4G/5G Modem 已持久禁用；请选择“重新连接设备”进入 PC/SC-only 模式。"))
            except Exception as exc:
                rumps.alert("MDD Agent 配置", str(exc))

        def _check_microphone(self, *, show_authorized):
            if not host.runtime.modem_enabled:
                return
            try:
                import AVFoundation
                from PyObjCTools import AppHelper
                status = int(AVFoundation.AVCaptureDevice.authorizationStatusForMediaType_(
                    AVFoundation.AVMediaTypeAudio))
                if status == int(AVFoundation.AVAuthorizationStatusAuthorized):
                    if show_authorized:
                        rumps.alert("MDD Agent", "通话麦克风权限已授权。")
                    return
                if status in {
                        int(AVFoundation.AVAuthorizationStatusDenied),
                        int(AVFoundation.AVAuthorizationStatusRestricted)}:
                    rumps.alert(
                        "MDD Agent",
                        "通话麦克风权限被拒绝或受限，请在系统设置 → 隐私与安全性 → "
                        "麦克风中允许 MDD Agent。")
                    return

                def completed(granted):
                    AppHelper.callAfter(self._microphone_permission_finished, bool(granted))

                AVFoundation.AVCaptureDevice.requestAccessForMediaType_completionHandler_(
                    AVFoundation.AVMediaTypeAudio, completed)
            except Exception as exc:
                rumps.alert("MDD Agent 麦克风权限", str(exc))

        def _microphone_permission_finished(self, granted):
            if not host.runtime.modem_enabled:
                return
            if not granted:
                rumps.alert("MDD Agent", "未获得通话麦克风权限，蜂窝语音音频保持禁用。")
                return
            def reprobe():
                # The runtime may still be discovering USB immediately after launch. Retry only
                # this non-billable audio probe, at most five times; never reconnect the host.
                generation = str(host.runtime.snapshot().get("agent_run_id") or "")
                def generation_active():
                    current = host.runtime.snapshot()
                    return bool(host.runtime.modem_enabled and generation and
                                str(current.get("agent_run_id") or "") == generation)
                result = None
                error = None
                for _attempt in range(5):
                    if not generation_active():
                        return
                    try:
                        result = client.call("audio.reprobe", deadline_ms=30_000)
                        if not generation_active():
                            return
                        if result.get("modems"):
                            break
                    except Exception as exc:
                        error = exc
                    time.sleep(2)
                if not generation_active():
                    return
                if result and result.get("ready"):
                    message = "通话麦克风已授权，设备音频自检通过。"
                elif error:
                    message = f"权限已授权，但音频重检失败：{error}"
                else:
                    reason = next((item.get("reason") for item in (result or {}).get("modems", [])
                                   if item.get("reason")), "尚未发现可用的 Modem 音频端点")
                    message = f"权限已授权，但通话音频尚未就绪：{reason}"
                try:
                    from PyObjCTools import AppHelper
                    if generation_active():
                        def alert_if_current():
                            if generation_active():
                                rumps.alert("MDD Agent", message)
                        AppHelper.callAfter(alert_if_current)
                except Exception:
                    pass

            threading.Thread(target=reprobe, name="mdd-audio-reprobe", daemon=True).start()

        def configure_gateway(self, _sender):
            try:
                current = client.call("config.show", deadline_ms=3000)
                response = rumps.Window(
                    "网关地址（host:port）", "MDD Agent 配置",
                    default_text=str(current.get("server") or ""),
                    ok="保存", cancel="取消").run()
                if response.clicked:
                    client.call("config.validate", {"changes": {"server": response.text}})
                    client.call("config.set", {"changes": {"server": response.text}})
                    rumps.alert("MDD Agent", "已保存；请选择“重新连接设备”应用配置。")
            except Exception as exc:
                rumps.alert("MDD Agent 配置", str(exc))

        def configure_token(self, _sender):
            try:
                response = rumps.Window(
                    "新的 Agent Token", "MDD Agent 凭据", secure=True,
                    ok="保存", cancel="取消").run()
                if response.clicked and response.text:
                    client.call("config.set", {"changes": {"token": response.text}})
                    rumps.alert(
                        "MDD Agent",
                        "Token 已保存到当前用户配置文件；请选择“重新连接设备”应用新 Token。")
            except Exception as exc:
                rumps.alert("MDD Agent 凭据", str(exc))

        def quit_agent(self, _sender):
            self.timer.stop()
            host.stop()
            rumps.quit_application()

    try:
        MddMenuBar().run()
    finally:
        host.stop()
