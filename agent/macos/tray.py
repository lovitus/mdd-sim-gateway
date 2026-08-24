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
            self.status_item = rumps.MenuItem("状态：正在读取…", callback=None)
            self.menu = [
                self.status_item,
                None,
                rumps.MenuItem("打开 MDD Agent", callback=self.open_window),
                rumps.MenuItem("重新连接设备", callback=self.reconnect),
                rumps.MenuItem("自检", callback=self.self_test),
                rumps.MenuItem("授权通话麦克风", callback=self.authorize_microphone),
                rumps.MenuItem("配置网关", callback=self.configure_gateway),
                rumps.MenuItem("设置 Token", callback=self.configure_token),
                None,
                rumps.MenuItem("退出 MDD Agent", callback=self.quit_agent),
            ]
            self.timer = rumps.Timer(self.refresh, 2)
            self.timer.start()
            # Enter AppKit first, then perform the per-launch TCC check on the GUI thread.
            # Hardware, PPP, SMS and readers are already running and never wait on the prompt.
            self.permission_timer = rumps.Timer(self.check_microphone_on_startup, 0.2)
            self.permission_timer.start()
            self.refresh(None)

        def refresh(self, _sender):
            try:
                value = client.call("status", deadline_ms=1000)
                runtime = value.get("runtime", "unknown")
                modem_count = len(value.get("modems") or [])
                self.status_item.title = f"状态：{runtime} · {modem_count} 个 Modem"
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
            self.permission_timer.stop()
            self._check_microphone(show_authorized=False)

        def _check_microphone(self, *, show_authorized):
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
            if not granted:
                rumps.alert("MDD Agent", "未获得通话麦克风权限，蜂窝语音音频保持禁用。")
                return
            def reprobe():
                # The runtime may still be discovering USB immediately after launch. Retry only
                # this non-billable audio probe, at most five times; never reconnect the host.
                result = None
                error = None
                for _attempt in range(5):
                    try:
                        result = client.call("audio.reprobe", deadline_ms=30_000)
                        if result.get("modems"):
                            break
                    except Exception as exc:
                        error = exc
                    time.sleep(2)
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
                    AppHelper.callAfter(rumps.alert, "MDD Agent", message)
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
