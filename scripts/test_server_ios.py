#!/usr/bin/env python3
"""Connect the real iOS dylib to the Go server over trusted local HTTPS/WSS."""
from pathlib import Path
import hashlib
import http.cookiejar
import json
import os
import platform
import plistlib
import shutil
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.request
from build_ios import ROOT, SOURCE, build


def run(*args, **kwargs):
    return subprocess.run(list(map(str, args)), check=True, **kwargs)


def wait(label, operation, timeout=28):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = operation()
        if value:
            return value
        time.sleep(0.25)
    raise RuntimeError("Timed out: " + label)


class Browser:
    def __init__(self, URL, certificate):
        self.URL, self.csrf = URL, ""
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(certificate))))

    def api(self, method, path, body=None, binary=False):
        request = urllib.request.Request(self.URL + path, data=json.dumps(body).encode() if body is not None else None,
                                         method=method, headers={"Content-Type": "application/json", "X-CSRF-Token": self.csrf})
        with self.opener.open(request, timeout=4) as response:
            raw = response.read()
        return raw if binary else json.loads(raw)


def main():
    output = ROOT / "build/tests"
    output.mkdir(parents=True, exist_ok=True)
    library = build("iphonesimulator")
    app = output / "JKServerIntegration.app"
    (app / "Frameworks").mkdir(parents=True, exist_ok=True)
    shutil.copy2(library, app / "Frameworks/jk.dylib")
    info = {
        "CFBundleExecutable": "JKServerIntegration", "CFBundleIdentifier": "com.jk.serverintegration",
        "CFBundleName": "JKServerIntegration", "CFBundlePackageType": "APPL", "CFBundleDevelopmentRegion": "zh_CN",
        "CFBundleVersion": "1", "CFBundleShortVersionString": "1.0", "MinimumOSVersion": "15.6",
        "LSRequiresIPhoneOS": True, "UIDeviceFamily": [1, 2], "UILaunchScreen": {},
        "JKMonitor": {"prompt_for_server": False},
        "UIApplicationSceneManifest": {"UIApplicationSupportsMultipleScenes": False,
            "UISceneConfigurations": {"UIWindowSceneSessionRoleApplication": [{
                "UISceneConfigurationName": "Default", "UISceneDelegateClassName": "HostSceneDelegate"}]}},
    }
    (app / "Info.plist").write_bytes(plistlib.dumps(info))
    entitlement = output / "server-simulated-entitlements.plist"
    entitlement.write_bytes(plistlib.dumps({"application-identifier": "JKTEST0000.com.jk.serverintegration",
                                           "keychain-access-groups": ["JKTEST0000.com.jk.serverintegration"]}))
    sdk = subprocess.check_output(["xcrun", "--sdk", "iphonesimulator", "--show-sdk-path"], text=True).strip()
    arch = "arm64" if platform.machine() == "arm64" else "x86_64"
    run("xcrun", "--sdk", "iphonesimulator", "clang", "-fobjc-arc", "-fblocks", "-Wall", "-Wextra", "-Werror",
        "-Wno-unused-parameter", "-target", f"{arch}-apple-ios15.6-simulator", "-isysroot", sdk,
        "-I", SOURCE, ROOT / "ios/Tests/SimulatorHost.m", "-framework", "UIKit", "-framework", "Foundation",
        "-framework", "QuartzCore", library, "-Wl,-rpath,@executable_path/Frameworks",
        "-Wl,-sectcreate,__TEXT,__entitlements," + str(entitlement), "-o", app / "JKServerIntegration")
    run("codesign", "--force", "--sign", "-", app / "Frameworks/jk.dylib")
    run("codesign", "--force", "--sign", "-", app)
    certificate, key = output / "server-localhost.crt", output / "server-localhost.key"
    config = output / "server-localhost.cnf"
    config.write_text("""[req]
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
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "1", "-config", config,
        "-keyout", key, "-out", certificate, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    URL = f"https://localhost:{port}"
    data = tempfile.TemporaryDirectory(prefix="go-ios-data-", dir=output)
    environment = os.environ.copy()
    environment.update({"LISTEN_ADDR": f"127.0.0.1:{port}", "DATA_DIR": data.name, "TLS_CERT": str(certificate),
                        "TLS_KEY": str(key), "SETUP_TOKEN": "local-server-test", "ENROLLMENT_TOKEN": ""})
    server_log = (output / "go-ios-server.log").open("w")
    server = subprocess.Popen([str(ROOT / "build/server/jk-monitor")], env=environment, stdout=server_log, stderr=server_log)
    device = None
    native = None
    native_log = (output / "go-ios-native.log").open("w")
    try:
        browser = Browser(URL, certificate)
        def healthy():
            try:
                return browser.api("GET", "/api/health")
            except OSError:
                return None
        wait("Go server startup", healthy, 10)
        auth = browser.api("POST", "/api/v1/auth/setup", {"username": "owner", "password": "OwnerPassword123!", "setup_token": "local-server-test"})
        browser.csrf = auth["csrf_token"]
        agent = browser.api("POST", "/api/v1/admin/agents", {"username": "iosagent", "display_name": "iOS 联调代理", "password": "InitialPass123!"})
        runtimes = json.loads(subprocess.check_output(["xcrun", "simctl", "list", "runtimes", "--json"], text=True))
        runtime = next(r["identifier"] for r in runtimes["runtimes"] if r["isAvailable"] and "iOS-18-1" in r["identifier"])
        device = subprocess.check_output(["xcrun", "simctl", "create", "JK Go Server Integration", "com.apple.CoreSimulator.SimDeviceType.iPhone-16", runtime], text=True).strip()
        run("xcrun", "simctl", "boot", device)
        run("xcrun", "simctl", "bootstatus", device, "-b")
        run("xcrun", "simctl", "keychain", device, "add-root-cert", certificate)
        run("xcrun", "simctl", "install", device, app)
        native = subprocess.Popen(["xcrun", "simctl", "launch", "--console", device, "com.jk.serverintegration",
                                   "--network-url", URL, "--real-server"], stdout=native_log, stderr=native_log)
        d = wait("native device registration", lambda: (browser.api("GET", "/api/v1/devices")["items"] or [None])[0])
        assert not d["agent_id"], "new native device bypassed the pending pool"
        browser.api("POST", f"/api/v1/admin/devices/{d['id']}/assignment", {"agent_id": agent["id"]})
        browser.api("POST", f"/api/v1/devices/{d['id']}/commands", {"type": "GET_IMAGE"})
        def detail():
            return browser.api("GET", f"/api/v1/devices/{d['id']}")
        image = wait("native single PNG upload", lambda: detail().get("latest_image"))
        picture = browser.api("GET", f"/api/v1/images/{image['id']}/file", binary=True)
        assert picture.startswith(b"\x89PNG\r\n\x1a\n")
        (output / "server-ios-window.png").write_bytes(picture)
        browser.api("POST", f"/api/v1/devices/{d['id']}/commands", {"type": "START_IMAGE_SESSION"})
        wait("native autonomous task", lambda: detail().get("session", {}).get("last_frame_seq", 0) >= 2)
        old_image = detail()["latest_image"]["id"]
        browser.api("POST", "/api/v1/auth/logout")
        browser = Browser(URL, certificate)
        auth = browser.api("POST", "/api/v1/auth/login", {"username": "owner", "password": "OwnerPassword123!"})
        browser.csrf = auth["csrf_token"]
        wait("capture continues after web logout", lambda: detail()["latest_image"]["id"] != old_image, 10)
        browser.api("POST", f"/api/v1/devices/{d['id']}/commands", {"type": "STOP_IMAGE_SESSION"})
        wait("native stop acknowledgement", lambda: not detail()["active_session_id"], 10)
        native.wait(timeout=10)
        container = Path(subprocess.check_output(["xcrun", "simctl", "get_app_container", device, "com.jk.serverintegration", "data"], text=True).strip())
        report = json.loads((container / "Documents/server-integration.json").read_text())
        assert report["passed"], report
        report["go_server_flow"] = ["register", "assign", "single_png", "autonomous_task", "web_logout_keeps_task", "stop_ack"]
        report["png_sha256"] = hashlib.sha256(picture).hexdigest()
        (output / "server-ios-integration.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        print("PASS: real iOS dylib → Go HTTPS/WSS → account assignment → PNG → autonomous task → web logout → stop")
    finally:
        if native and native.poll() is None:
            native.terminate()
            native.wait(timeout=5)
        if device:
            subprocess.run(["xcrun", "simctl", "shutdown", device], capture_output=True)
            subprocess.run(["xcrun", "simctl", "delete", device], capture_output=True)
        server.terminate()
        server.wait(timeout=5)
        native_log.close()
        server_log.close()
        data.cleanup()


if __name__ == "__main__":
    main()
