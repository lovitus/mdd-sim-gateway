"""Native Windows notification-area controller for the optional GUI client."""

from __future__ import annotations

import os
import sys
import threading
import time
from pathlib import Path


class WindowsTray:
    """Own one Shell_NotifyIcon on a dedicated Win32 message-loop thread."""

    WM_TRAY = 0x0400 + 20
    WM_UPDATE_TIP = 0x0400 + 21
    CMD_OPEN = 1001
    CMD_RESTART = 1002
    CMD_EXIT = 1003

    def __init__(self, root, *, on_open, on_restart, on_exit):
        self.root = root
        self.on_open = on_open
        self.on_restart = on_restart
        self.on_exit = on_exit
        self.hwnd = None
        self.icon = None
        self._owned_icons = []
        self._tip = "MDD Agent"
        self._tip_lock = threading.Lock()
        self._ready = threading.Event()
        self._thread = None
        self.error = None

    @staticmethod
    def _diagnostic_path():
        base = Path(os.environ.get("LOCALAPPDATA") or Path.home())
        return base / "MDD" / "Agent" / "gui-tray-error.log"

    def _record_error(self, exc):
        try:
            path = self._diagnostic_path()
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"{type(exc).__name__}: {exc}\n", encoding="utf-8")
        except Exception:
            pass

    def _clear_error(self):
        try:
            self._diagnostic_path().unlink(missing_ok=True)
        except Exception:
            pass

    @property
    def available(self):
        return self._ready.is_set() and self.hwnd is not None and self.error is None

    def start(self, timeout=3.0):
        if os.name != "nt":
            return False
        self._thread = threading.Thread(
            target=self._run, name="mdd-agent-tray", daemon=True
        )
        self._thread.start()
        self._ready.wait(timeout)
        return self.available

    def set_tooltip(self, value):
        with self._tip_lock:
            self._tip = str(value or "MDD Agent")[:127]
        if self.available:
            try:
                import win32gui
                win32gui.PostMessage(self.hwnd, self.WM_UPDATE_TIP, 0, 0)
            except Exception:
                pass

    def stop(self):
        hwnd = self.hwnd
        if hwnd:
            try:
                import win32con
                import win32gui
                win32gui.PostMessage(hwnd, win32con.WM_CLOSE, 0, 0)
            except Exception:
                pass

    def _post_to_ui(self, callback):
        try:
            self.root.after(0, callback)
        except Exception:
            pass

    def _load_icon(self, win32con, win32gui):
        base = Path(getattr(sys, "_MEIPASS", Path(__file__).resolve().parents[1]))
        icon_path = base / "assets" / "mdd-agent.ico"
        if icon_path.is_file():
            flags = win32con.LR_LOADFROMFILE | win32con.LR_DEFAULTSIZE
            icon = win32gui.LoadImage(
                0, str(icon_path), win32con.IMAGE_ICON, 0, 0, flags
            )
            self._owned_icons = [icon]
            return icon
        try:
            large, small = win32gui.ExtractIconEx(os.path.abspath(os.sys.executable), 0)
            self._owned_icons = list(large or []) + list(small or [])
            if small:
                return small[0]
            if large:
                return large[0]
        except Exception:
            self._owned_icons = []
        return win32gui.LoadIcon(0, win32con.IDI_APPLICATION)

    def _notify_data(self, win32gui):
        with self._tip_lock:
            tip = self._tip
        flags = win32gui.NIF_ICON | win32gui.NIF_MESSAGE | win32gui.NIF_TIP
        return self.hwnd, 0, flags, self.WM_TRAY, self.icon, tip

    def _add_icon(self, win32gui):
        win32gui.Shell_NotifyIcon(win32gui.NIM_ADD, self._notify_data(win32gui))

    def _update_tip(self, hwnd, _message, _wparam, _lparam):
        import win32gui
        win32gui.Shell_NotifyIcon(win32gui.NIM_MODIFY, self._notify_data(win32gui))
        return 0

    def _show_menu(self, hwnd, win32con, win32gui):
        menu = win32gui.CreatePopupMenu()
        try:
            win32gui.AppendMenu(menu, win32con.MF_STRING, self.CMD_OPEN, "打开 MDD Agent")
            win32gui.AppendMenu(menu, win32con.MF_STRING, self.CMD_RESTART, "重启服务")
            win32gui.AppendMenu(menu, win32con.MF_SEPARATOR, 0, "")
            win32gui.AppendMenu(menu, win32con.MF_STRING, self.CMD_EXIT,
                                "退出 GUI（服务继续运行）")
            x, y = win32gui.GetCursorPos()
            try:
                win32gui.SetForegroundWindow(hwnd)
            except Exception:
                pass
            command = win32gui.TrackPopupMenu(
                menu,
                win32con.TPM_LEFTALIGN | win32con.TPM_RIGHTBUTTON |
                win32con.TPM_RETURNCMD,
                x, y, 0, hwnd, None,
            )
            if command == self.CMD_OPEN:
                self._post_to_ui(self.on_open)
            elif command == self.CMD_RESTART:
                self._post_to_ui(self.on_restart)
            elif command == self.CMD_EXIT:
                self._post_to_ui(self.on_exit)
        finally:
            win32gui.DestroyMenu(menu)
        return 0

    def _on_tray(self, hwnd, _message, _wparam, event):
        import win32con
        import win32gui
        if event in (win32con.WM_LBUTTONUP, win32con.WM_LBUTTONDBLCLK):
            self._post_to_ui(self.on_open)
        elif event in (win32con.WM_RBUTTONUP, win32con.WM_CONTEXTMENU):
            return self._show_menu(hwnd, win32con, win32gui)
        return 0

    def _on_close(self, hwnd, _message, _wparam, _lparam):
        import win32gui
        win32gui.DestroyWindow(hwnd)
        return 0

    def _on_destroy(self, _hwnd, _message, _wparam, _lparam):
        import win32gui
        self.hwnd = None
        win32gui.PostQuitMessage(0)
        return 0

    def _run(self):
        try:
            import win32api
            import win32con
            import win32gui

            taskbar_created = win32gui.RegisterWindowMessage("TaskbarCreated")
            handlers = {
                self.WM_TRAY: self._on_tray,
                self.WM_UPDATE_TIP: self._update_tip,
                win32con.WM_CLOSE: self._on_close,
                win32con.WM_DESTROY: self._on_destroy,
                taskbar_created: lambda *_args: self._add_icon(win32gui) or 0,
            }
            instance = win32api.GetModuleHandle(None)
            window_class = win32gui.WNDCLASS()
            window_class.hInstance = instance
            window_class.lpszClassName = f"MDD_AGENT_TRAY_{os.getpid()}"
            window_class.style = win32con.CS_VREDRAW | win32con.CS_HREDRAW
            window_class.hCursor = win32api.LoadCursor(0, win32con.IDC_ARROW)
            window_class.hbrBackground = win32con.COLOR_WINDOW
            window_class.lpfnWndProc = handlers
            win32gui.RegisterClass(window_class)
            self.hwnd = win32gui.CreateWindow(
                window_class.lpszClassName, "MDD Agent",
                win32con.WS_OVERLAPPED | win32con.WS_SYSMENU,
                0, 0, win32con.CW_USEDEFAULT, win32con.CW_USEDEFAULT,
                0, 0, instance, None
            )
            win32gui.UpdateWindow(self.hwnd)
            self.icon = self._load_icon(win32con, win32gui)
            last_add_error = None
            for _attempt in range(4):
                try:
                    self._add_icon(win32gui)
                    last_add_error = None
                    break
                except Exception as exc:
                    last_add_error = exc
                    time.sleep(0.25)
            if last_add_error is not None:
                raise last_add_error
            self._clear_error()
            self._ready.set()
            win32gui.PumpMessages()
        except Exception as exc:
            self.error = exc
            self._record_error(exc)
            self._ready.set()
        finally:
            try:
                import win32gui
                if self.hwnd:
                    win32gui.Shell_NotifyIcon(win32gui.NIM_DELETE,
                                              (self.hwnd, 0))
            except Exception:
                pass
            for handle in self._owned_icons:
                try:
                    win32gui.DestroyIcon(handle)
                except Exception:
                    pass
            self.hwnd = None
