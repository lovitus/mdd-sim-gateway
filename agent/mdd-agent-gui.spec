# -*- mode: python ; coding: utf-8 -*-

import os
import sys

native_binaries = []
mac_hidden = []
platform_hidden = ["health_reporter"]
if sys.platform == "win32":
    platform_hidden = [
        "pywintypes", "pythoncom", "win32timezone",
        "win32api", "win32con", "win32file", "win32gui", "win32pipe",
        "win32security", "windows.tray",
    ]
if sys.platform == "darwin":
    mac_hidden = ["AppKit", "AVFoundation", "Foundation", "objc", "PyObjCTools.AppHelper",
                  "rumps", "macos.tray", "macos.window"]
    for environment, name in (("MDD_CELLULAR_IO_BINARY", "mdd-cellular-io"),
                              ("MDD_CALL_AUDIO_BINARY", "mdd-call-audio-helper")):
        source = os.environ.get(environment, "")
        if not source or not os.path.isfile(source):
            raise SystemExit(f"{environment} must name the prebuilt {name} artifact")
        native_binaries.append((source, "."))

a = Analysis(
    ["mdd_agent_gui.py"], pathex=[], binaries=native_binaries,
    datas=[("../VERSION", "."),
           ("data/serviceproviders.xml", "data"),
           ("assets/mdd-agent.png", "assets"),
           ("assets/mdd-agent.ico", "assets"),
           ("assets/mdd-agent.icns", "assets")],
    hiddenimports=platform_hidden + mac_hidden,
    hookspath=[], hooksconfig={}, runtime_hooks=[],
    excludes=["tkinter"] if sys.platform == "darwin" else [],
    noarchive=False, optimize=0,
)
pyz = PYZ(a.pure)
if sys.platform == "darwin":
    exe = EXE(
        pyz, a.scripts, [], exclude_binaries=True,
        name="mdd-agent-gui", debug=False, bootloader_ignore_signals=False,
        strip=False, upx=True, console=False, argv_emulation=False,
        target_arch=None, codesign_identity=None, entitlements_file=None,
    )
    collection = COLLECT(
        exe, a.binaries, a.datas, strip=False, upx=True,
        upx_exclude=[], name="mdd-agent-gui")
    app = BUNDLE(
        collection,
        name="MDD Agent.app",
        icon="assets/mdd-agent.icns",
        bundle_identifier="com.mdd.agent",
        info_plist={
            "LSUIElement": True,
            "NSHighResolutionCapable": True,
            "NSMicrophoneUsageDescription":
                "MDD Agent uses the cellular module audio endpoint for browser calls.",
        },
    )
else:
    exe = EXE(
        pyz, a.scripts, a.binaries, a.datas, [], name="mdd-agent-gui", debug=False,
        bootloader_ignore_signals=False, strip=False, upx=True, upx_exclude=[],
        runtime_tmpdir=None, console=False, disable_windowed_traceback=False,
        argv_emulation=False, target_arch=None, codesign_identity=None,
        entitlements_file=None, icon="assets/mdd-agent.ico",
    )
