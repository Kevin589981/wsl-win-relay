# wsl-win-relay

[English](README.md)

当 WSL 无法联网、但 Windows 仍然可以联网时，使用 Windows 的网络能力为
WSL 提供应急网络代理。

WSL 程序只需要连接本地 SOCKS5 或 HTTP 代理，真正的 TCP/UDP 出站连接由
Windows 进程完成，也可以继续经过 Windows 上配置的上游代理。因此 WSL
不需要直接访问 Windows 上游代理端口，适用于 HNS、镜像网络、Windows
热点或 Windows 本地环回异常的情况。

项目还支持把 WSL 服务暴露到 Windows 端口、同步协调严格的
`listen()`/`bind()`，以及通过 TUN 为不支持代理的程序提供透明网络。

## 选择使用模式

| 需求 | 推荐模式 |
| --- | --- |
| 快速试用，不使用 systemd | [手动 stdio 模式](#快速开始手动-stdio) |
| 长期运行并自动恢复 | [持久 broker 模式](#推荐持久-broker) |
| Windows 拒绝端口时让 WSL `listen()` 也失败 | [strict 模式](#严格同步-listenbind) |
| 程序完全不支持代理 | [透明 TUN 模式](#透明-tun-模式) |

## 工作原理

```text
WSL 程序
   |
127.0.0.1:1080 (SOCKS5) 或 127.0.0.1:8080 (HTTP)
   |
WSL relay client
   |
stdio 子进程或 broker connector
   |
Windows relay / socket owner
   |
Windows WinSock 或 Windows 上游代理
```

出站请求不需要端口对应。WSL 只发送目标地址，Windows 正常创建出站
socket 并选择临时源端口。入站暴露则不同：Windows 先监听端口，再为每个
Windows 客户端连接 WSL 服务。

## 模式与数据流程

可以把 relay 看成三层，三层可以组合使用：

```text
应用接入层：      SOCKS5 / HTTP / TUN
跨 WSL-Windows：  stdio / broker + connector
端口发布层：      reverse / auto-forward / strict
```

### 手动 stdio 模式

```text
WSL wsl-proxy-linux
        |
        | 启动 Windows .exe，并通过标准输入输出传输 relay 协议
        v
Windows wsl-win-relay.exe
        |
        v
Windows WinSock 或 Windows 上游代理
```

WSL proxy 通过 interop 启动 Windows relay。应用请求先进入 WSL 本地
SOCKS5/HTTP 监听，WSL relay 只把目标地址和数据传给 Windows relay，真正的
socket 由 Windows 创建。该模式不需要 systemd，适合快速测试；Windows
relay 子进程退出时，已有连接会中断。

### 持久 broker 模式

```text
WSL wsl-proxy-linux
        |
        v
WSL wsl-win-connector.exe
        |  本地 IPC + attach token
        v
Windows broker / socket owner
        |
        v
Windows WinSock 或 127.0.0.1:7890 上游代理
```

broker 是独立的 Windows 常驻进程，connector 可以在 WSL proxy 或连接进程
重启后重新 attach。Windows socket 由 broker 的 socket owner 持有，因此普通
connector/frontend/bridge 重启时已有连接有机会继续使用。broker 或 socket
owner 自身崩溃时，已有连接仍可能丢失。长期运行时推荐此模式；broker 模式的
上游代理配置在 Windows broker 的私有环境中，WSL 不直接连接该地址。

broker supervisor 还会持有一个独立的单实例选举 endpoint。WSL 关闭后旧的
Windows broker 仍在运行时，新的 supervisor 会先复用/等待现有 frontend；如果
旧实例最终消失，再由等待中的 supervisor 接管。这样可以避免两个 broker 同时
抢占同名 endpoint，导致 `Access is denied`、反复重启或新 connector 无法工作。

### SOCKS5 出站模式

```text
应用 -> WSL 127.0.0.1:1080 -> relay -> Windows -> 上游代理/互联网
```

应用通过 SOCKS5 告诉 WSL relay“请连接 `host:port`”。WSL 不建立目标连接，
Windows 创建正常的出站 socket，并自行选择临时源端口。因此出站请求不需要
WSL 端口和 Windows 端口对应。

`socks5h` 会把域名解析交给代理端，适合 WSL DNS/HNS 已经异常的情况；
`socks5` 可能先由 WSL 本地解析。SOCKS5 还支持 UDP ASSOCIATE，但能否真正
出站取决于 Windows 上游代理是否支持 UDP。

### HTTP 代理模式

```text
应用 -> WSL 127.0.0.1:8080 -> HTTP CONNECT/普通 HTTP -> relay -> Windows
```

HTTPS 通常通过 HTTP `CONNECT host:443` 建立隧道，普通 HTTP 请求可以直接
转发。该接口用于只支持 HTTP 代理的程序，主要承载 TCP，不提供 SOCKS5 的
UDP 能力。

### 透明 TUN 模式

```text
不支持代理的应用
        -> Linux 路由表 -> tun0 -> tun2socks
        -> WSL SOCKS5 -> Windows broker -> Windows 上游代理
```

脚本创建 `tun0`，把默认 IPv4 流量拆成 `0.0.0.0/1` 和 `128.0.0.0/1`
两条路由交给 TUN。tun2socks 将 IP 流量转换为 SOCKS5 请求，再进入本地
relay。脚本会保留到本地 SOCKS 端口的路径，避免 relay 自己再次被路由进
TUN。此模式需要 root、`iproute2`、`/dev/net/tun` 和 tun2socks，只影响当前
WSL 实例，不会接管 Windows 本机流量。

TUN 主要接管 IP 流量；应用在建立连接前进行的 DNS 解析可能仍然依赖 WSL
DNS。DNS 已损坏时，优先使用 `socks5h`，或在启动脚本时配置 `WWR_DNS`。

### 显式 reverse 反向映射

```text
Windows 客户端 -> Windows 固定监听端口
               -> relay -> WSL 服务端口
```

例如 `127.0.0.1:8000=127.0.0.1:8000` 表示左侧由 Windows 监听，右侧是
WSL 目标。Windows 必须先成功 `bind()`，之后每个 Windows 客户端连接才会
被转发到 WSL 服务。这是入站发布，与出站代理是两个独立方向。

### auto-forward 自动映射

```text
WSL 程序 listen(8000)
        -> relay 轮询发现
        -> Windows 尝试监听 8000
        -> 建立 Windows -> WSL 转发
```

自动映射默认通过轮询发现 TCP 监听，WSL 的 `listen()` 成功后才尝试 Windows
映射。因此它是“监听后发现”，存在时间差；Windows 后续拒绝不会让已经成功
返回的 WSL `listen()` 事后失败。端口冲突时可以使用端口偏移或让 Windows
自动分配。UDP 自动发现默认关闭，只对明确的白名单端口启用。

### strict 严格同步模式

```text
程序准备 listen(8000)
        -> strict adapter 先请求 Windows 预留
             | 成功                 | 失败
             v                      v
        WSL listen 成功       WSL 返回 EADDRINUSE
```

strict 通过动态库拦截或 kernel adapter，在 WSL 程序的 `listen()`/非零 UDP
`bind()` 返回前，先请求 Windows 预留相同端口。Windows 拒绝时 WSL 调用也
失败；WSL 调用失败时 Windows 预留会取消。这正是“Windows 拒绝，WSL 也拒绝”
的同步语义，但它只协调端口发布，不负责普通出站联网。

### Windows Task Scheduler（可选）

Task Scheduler 不是新的转发协议，而是 broker 的生命周期方式。普通安装由
WSL 用户服务启动 broker；Task Scheduler 可以在 Windows 登录或启动时启动
broker，使其在 WSL 用户服务暂时停止时继续等待 connector 重新连接。

### 模式组合

长期使用通常是：

```text
持久 broker
  + SOCKS5/HTTP（代理感知应用）
  + TUN（不支持代理的应用）
  + auto-forward（普通开发服务暴露）
  + strict（必须同步保证端口一致）
```

例如 `curl --proxy socks5h://127.0.0.1:1080 https://example.com` 使用的是
“持久 broker + SOCKS5 + Windows 上游代理”。直接执行 `curl https://example.com`
只有在设置了代理环境变量或 TUN 正在运行时，才会自动进入 relay；否则仍会
尝试走 WSL 自己的网络路径。

## 前置条件和限制

- Windows 已安装 WSL，并且 WSL interop 可运行 Windows `.exe`。
- amd64 WSL 是当前运行验证目标；arm64 只提供构建支持。
- 从源码构建需要 Go 1.22+ 和 GCC；下载 Release 不需要 Go。
- 使用 systemd 安装器需要 WSL systemd。
- 透明 TUN 需要 root、`iproute2`、`/dev/net/tun` 和 tun2socks。

本项目不会修复 HNS。即使网络路径坏了，WSL 仍必须能启动 Windows 程序，
Windows 也必须能访问互联网或指定的上游代理。

自动端口发现采用轮询方式：它会在 WSL 程序已经成功执行
`listen()` 后创建 Windows 映射，不能让之前已经成功的 `listen()` 事后失败。
需要同步返回 Windows 拒绝时请使用 strict 模式。

默认 SOCKS5/HTTP 监听只绑定 WSL 本地回环，且没有用户认证。不要直接暴露
到局域网。

## 下载 Release

普通用户不需要安装 Go。打开
[Releases](https://github.com/Kevin589981/wsl-win-relay/releases)，根据架构
下载两个压缩包：

- `wsl-win-relay-<版本>-windows-amd64.zip` 或 `windows-arm64.zip`：Windows
  可执行文件；
- `wsl-win-relay-<版本>-linux-amd64.tar.gz` 或 `linux-arm64.tar.gz`：WSL
  可执行文件。

把 Windows 压缩包解压到例如 `C:\Tools\wsl-win-relay`，把 Linux 压缩包解压
到 WSL 中的例如 `$HOME/wsl-win-relay`。Linux 压缩包根目录直接包含 `bin/` 和
`lib/`，解压后即可使用 `$HOME/wsl-win-relay/bin/wsl-proxy-linux`。amd64 包还
包含 `bin/` 下的 native strict supervisor 和 `lib/` 下的 LD_PRELOAD 库。这些
目录只是示例，请替换成你自己的路径。压缩包包含中英文 README、Apache 许可证
和示例配置。

建议使用同一 Release 中的 `SHA256SUMS` 校验下载文件。

## 快速开始：手动 stdio

此模式不需要 systemd，由 WSL proxy 启动一个 Windows relay 子进程。

### 1. 设置路径

```bash
export RELAY_ROOT="$HOME/wsl-win-relay"
export WINDOWS_RELAY_EXE="/mnt/c/Tools/wsl-win-relay/wsl-win-relay.exe"
```

如果是源码仓库，在 `$RELAY_ROOT` 执行 `./scripts/build-wsl.sh`；如果是下载的
Release，跳过构建，因为压缩包已经包含可执行文件和服务脚本。

### 2. 创建配置

```bash
cp "$RELAY_ROOT/wsl-win-relay.example.json" "$HOME/wsl-win-relay.json"
${EDITOR:-nano} "$HOME/wsl-win-relay.json"
```

至少设置以下字段；如果 Windows 可以直接联网，就让 `upstream_proxy` 为空：

```json
{
  "relay_exe": "/mnt/c/Tools/wsl-win-relay/wsl-win-relay.exe",
  "broker_mode": false,
  "upstream_proxy": "",
  "socks5_listen": "127.0.0.1:1080",
  "http_proxy_listen": "127.0.0.1:8080"
}
```

需要上游代理时，将其改成例如 `socks5h://YOUR_PROXY_HOST:PORT`。

### 3. 启动和使用

```bash
"$RELAY_ROOT/bin/wsl-proxy-linux" -config "$HOME/wsl-win-relay.json"
```

保持该终端运行，在另一个 WSL 终端测试：

```bash
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

只支持 HTTP 代理的程序：

```bash
HTTPS_PROXY=http://127.0.0.1:8080 curl https://example.com
HTTP_PROXY=http://127.0.0.1:8080 curl http://example.com
```

按 `Ctrl-C` 停止。stdio 子进程退出后，已有连接不会保留。

## 推荐：持久 broker

broker 模式把 Windows socket 所有权放到独立进程中。connector、broker
frontend 或 bridge 重启时，已有 Windows socket 可以继续使用。使用前请
确认 WSL systemd 已启用。

设置已下载的 Windows 二进制路径：

```bash
export RELAY_ROOT="$HOME/wsl-win-relay"
export WSL_WIN_RELAY_BROKER_EXE="/mnt/c/Tools/wsl-win-relay/wsl-win-broker.exe"
export WSL_WIN_RELAY_CONNECTOR_EXE="/mnt/c/Tools/wsl-win-relay/wsl-win-connector.exe"
```

安装并启动服务：

```bash
cd "$RELAY_ROOT"
./scripts/install-broker-user-service.sh
./scripts/install-user-service.sh
```

安装器会在 `${XDG_CONFIG_HOME:-$HOME/.config}/wsl-win-relay/` 创建：

```text
config.json   WSL proxy 配置
broker.env    broker 路径、端点、token 和上游代理
attach.token  broker 认证 token
```

检查服务：

```bash
systemctl --user status wsl-win-relay-broker.service
systemctl --user status wsl-win-relay.service
curl --proxy socks5h://127.0.0.1:1080 https://example.com
```

### 配置 Windows 上游代理

broker 模式下，上游代理配置在 Windows broker 的私有环境中：

```bash
export WSL_WIN_RELAY_UPSTREAM_PROXY='socks5h://YOUR_PROXY_HOST:PORT'
./scripts/install-broker-user-service.sh
```

WSL 不会直接连接 `YOUR_PROXY_HOST:PORT`。stdio 模式则在 JSON 的
`upstream_proxy` 字段设置同一个 URL。

支持 HTTP/HTTPS CONNECT 和 SOCKS5/SOCKS5H。HTTP 上游只承载 TCP；SOCKS5
上游还支持通过 UDP ASSOCIATE 转发 relay UDP。使用 `socks5h` 可以让上游
代理解析目标域名。

## 配置参考

完整配置见 [`wsl-win-relay.example.json`](wsl-win-relay.example.json)。
验证配置但不启动 relay：

```bash
"$RELAY_ROOT/bin/wsl-proxy-linux" \
  -config "$HOME/.config/wsl-win-relay/config.json" \
  -check-config
```

| 字段 | 默认值 | 作用 |
| --- | --- | --- |
| `socks5_listen` | `127.0.0.1:1080` | WSL SOCKS5 监听 |
| `http_proxy_listen` | `127.0.0.1:8080` | WSL HTTP 监听 |
| `relay_handshake_timeout` | `5s` | 启动能力协商超时 |
| `relay_dial_timeout` | `30s` | 等待 relay 可用 |
| `proxy_handshake_timeout` | `15s` | 本地代理握手超时 |
| `max_proxy_connections` | `256` | 每个代理监听的客户端上限 |
| `udp_associate_idle_timeout` | `5m` | SOCKS5 UDP 空闲超时 |
| `control_socket` | `/tmp/wsl-win-relay-control.sock` | strict 控制 socket |

未知 JSON 字段会被拒绝。命令行标量参数覆盖 JSON；重复的 `-reverse` 和
`-reverse-udp` 参数会追加映射。

## 反向端口转发

### 显式 TCP 映射

假设 WSL 服务监听 `127.0.0.1:8000`，配置：

```json
{
  "reverse": [
    "127.0.0.1:8000=127.0.0.1:8000"
  ]
}
```

左侧是 Windows 监听地址，右侧是 WSL 目标地址。Windows 客户端访问：

```powershell
Invoke-WebRequest http://127.0.0.1:8000/
```

Windows 绑定失败会在映射建立时报告，不会留下半成品映射。绑定成功后的
防火墙拒绝属于另一层策略，不能转换成 WSL `listen()` 错误。

### 显式 UDP 映射

```json
{
  "reverse_udp": [
    "127.0.0.1:5353=127.0.0.1:5353"
  ]
}
```

不同 Windows 源端点会被隔离成不同 flow，响应返回到发送该数据报的源端点。

## 自动端口映射

启用 TCP 监听轮询发现：

```json
{
  "auto_forward": {
    "enabled": true,
    "windows_host": "127.0.0.1",
    "windows_host6": "::1",
    "interval": "1s",
    "exclude": [22, 53]
  }
}
```

镜像网络共享端口命名空间时，可以使用偏移或让 Windows 自动分配：

```text
-auto-forward-port-offset 10000
-auto-forward-port-auto
```

使用偏移时，WSL `8000` 会暴露为 Windows `10800`。使用自动分配时配置状态
文件：

```json
{
  "auto_forward": {
    "enabled": true,
    "windows_port_auto": true,
    "status_file": "/tmp/wsl-win-relay-mappings.json"
  }
}
```

读取实际 Windows 地址：

```bash
"$RELAY_ROOT/bin/wsl-win-relay-status" \
  -file /tmp/wsl-win-relay-mappings.json
```

WSL 监听消失后，Windows 映射会被删除。Windows 拒绝会使用有界重试退避。

### 白名单 UDP 自动发现

`/proc/net/udp` 无法可靠区分 UDP 服务 socket 和临时客户端 socket，所以不
启用全量 UDP 自动发现。已知端口可以显式加入白名单：

```json
{
  "auto_forward": {
    "enabled": true,
    "udp_enabled": true,
    "udp_include": [5353, 8125]
  }
}
```

## 严格同步 listen/bind

如果必须让 Windows 先预留端口，再让 WSL 的 `listen()` 或非零 UDP `bind()`
成功，请使用 strict：

```bash
~/bin/wsl-win-relay-run python3 -m http.server 8000
```

也可以让整个 shell 进程树继承 strict 能力：

```bash
~/bin/wsl-win-relay-shell
python3 -m http.server 8000
```

Windows 返回 `EADDRINUSE` 时，WSL 调用也返回 `EADDRINUSE`；如果 WSL 调用
失败，Windows 预留会被取消。这正是“Windows 拒绝，WSL 也拒绝”的实现方式。

静态程序使用 kernel adapter：

```bash
~/bin/wsl-win-relay-run --kernel ./static-service 8000
```

kernel adapter 在 Linux amd64 上完成运行验证，aarch64 仅构建。setuid/setgid
程序会被拒绝，因为无法安全保持其权限语义。

## 透明 TUN 模式

没有代理设置能力的程序可以使用透明模式。需要 root、`iproute2`、
`/dev/net/tun` 和 tun2socks：

```bash
./scripts/install-tun2socks.sh
sudo env \
  WWR_TUN_PROXY=socks5://127.0.0.1:1080 \
  ./scripts/transparent-relay.sh
```

如果 HNS 故障导致 WSL 普通网卡消失，而本地 SOCKS 仍可用：

```bash
sudo env \
  WWR_UPLINK_INTERFACE=lo \
  WWR_TUN_PROXY=socks5://127.0.0.1:1080 \
  ./scripts/transparent-relay.sh
```

脚本会先等待本地 SOCKS 监听，再修改路由；退出时恢复路由、DNS 和 TUN
设备。只有确实需要替换 DNS 时才设置 `WWR_DNS`。

## 服务操作和排查

查看日志：

```bash
journalctl --user -u wsl-win-relay.service -f
journalctl --user -u wsl-win-relay-broker.service -f
```

运行只读诊断：

```bash
~/bin/wsl-win-relay-doctor
~/bin/wsl-win-relay-doctor --probe-url https://example.com
```

停止服务：

```bash
systemctl --user stop wsl-win-relay.service
systemctl --user stop wsl-win-relay-broker.service
```

卸载非特权部署（保留配置和 token）：

```bash
./scripts/install-user-service.sh --uninstall
```

卸载透明服务：

```bash
sudo ./scripts/install-transparent-service.sh --uninstall
```

### WSL 无法访问 Windows 上游代理

这条直连路径本来就不是必需的。检查：

1. WSL 能否通过 interop 启动 Windows `.exe`；
2. Windows broker 是否运行；
3. broker 模式的私有 `broker.env` 是否配置了上游代理；
4. Windows 自身能否解析并访问上游代理；
5. WSL 本地 `127.0.0.1:1080` 是否在监听。

实际路径应是：

```text
WSL 程序 -> WSL 本地 relay -> Windows broker -> Windows 上游代理
```

### WSL 找不到 PowerShell

这只影响部分 interop 测试和清理脚本。设置实际挂载路径：

```bash
export WWR_WINDOWS_SHELL=/mnt/c/Path/To/pwsh.exe
```

### Windows 端口已被占用

镜像网络可能共享 WSL/Windows 端口命名空间。自动映射使用端口偏移或
Windows 自动分配；需要同步失败则使用 strict。

### 服务无法启动

```bash
~/bin/wsl-win-relay-doctor
"$RELAY_ROOT/bin/wsl-proxy-linux" \
  -config "$HOME/.config/wsl-win-relay/config.json" \
  -check-config
```

检查 broker/connector 路径是否存在、二者构建元数据是否一致。除非明确
使用旧版或自定义二进制，不要设置
`WSL_WIN_RELAY_ALLOW_UNVERIFIED_BINARIES=1`。

## Windows Task Scheduler（可选）

如果需要 broker 跨越 WSL 用户服务或 WSL VM 关闭后仍可用，可以在 PowerShell
7 中运行：

```powershell
.\scripts\install-broker-windows-task.ps1 `
  -BrokerExe 'C:\Tools\wsl-win-relay\wsl-win-broker.exe' `
  -TokenFile 'C:\Users\you\.config\wsl-win-relay\attach.token' `
  -StartNow
```

将示例路径替换成实际路径。该方式可以保持后续 attach 可用，但 socket-owner
自身崩溃后，已有连接仍会丢失。

## 验证和发布

在 amd64 WSL 中运行完整验证：

```bash
./scripts/test-release.sh
```

验证包括 Go 测试、vet、race、构建、native strict、透明路由回滚、安装器和
broker 恢复矩阵。

需要真实 Windows interop 时：

```bash
export WWR_WINDOWS_SHELL=/mnt/c/Path/To/pwsh.exe
export WWR_WINDOWS_UPSTREAM_PROXY='socks5h://YOUR_PROXY_HOST:PORT'
./scripts/test-release.sh --windows-interop
```

向仓库推送 `v<版本>` tag 会触发 GitHub Actions 发布工作流，自动构建 Linux
和 Windows 的 amd64/arm64 Go 二进制、Linux amd64 native strict 组件，生成
压缩包、`SHA256SUMS` 并创建 GitHub Release。发布包不包含任何个人路径或
上游代理地址。

## 开发

```bash
gofmt -w $(find cmd internal -type f -name '*.go')
go test ./...
go vet ./...
go test -race ./...
```

仓库包含 vendored 依赖。完整发布门禁会使用 `GOPROXY=off` 执行隔离检查。
更多设计见 [实施计划](docs/plans/2026-09-12-wsl-win-relay.md) 和
[架构决策](docs/adr/)。

向仓库推送形如 `v主版本.次版本.修订版本` 的 tag（例如 `v1.0.0`）会触发
GitHub Actions 发布工作流。工作流先运行测试、vet 和 race detector，再构建
Linux/Windows amd64/arm64 Go 二进制、Linux amd64 native strict 组件，打包
中英文说明和示例配置，生成 `SHA256SUMS` 并创建 GitHub Release。发布包不
包含任何个人路径或上游代理地址。

## 安全说明

- WSL SOCKS5/HTTP 默认只监听回环且没有认证；
- broker 和内部角色使用同用户本地 IPC，不监听局域网；
- attach token 和服务环境文件使用 `0600` 权限；
- 不要把密钥放到公开命令行，也不要把本地代理暴露到局域网；
- 项目面向同一用户的 WSL/Windows 边界，不是通用网络服务。

## 许可证

Copyright 2026 Kevin589981.

本项目使用 Apache License 2.0，详见 [`LICENSE`](LICENSE)。vendored 第三方
依赖保留各自的许可证声明。
