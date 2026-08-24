"""Small windowed client for the same local control API used by the CLI."""

from __future__ import annotations

import json
import os
import sys
import threading
from pathlib import Path

if sys.platform != "darwin":
    import tkinter as tk
    from tkinter import messagebox, ttk
else:  # The macOS build is native AppKit and must not depend on an optional Tcl/Tk install.
    tk = messagebox = ttk = None

try:
    from config_store import ConfigStore
    from local_control import ControlClient
    from service_manager import ServiceManager
except ModuleNotFoundError:
    from .config_store import ConfigStore
    from .local_control import ControlClient
    from .service_manager import ServiceManager


class AgentGui:
    def __init__(self, root, store, client, services):
        self.root, self.store, self.client, self.services = root, store, client, services
        self.last_render = ""
        self.refreshing = False
        self.tray = None
        self.window_icon = None
        root.title("MDD Agent")
        root.minsize(680, 420)
        self._set_window_icon(root)
        frame = ttk.Frame(root, padding=16)
        frame.pack(fill="both", expand=True)
        self.summary = ttk.Label(frame, text="正在读取服务状态…", font=("Segoe UI", 12, "bold"))
        self.summary.pack(anchor="w")
        buttons = ttk.Frame(frame)
        buttons.pack(fill="x", pady=(12, 8))
        for label, action in (("启动服务", "start"), ("停止服务", "stop"),
                              ("重启服务", "restart")):
            ttk.Button(buttons, text=label,
                       command=lambda value=action: self.service_action(value)).pack(side="left", padx=3)
        ttk.Button(buttons, text="重新连接设备", command=self.reconnect).pack(side="left", padx=3)
        ttk.Button(buttons, text="自检", command=self.self_test).pack(side="left", padx=3)
        ttk.Button(buttons, text="配置", command=self.configure).pack(side="left", padx=3)
        ttk.Button(buttons, text="日志", command=self.show_logs).pack(side="left", padx=3)
        self.text = tk.Text(frame, wrap="none", state="disabled", font=("Consolas", 10))
        self.text.pack(fill="both", expand=True)
        self._start_tray()
        root.protocol("WM_DELETE_WINDOW", self.hide_to_tray)
        self.refresh()

    @staticmethod
    def _asset_path(name):
        if getattr(sys, "frozen", False):
            return Path(getattr(sys, "_MEIPASS", Path(sys.executable).parent)) / "assets" / name
        return Path(__file__).resolve().parent / "assets" / name

    def _set_window_icon(self, window):
        try:
            if self.window_icon is None:
                self.window_icon = tk.PhotoImage(file=str(self._asset_path("mdd-agent.png")))
            window.iconphoto(True, self.window_icon)
        except (OSError, tk.TclError):
            pass

    def _start_tray(self):
        if os.name != "nt":
            return
        try:
            try:
                from windows.tray import WindowsTray
            except ModuleNotFoundError:
                from .windows.tray import WindowsTray
            tray = WindowsTray(
                self.root,
                on_open=self.show_window,
                on_restart=lambda: self.service_action("restart"),
                on_exit=self.exit_gui,
            )
            if tray.start():
                self.tray = tray
        except Exception:
            self.tray = None

    def show_window(self):
        self.root.deiconify()
        self.root.lift()
        try:
            self.root.focus_force()
        except tk.TclError:
            pass

    def hide_to_tray(self):
        if self.tray and self.tray.available:
            self.root.withdraw()
        else:
            self.exit_gui()

    def exit_gui(self):
        if self.tray:
            self.tray.stop()
            self.tray = None
        try:
            self.root.destroy()
        except tk.TclError:
            pass

    def render(self, value):
        stable = json.loads(json.dumps(value))
        if isinstance(stable.get("runtime"), dict):
            stable["runtime"].pop("uptime_seconds", None)
        rendered = json.dumps(stable, ensure_ascii=False, indent=2)
        if rendered == self.last_render:
            return
        self.last_render = rendered
        service = value.get("service") or {}
        runtime = value.get("runtime") or {}
        self.summary.configure(text=f"服务：{service.get('state', 'unknown')}    "
                                    f"运行时：{runtime.get('runtime', 'unavailable')}")
        if self.tray:
            self.tray.set_tooltip(
                f"MDD Agent — 服务 {service.get('state', 'unknown')} / "
                f"运行时 {runtime.get('runtime', 'unavailable')}"
            )
        self.text.configure(state="normal")
        self.text.delete("1.0", "end")
        self.text.insert("1.0", rendered)
        self.text.configure(state="disabled")

    def refresh(self):
        if not self.refreshing:
            self.refreshing = True
            def load():
                try:
                    service = self.services.status()
                    runtime = self.client.call("status", deadline_ms=1000)
                    devices = self.client.call("devices", deadline_ms=3000)
                    value = {"service": service, "runtime": runtime, "devices": devices}
                except Exception as exc:
                    try:
                        service = self.services.status()
                    except Exception:
                        service = {"state": "unavailable"}
                    value = {"service": service,
                             "runtime": {"runtime": "unavailable", "error": str(exc)},
                             "devices": None}
                self.root.after(0, lambda: self._finish_refresh(value))
            threading.Thread(target=load, name="mdd-agent-gui-refresh", daemon=True).start()
        self.root.after(2000, self.refresh)

    def _finish_refresh(self, value):
        self.refreshing = False
        self.render(value)

    def service_action(self, action):
        self._background(lambda: self.services.action(action))

    def reconnect(self):
        self._background(lambda: self.client.call("reconnect", deadline_ms=60_000))

    def self_test(self):
        def done(result):
            messagebox.showinfo("MDD Agent 自检", json.dumps(result, ensure_ascii=False, indent=2))
        self._background(lambda: self.client.call("self-test", deadline_ms=30_000), done)

    def _background(self, operation, done=None):
        def run():
            try:
                result = operation()
                if done:
                    self.root.after(0, lambda: done(result))
            except Exception as exc:
                self.root.after(0, lambda detail=str(exc):
                                messagebox.showerror("MDD Agent", detail))
        threading.Thread(target=run, name="mdd-agent-gui-action", daemon=True).start()

    def show_logs(self):
        def show(result):
            window = tk.Toplevel(self.root)
            window.title("MDD Agent 日志")
            self._set_window_icon(window)
            text = tk.Text(window, wrap="none", font=("Consolas", 9))
            text.pack(fill="both", expand=True)
            text.insert("1.0", "\n".join(result.get("lines") or []))
            text.configure(state="disabled")
            window.minsize(760, 420)
        self._background(lambda: self.client.call("logs", {"lines": 300}, deadline_ms=5000),
                         show)

    def configure(self):
        self._background(lambda: self.client.call("config.show"), self._open_configure)

    def _open_configure(self, current):
        window = tk.Toplevel(self.root)
        window.title("MDD Agent 配置")
        self._set_window_icon(window)
        body = ttk.Frame(window, padding=14)
        body.pack(fill="both", expand=True)
        fields = {}
        for row, (key, label) in enumerate((("server", "网关 host:port"),
                                            ("port", "Modem AT 端口"),
                                            ("gammu_port", "辅助 AT 端口"),
                                            ("pcsc_reader", "读卡器过滤"))):
            ttk.Label(body, text=label).grid(row=row, column=0, sticky="w", pady=4)
            value = tk.StringVar(value=str(current.get(key, "")))
            ttk.Entry(body, textvariable=value, width=48).grid(row=row, column=1, sticky="ew")
            fields[key] = value
        ttk.Label(body, text="新 Token（留空不更改）").grid(row=4, column=0, sticky="w", pady=4)
        token = tk.StringVar()
        ttk.Entry(body, textvariable=token, show="•", width=48).grid(row=4, column=1, sticky="ew")
        no_pcsc = tk.BooleanVar(value=bool(current.get("no_pcsc")))
        ttk.Checkbutton(body, text="禁用 PC/SC 读卡器管理", variable=no_pcsc).grid(
            row=5, column=1, sticky="w", pady=4)
        body.columnconfigure(1, weight=1)

        def save():
            changes = {key: variable.get() for key, variable in fields.items()}
            changes["no_pcsc"] = no_pcsc.get()
            if token.get():
                changes["token"] = token.get()
            def apply():
                self.client.call("config.validate", {"changes": changes})
                return self.client.call("config.set", {"changes": changes})
            def saved(_result):
                messagebox.showinfo("MDD Agent", "配置已保存；请重启服务使其生效。")
                window.destroy()
            self._background(apply, saved)

        ttk.Button(body, text="验证并保存", command=save).grid(row=6, column=1, sticky="e", pady=10)


def run_gui(*, store=None, client=None, services=None):
    store = store or ConfigStore()
    client = client or ControlClient(store.root)
    services = services or ServiceManager(data_dir=str(store.root))
    if sys.platform == "darwin":
        try:
            from agent_host import AgentHost
            from macos.tray import run_menu_bar
        except ModuleNotFoundError:
            from .agent_host import AgentHost
            from .macos.tray import run_menu_bar
        host = AgentHost(store, host_mode="gui")
        # Take the installation lease before reading config or opening onboarding UI. This
        # gives GUI/CLI mixed starts the same deterministic conflict exit and avoids two
        # onboarding/config writers racing before hardware ownership is settled.
        host.lease.acquire()
        try:
            if not store.load(include_secrets=True).get("token"):
                try:
                    import rumps
                except ImportError as exc:
                    raise RuntimeError(
                        "the bundled macOS menu-bar runtime is unavailable") from exc
                response = rumps.Window(
                    "首次运行需要 Agent Token", "MDD Agent 设置", secure=True,
                    ok="保存并启动", cancel="退出").run()
                if not response.clicked or not response.text:
                    host.release_lease_if_idle()
                    return 3
                store.save({"token": response.text})
            host.start()
        except Exception:
            host.release_lease_if_idle()
            raise
        run_menu_bar(host, client, AgentGui._asset_path("mdd-agent.png"))
        return 0
    root = tk.Tk()
    AgentGui(root, store, client, services)
    root.mainloop()
    return 0
