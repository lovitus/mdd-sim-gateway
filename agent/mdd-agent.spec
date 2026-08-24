# -*- mode: python ; coding: utf-8 -*-

import os
import sys

common_datas = [
    ("../VERSION", "."),
    ("data/serviceproviders.xml", "data"),
    ("windows/install.ps1", "windows"),
    ("assets/mdd-agent.png", "assets"),
    ("assets/mdd-agent.ico", "assets"),
    ("assets/mdd-agent.icns", "assets"),
]
common_hidden = ["health_reporter", "package_manifest"]
if sys.platform == "win32":
    common_hidden = [
        "package_manifest", "pywintypes", "pythoncom", "win32timezone",
        "win32api", "win32con", "win32crypt", "win32event", "win32file", "win32pipe",
        "win32gui", "win32security", "win32service", "win32serviceutil", "servicemanager",
        "windows.service_host", "windows.tray",
    ]
native_binaries = []
codesign_identity = os.environ.get("MDD_PYINSTALLER_CODESIGN_IDENTITY") or None
entitlements_file = "macos/MDD-Agent.entitlements" if sys.platform == "darwin" else None
if sys.platform == "darwin":
    common_hidden += ["AppKit", "AVFoundation", "Foundation", "objc",
                      "PyObjCTools.AppHelper", "rumps", "macos.tray", "macos.window"]
    for environment, name in (("MDD_CELLULAR_IO_BINARY", "mdd-cellular-io"),
                              ("MDD_CALL_AUDIO_BINARY", "mdd-call-audio-helper")):
        source = os.environ.get(environment, "")
        if not source or not os.path.isfile(source):
            raise SystemExit(f"{environment} must name the prebuilt {name} artifact")
        native_binaries.append((source, "."))

a = Analysis(
    ["mdd_agent.py"], pathex=[], binaries=native_binaries, datas=common_datas,
    hiddenimports=common_hidden, hookspath=[], hooksconfig={}, runtime_hooks=[],
    excludes=["tkinter"] if sys.platform == "darwin" else [],
    noarchive=False, optimize=0,
)
pyz = PYZ(a.pure)
exe = EXE(
    pyz, a.scripts, a.binaries, a.datas, [], name="mdd-agent", debug=False,
    bootloader_ignore_signals=False, strip=False, upx=True, upx_exclude=[],
    runtime_tmpdir=None, console=True, disable_windowed_traceback=False,
    argv_emulation=False, target_arch=None, codesign_identity=codesign_identity,
    entitlements_file=entitlements_file,
)
