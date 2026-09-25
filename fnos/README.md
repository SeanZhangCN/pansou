# PanSou fnOS 原生应用

此包将当前 Go API 和 `../pansou-web` 的三个设计页面一起打包为 ARM64 FPK。应用通过 fnOS 统一网关提供 `/app/pansou` 入口，不需要在 NAS 上安装 Docker 或 Node.js。要求 fnOS 1.1.3100 或更新版本；目标设备的 `uname -m` 应为 `aarch64`。

## 构建

开发机需要 Go 1.24、Node.js/npm 和官方 `fnpack` 1.2.3。在后端仓库执行：

```sh
python3 scripts/build-fnos.py --fnpack /path/to/fnpack
```

脚本会从同级 `pansou-web` 构建前端、交叉编译 Linux ARM64 Go 程序，并生成 `dist/fnos/pansou-<版本>-arm.fpk`。频道和插件默认名单取自本仓库 `docker-compose.yml`；Docker 专用的 `host.docker.internal` 代理地址不会复制到 NAS。

## 安装

在 fnOS 应用中心使用“手动安装”上传 FPK。应用中心或桌面点击 PanSou 后，会在浏览器页面打开 `/app/pansou`，而不是桌面内嵌窗口。也可以将文件复制到设备后，通过 SSH 执行：

```sh
appcenter-cli install-fpk pansou-0.1.1-arm.fpk
```

安装后从应用中心打开 PanSou。若设备访问 Telegram 等来源需要代理，可在应用配置目录的 `pansou.env` 写入 `PROXY=http://设备可访问的代理地址:端口`，再重启应用。运行日志位于应用 `var/pansou.log`。前端选择和搜索设置保存在浏览器本地存储中。
