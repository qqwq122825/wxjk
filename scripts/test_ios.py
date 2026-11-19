#!/usr/bin/env python3
"""Run policy tests and a real UIKit host app using a dedicated iOS simulator."""
from pathlib import Path
import json
import platform
import plistlib
import shutil
import subprocess
import sys
from build_ios import ROOT, SOURCE, build
from mock_device_server import MockDeviceServer


def run(*args, **kwargs):
    return subprocess.run(list(map(str, args)), check=True, **kwargs)


def main():
    output = ROOT / "build/tests"
    output.mkdir(parents=True, exist_ok=True)
    executable = output / "capture-policy-tests"
    run("clang++", "-std=c++17", "-Wall", "-Wextra", "-Werror", "-O2", "-I", SOURCE,
        ROOT / "ios/Tests/CapturePolicyTests.cpp", "-o", executable)
    run(executable)
    library = build("iphonesimulator")
    sdk = subprocess.check_output(["xcrun", "--sdk", "iphonesimulator", "--show-sdk-path"], text=True).strip()
    arch = "arm64" if platform.machine() == "arm64" else "x86_64"
    app = output / "JKCaptureTest.app"
    frameworks = app / "Frameworks"
    frameworks.mkdir(parents=True, exist_ok=True)
    shutil.copy2(library, frameworks / "jk.dylib")
    info = {
        "CFBundleExecutable": "JKCaptureTest", "CFBundleIdentifier": "com.jk.capturetest",
        "CFBundleName": "JKCaptureTest", "CFBundlePackageType": "APPL",
        "CFBundleVersion": "1", "CFBundleShortVersionString": "1.0", "MinimumOSVersion": "15.6",
        "LSRequiresIPhoneOS": True, "UIDeviceFamily": [1, 2],
        "JKMonitor": {"prompt_for_server": False},
        "UILaunchScreen": {},
        "UIApplicationSceneManifest": {
            "UIApplicationSupportsMultipleScenes": False,
            "UISceneConfigurations": {"UIWindowSceneSessionRoleApplication": [{
                "UISceneConfigurationName": "Default", "UISceneDelegateClassName": "HostSceneDelegate"
            }]},
        },
    }
    (app / "Info.plist").write_bytes(plistlib.dumps(info))
    simulated_entitlements = output / "simulated-entitlements.plist"
    simulated_entitlements.write_bytes(plistlib.dumps({
        "application-identifier": "JKTEST0000.com.jk.capturetest",
        "keychain-access-groups": ["JKTEST0000.com.jk.capturetest"],
    }))
    run("xcrun", "--sdk", "iphonesimulator", "clang", "-fobjc-arc", "-fblocks", "-Wall", "-Wextra",
        "-Werror", "-Wno-unused-parameter", "-target", f"{arch}-apple-ios15.6-simulator", "-isysroot", sdk,
        "-I", SOURCE, ROOT / "ios/Tests/SimulatorHost.m", "-framework", "UIKit", "-framework", "Foundation",
        "-Wl,-sectcreate,__TEXT,__entitlements," + str(simulated_entitlements),
        "-framework", "QuartzCore", library, "-Wl,-rpath,@executable_path/Frameworks",
        "-o", app / "JKCaptureTest")
    run("codesign", "--force", "--sign", "-", frameworks / "jk.dylib")
    run("codesign", "--force", "--sign", "-", app)
    certificate = output / "localhost.crt"
    key = output / "localhost.key"
    certificate_config = output / "localhost.cnf"
    certificate_config.write_text("""[req]
distinguished_name=dn
x509_extensions=extensions
prompt=no
[dn]
CN=localhost
[extensions]
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:localhost,IP:127.0.0.1
""")
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "1",
        "-config", certificate_config, "-keyout", key, "-out", certificate,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    peer = MockDeviceServer(certificate, key)
    runtimes = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "runtimes", "--json"], text=True))
    runtime = next(r["identifier"] for r in runtimes["runtimes"] if r["isAvailable"] and "iOS-18-1" in r["identifier"])
    device = subprocess.check_output([
        "xcrun", "simctl", "create", "JK Capture Tests", "com.apple.CoreSimulator.SimDeviceType.iPhone-16", runtime
    ], text=True).strip()
    try:
        run("xcrun", "simctl", "boot", device)
        run("xcrun", "simctl", "bootstatus", device, "-b")
        run("xcrun", "simctl", "keychain", device, "add-root-cert", certificate)
        run("xcrun", "simctl", "install", device, app)
        phases = [("phase3", ["--network-url", peer.URL]),
                  ("phase4", ["--network-url", peer.URL + "/unauthorized", "--reject-auth"]),
                  ("phase5", ["--network-url", peer.URL + "/unauthorized", "--prompt-test"]),
                  ("phase6", ["--verify-saved-address"])]
        if "--network-only" not in sys.argv:
            phases = [("phase1", []), ("phase2", ["--verify-restored"])] + phases
        for phase, args in phases:
            run("xcrun", "simctl", "launch", "--terminate-running-process", "--console", device,
                "com.jk.capturetest", *args, timeout=55)
            container = Path(subprocess.check_output([
                "xcrun", "simctl", "get_app_container", device, "com.jk.capturetest", "data"
            ], text=True).strip())
            report = json.loads((container / "Documents" / f"{phase}.json").read_text())
            (output / f"{phase}.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
            if not report["passed"]:
                print(f"Mock server: registrations={peer.registrations}, WSS={peer.upgrades}, uploads={len(peer.uploads)}")
                raise RuntimeError(report)
            print(f"{phase}: PASS ({report['frames']} frames)")
        shutil.copy2(container / "Documents/active-window.png", output / "active-window.png")
        assert peer.upgrades >= 3, "WSS failures did not cause reconnection"
        assert len(peer.uploads) >= 3, "missing committed PNG uploads"
        assert len({item["report_id"] for item in peer.uploads}) == len(peer.uploads), "duplicate report identity"
        assert len({item["frame_seq"] for item in peer.uploads}) == len(peer.uploads), "frame sequence restarted on reconnect"
        assert not peer.failures, peer.failures
        print(f"TLS/WSS: {peer.upgrades} connection attempts, {len(peer.uploads)} PNG uploads verified")
        print(f"Captured test window: {output / 'active-window.png'}")
    finally:
        subprocess.run(["xcrun", "simctl", "shutdown", device], capture_output=True)
        subprocess.run(["xcrun", "simctl", "delete", device], capture_output=True)
        peer.close()


if __name__ == "__main__":
    main()
