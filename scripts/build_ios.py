#!/usr/bin/env python3
"""Build host-embeddable dylibs without MonkeyDev installation or packaging."""
from pathlib import Path
import argparse
import platform
import subprocess

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "ios/jk/jk"


def build(sdk: str) -> Path:
    sdk_path = subprocess.check_output(
        ["xcrun", "--sdk", sdk, "--show-sdk-path"], text=True
    ).strip()
    simulator = sdk == "iphonesimulator"
    arch = "arm64" if not simulator or platform.machine() == "arm64" else "x86_64"
    target = f"{arch}-apple-ios15.6" + ("-simulator" if simulator else "")
    output = ROOT / "build/ios" / ("simulator" if simulator else "device")
    output.mkdir(parents=True, exist_ok=True)
    library = output / "jk.dylib"
    subprocess.run([
        "xcrun", "--sdk", sdk, "clang++", "-std=c++17", "-fobjc-arc", "-fblocks",
        "-Wall", "-Wextra", "-Werror", "-Wno-unused-parameter", "-O2", "-g",
        "-target", target, "-isysroot", sdk_path, "-dynamiclib",
        "-install_name", "@rpath/jk.dylib",
        str(SOURCE / "jk.mm"), str(SOURCE / "JKWindowCapture.mm"), str(SOURCE / "JKDeviceClient.mm"),
        "-framework", "Foundation", "-framework", "UIKit", "-framework", "ImageIO",
        "-framework", "CoreGraphics",
        "-framework", "Network", "-framework", "Security",
        "-o", str(library),
    ], check=True)
    (output / "JKWindowCapture.h").write_bytes((SOURCE / "JKWindowCapture.h").read_bytes())
    (output / "JKDeviceClient.h").write_bytes((SOURCE / "JKDeviceClient.h").read_bytes())
    (output / "JKDebugLog.h").write_bytes((SOURCE / "JKDebugLog.h").read_bytes())
    if simulator:
        subprocess.run(["codesign", "--force", "--sign", "-", str(library)], check=True)
    print(library)
    return library


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk", choices=["all", "iphoneos", "iphonesimulator"], default="all")
    args = parser.parse_args()
    for name in (["iphoneos", "iphonesimulator"] if args.sdk == "all" else [args.sdk]):
        build(name)
