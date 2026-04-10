# -*- mode: python ; coding: utf-8 -*-
# PyInstaller spec for ravenlink
# Build with: pyinstaller build.spec

from PyInstaller.utils.hooks import collect_dynamic_libs, collect_data_files

# Collect native DLLs from robotpy/wpilib packages
binaries = []
datas = []
for pkg in ['ntcore', 'wpiutil', 'wpinet', 'wpimath',
            'robotpy_wpiutil', 'robotpy_wpinet',
            'pyntcore', 'robotpy_native_wpiutil',
            'robotpy_native_wpinet', 'robotpy_native_ntcore']:
    try:
        binaries += collect_dynamic_libs(pkg)
        datas += collect_data_files(pkg)
    except Exception:
        pass

a = Analysis(
    ['ravenlink.py'],
    pathex=[],
    binaries=binaries,
    datas=datas,
    hiddenimports=[
        'ntcore', 'ntcore._ntcore',
        'wpiutil', 'wpiutil._wpiutil',
        'wpinet',
        'obsws_python', 'flask', 'pystray', 'PIL',
        'robotpy_wpiutil', 'robotpy_wpinet', 'robotpy_native_wpiutil',
        'robotpy_native_wpinet', 'robotpy_native_ntcore',
        'pyntcore',
    ],
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
)

pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.datas,
    [],
    name='ravenlink',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    upx_exclude=[],
    runtime_tmpdir=None,
    console=True,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
