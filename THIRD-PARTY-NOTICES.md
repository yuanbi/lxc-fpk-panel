# 第三方组件声明 · Third-Party Notices

LXCPanel 本体以 [MIT License](LICENSE) 发布。本文件列出仓库内**实际包含**的第三方代码及其许可证，
以及运行时依赖的系统组件（不随本项目分发）。

## 随仓库 / 随 fpk 分发

### xterm.js 5.3.0 + xterm-addon-fit 0.8.0

- 项目主页：<https://github.com/xtermjs/xterm.js>
- 许可证：MIT
- 位置：`src/www/vendor/xterm.js`、`src/www/vendor/xterm.css`、`src/www/vendor/addon-fit.js`
- 完整许可证文本：`src/www/vendor/LICENSE.xterm`

```
Copyright (c) 2017-2019, The xterm.js authors (https://github.com/xtermjs/xterm.js)
Copyright (c) 2014-2016, SourceLair Private Company (https://www.sourcelair.com)
Copyright (c) 2012-2013, Christopher Jeffrey (https://github.com/chjj/)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

### Simple Icons（发行版图形）

- 项目主页：<https://github.com/simple-icons/simple-icons>
- 版本：13
- 许可证：CC0-1.0（图形本身）
- 位置：SVG path 内联在 `src/www/app.js` 的 `distroMark()` 中
- 说明：Debian、Ubuntu、Alpine、Kali、Rocky、AlmaLinux 等**名称与 LOGO 是其各自所有者的商标**，
  本项目仅用于标识所管理的系统类型，不代表任何形式的官方关联或背书。

## 运行时依赖（宿主机系统组件，不随本项目分发）

| 组件 | 许可证 | 用途 |
| --- | --- | --- |
| [LXC](https://linuxcontainers.org/lxc/) | LGPL-2.1+ | 容器运行时，通过 `lxc-*` 命令调用 |
| [iptables](https://www.netfilter.org/) / ip6tables | GPL-2.0 | 端口转发、NAT、MSS 钳制 |
| [dnsmasq](https://thekelleys.org.uk/dnsmasq/doc.html) | GPL-2.0 / GPL-3.0 | 给容器下发 DHCP 与 DNS |
| [systemd](https://systemd.io/) | LGPL-2.1+ | 服务托管、`lxc-net` |
| `wget` / `tar` / `xz` | GPL-3.0 / GPL-2.0 / 公有领域 | 下载并解包 `lxc-download` 模板 |

以上组件由用户的操作系统提供，本项目仅以命令行方式调用，**不包含其代码，也不构成衍生作品**。

## 容器镜像

创建容器时，`lxc-download` 从 `images.linuxcontainers.org` 或国内镜像站（清华 / 中科大 / 阿里云 / 南大）
下载对应发行版的根文件系统。镜像**不随本仓库或 fpk 分发**，各自遵循其发行版的许可证与服务条款。

## 构建工具（不参与分发）

| 工具 | 许可证 | 用途 |
| --- | --- | --- |
| [Go](https://go.dev/) | BSD-3-Clause | 编译后端 |
| [fnpack](https://developer.fnnas.com/docs/guide/) | 飞牛官方工具 | 打包 `.fpk` |
