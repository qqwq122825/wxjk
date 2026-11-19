#!/usr/bin/env python3
"""Run/build/test the local Go server; bootstrap a checksum-verified local SDK."""
from pathlib import Path
import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def go_binary():
    installed = shutil.which("go")
    if installed:
        return installed
    local = ROOT / ".toolchains/go/bin/go"
    if local.exists():
        return str(local)
    host_os = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[platform.machine()]
    releases = json.load(urllib.request.urlopen("https://go.dev/dl/?mode=json", timeout=30))
    release = next(item for item in releases if item["stable"])
    archive = next(item for item in release["files"] if item["os"] == host_os and item["arch"] == arch and item["kind"] == "archive")
    directory = ROOT / ".toolchains"
    directory.mkdir(exist_ok=True)
    print(f"Downloading {release['version']} for this workspace...", flush=True)
    with tempfile.TemporaryDirectory(dir=directory) as temporary:
        file = Path(temporary) / archive["filename"]
        with urllib.request.urlopen("https://go.dev/dl/" + archive["filename"], timeout=60) as response, file.open("wb") as output:
            shutil.copyfileobj(response, output)
        if hashlib.sha256(file.read_bytes()).hexdigest() != archive["sha256"]:
            raise RuntimeError("Go archive checksum mismatch")
        with tarfile.open(file) as content:
            content.extractall(temporary, filter="data")
        shutil.move(str(Path(temporary) / "go"), str(directory / "go"))
    return str(local)


def environment():
    values = os.environ.copy()
    values["GOPATH"] = str(ROOT / ".cache/gopath")
    values["GOCACHE"] = str(ROOT / ".cache/go-build")
    return values


def build_frontend():
    web = ROOT / "server/web"
    if not (web / "node_modules").exists():
        subprocess.run(["npm", "ci"], cwd=web, check=True)
    index = ROOT / "server/public/index.html"
    inputs = [web / "package-lock.json", web / "vite.config.js", web / "index.html", *list((web / "src").rglob("*"))]
    if not (ROOT / "server/public/assets").exists() or not index.exists() or any(p.is_file() and p.stat().st_mtime > index.stat().st_mtime for p in inputs):
        subprocess.run(["npm", "run", "build"], cwd=web, check=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--prepare", action="store_true")
    parser.add_argument("--test", action="store_true")
    parser.add_argument("--build", action="store_true")
    parser.add_argument("--toolchain", action="store_true")
    args = parser.parse_args()
    go = go_binary()
    if args.toolchain:
        print(go)
    elif args.prepare:
        subprocess.run([go, "get", "github.com/gorilla/websocket@latest", "golang.org/x/crypto@latest",
                        "gorm.io/gorm@latest", "gorm.io/driver/sqlite@latest", "gorm.io/driver/postgres@latest"],
                       cwd=ROOT / "server", env=environment(), check=True)
    elif args.test:
        subprocess.run([go, "test", "-race", "./..."], cwd=ROOT / "server", env=environment(), check=True)
    elif args.build:
        build_frontend()
        destination = ROOT / "build/server/jk-monitor"
        destination.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run([go, "build", "-o", str(destination), "."], cwd=ROOT / "server", env=environment(), check=True)
        print(destination)
    else:
        build_frontend()
        subprocess.run([go, "run", "."], cwd=ROOT / "server", env=environment(), check=True)
