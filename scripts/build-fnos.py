#!/usr/bin/env python3
"""Build the native ARM64 PanSou fnOS package from both local repositories."""

import argparse
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess


ROOT = Path(__file__).resolve().parent.parent
PACKAGE = ROOT / "fnos"
OUTPUT = ROOT / "dist" / "fnos"
PREFIX = "/app/pansou"
PATH_PATTERN = re.compile(r"(['\"])/(api/|designs/|classic\.html|favicon\.svg|icons\.svg)")


def run(*args: str, cwd: Path, env: dict[str, str] | None = None) -> None:
    subprocess.run(args, cwd=cwd, env=env, check=True)


def build_frontend(frontend: Path) -> Path:
    if not (frontend / "package.json").is_file():
        raise SystemExit(f"找不到前端项目: {frontend}")
    env = os.environ.copy()
    env["VITE_PANSOU_BASE_URL"] = PREFIX
    run("npm", "run", "build", "--", f"--base={PREFIX}/", cwd=frontend, env=env)
    return frontend / "dist"


def prepare_frontend(source: Path, destination: Path) -> None:
    shutil.copytree(source, destination)
    for file in destination.rglob("*"):
        if file.suffix not in {".html", ".js"} or not file.is_file():
            continue
        content = file.read_text(encoding="utf-8")
        content = PATH_PATTERN.sub(lambda match: match.group(1) + PREFIX + "/" + match.group(2), content)
        content = content.replace('href="/"', f'href="{PREFIX}/"')
        if PATH_PATTERN.search(content):
            raise SystemExit(f"还有未加网关前缀的站内路径: {file}")
        file.write_text(content, encoding="utf-8")


def write_defaults(destination: Path) -> None:
    compose = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
    lines = ["# Generated from docker-compose.yml; proxy settings stay device-local."]
    for key in ("CHANNELS", "ENABLED_PLUGINS"):
        found = re.search(rf"^\s*-\s*{key}=([^\n]+)$", compose, re.MULTILINE)
        if not found:
            raise SystemExit(f"docker-compose.yml 中缺少 {key}")
        value = found.group(1).strip()
        if not re.fullmatch(r"[A-Za-z0-9_,.-]+", value):
            raise SystemExit(f"{key} 含不支持的字符")
        lines.append(f"{key}={value}")
    lines.append("CACHE_ENABLED=true")
    destination.write_text("\n".join(lines) + "\n", encoding="utf-8")


def build_backend(destination: Path) -> None:
    env = os.environ.copy()
    env.update({"GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0"})
    env.setdefault("GOTOOLCHAIN", "go1.24.12")
    env.setdefault("GOCACHE", str(OUTPUT / "go-cache"))
    run("go", "build", "-trimpath", "-ldflags=-s -w", "-o", str(destination), ".", cwd=ROOT, env=env)
    data = destination.read_bytes()[:20]
    if data[:4] != b"\x7fELF" or struct.unpack("<H", data[18:20])[0] != 183:
        raise SystemExit("Go 构建结果不是 Linux ARM64 ELF")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--frontend", type=Path, default=ROOT.parent / "pansou-web")
    parser.add_argument("--fnpack", default=shutil.which("fnpack"))
    args = parser.parse_args()
    if not args.fnpack:
        parser.error("请安装官方 fnpack，或用 --fnpack 指定工具路径")

    OUTPUT.mkdir(parents=True, exist_ok=True)
    staging = OUTPUT / "staging" / "pansou"
    if staging.exists():
        shutil.rmtree(staging)
    shutil.copytree(PACKAGE, staging)
    shutil.copy2(ROOT / "LICENSE", staging / "LICENSE")
    (staging / "wizard").mkdir()
    app = staging / "app"
    build_backend(app / "pansou")
    write_defaults(app / "defaults.env")
    prepare_frontend(build_frontend(args.frontend.resolve()), app / "web")
    built = OUTPUT / "pansou.fpk"
    built.unlink(missing_ok=True)
    run(args.fnpack, "build", "--directory", str(staging), cwd=OUTPUT)

    if not built.is_file():
        raise SystemExit("fnpack 未生成 FPK")
    manifest = (staging / "manifest").read_text(encoding="utf-8")
    version = re.search(r"^version\s*=\s*([0-9A-Za-z._-]+)$", manifest, re.MULTILINE)
    if not version:
        raise SystemExit("manifest 中缺少合法的 version")
    artifact = OUTPUT / f"pansou-{version.group(1)}-arm.fpk"
    shutil.copy2(built, artifact)
    print(f"FPK: {artifact}")


if __name__ == "__main__":
    main()
