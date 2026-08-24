"""Native AppKit status window for the menu-bar Agent host."""

from __future__ import annotations

import json
import threading


class MacAgentWindow:
    """A small native window; it is a control client and never owns hardware."""

    def __init__(self, client):
        try:
            import objc
            from AppKit import (
                NSBackingStoreBuffered, NSButton, NSMakeRect, NSScrollView, NSTextField,
                NSTextView, NSWindow, NSWindowStyleMaskClosable,
                NSWindowStyleMaskMiniaturizable, NSWindowStyleMaskResizable,
                NSWindowStyleMaskTitled,
            )
            from Foundation import NSObject
        except ImportError as exc:  # pragma: no cover - packaging guard
            raise RuntimeError("the bundled PyObjC Cocoa runtime is unavailable") from exc

        outer = self

        class Controller(NSObject):
            def init(self):
                self = objc.super(Controller, self).init()
                if self is None:
                    return None
                self.refreshing = False
                style = (NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                         NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable)
                self.window = NSWindow.alloc().initWithContentRect_styleMask_backing_defer_(
                    NSMakeRect(0, 0, 760, 520), style, NSBackingStoreBuffered, False)
                self.window.setTitle_("MDD Agent")
                self.window.setMinSize_((620, 380))
                self.window.center()
                self.window.setDelegate_(self)
                content = self.window.contentView()

                self.summary = NSTextField.labelWithString_("正在读取 Agent 状态…")
                self.summary.setFrame_(NSMakeRect(20, 478, 720, 24))
                content.addSubview_(self.summary)

                for index, (title, action) in enumerate((
                        ("刷新", "refreshNow:"), ("重新连接设备", "reconnect:"),
                        ("自检", "selfTest:"), ("查看日志", "showLogs:"))):
                    button = NSButton.alloc().initWithFrame_(
                        NSMakeRect(20 + index * 142, 438, 132, 30))
                    button.setTitle_(title)
                    button.setTarget_(self)
                    button.setAction_(action)
                    content.addSubview_(button)

                scroll = NSScrollView.alloc().initWithFrame_(NSMakeRect(20, 20, 720, 405))
                scroll.setHasVerticalScroller_(True)
                scroll.setHasHorizontalScroller_(True)
                self.text = NSTextView.alloc().initWithFrame_(NSMakeRect(0, 0, 700, 405))
                self.text.setEditable_(False)
                self.text.setRichText_(False)
                scroll.setDocumentView_(self.text)
                content.addSubview_(scroll)
                return self

            def windowShouldClose_(self, _sender):
                # Closing the window hides it. Only the menu's explicit Exit stops hardware.
                self.window.orderOut_(None)
                return False

            def refreshNow_(self, _sender):
                self.refresh()

            def reconnect_(self, _sender):
                self.runAction_("reconnect")

            def selfTest_(self, _sender):
                self.runAction_("self-test")

            def showLogs_(self, _sender):
                self.runAction_("logs")

            def runAction_(self, method):
                def work():
                    try:
                        params = {"lines": 300} if method == "logs" else {}
                        value = client.call(method, params, deadline_ms=60_000)
                        payload = {"action": method, "result": value}
                    except Exception as exc:
                        payload = {"action": method, "error": str(exc)}
                    self.performSelectorOnMainThread_withObject_waitUntilDone_(
                        "applyPayload:", json.dumps(payload, ensure_ascii=False), False)
                threading.Thread(target=work, name=f"mdd-mac-{method}", daemon=True).start()

            def refresh(self):
                if self.refreshing:
                    return
                self.refreshing = True

                def work():
                    try:
                        payload = {"status": client.call("status", deadline_ms=1500),
                                   "devices": client.call("devices", deadline_ms=4000)}
                    except Exception as exc:
                        payload = {"error": str(exc)}
                    self.performSelectorOnMainThread_withObject_waitUntilDone_(
                        "applyPayload:", json.dumps(payload, ensure_ascii=False), False)
                threading.Thread(target=work, name="mdd-mac-window-refresh", daemon=True).start()

            def applyPayload_(self, serialized):
                self.refreshing = False
                try:
                    value = json.loads(str(serialized))
                except Exception:
                    value = {"error": str(serialized)}
                status = value.get("status") or {}
                if status:
                    self.summary.setStringValue_(
                        f"运行时：{status.get('runtime', 'unknown')}   "
                        f"Modem：{len(status.get('modems') or [])}")
                elif value.get("error"):
                    self.summary.setStringValue_(f"本地控制不可用：{value['error']}")
                self.text.setString_(json.dumps(value, ensure_ascii=False, indent=2))

        self.controller = Controller.alloc().init()

    def show(self):
        self.controller.window.makeKeyAndOrderFront_(None)
        self.controller.refresh()

    def refresh(self):
        self.controller.refresh()

