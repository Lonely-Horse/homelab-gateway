# homelab-gateway

把所有 homelab 服务的入口收成一个进程：**读一份 YAML，按 host / path 转发，自带 JSON 访问日志与 Prometheus 指标。**

设计取舍以 [DESIGN.md](DESIGN.md) 为准。单二进制静态编译（6.5 MB），唯一外部依赖 `go.yaml.in/yaml/v3`。
TLS、鉴权、热加载、负载均衡都不做——分别由外层 Nginx、Tailscale 和 `systemctl restart` 承担。

## 网络位置

```
公网 → VPS Nginx（TLS） → Tailscale → homelab-gateway → 127.0.0.1:8080 / :3000 ...
```

三条硬约束：`listen` 绑到**不可从公网直达的地址**（回环 / 内网 IP / tailnet IP，不要在有公网 IP 的机器上绑 `0.0.0.0`）；`upstream` 不得为公网地址（防开放代理）；`upstream` 不得指向自身（防环路）。

---

## 快速开始

第一步，把下面这段存成 `gateway.yaml`（最小可用配置，字段含义见后文）：

```yaml
listen: "127.0.0.1:8080"

timeouts:
  dial: 3s
  response_header: 10s
  idle: 90s

server:
  read_header: 5s
  write: 30s
  idle: 90s

routes:
  - name: blog
    host: blog.example.com
    upstream: "http://127.0.0.1:8081"
```

第二步，构建并启动：

```bash
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o gateway ./cmd/gateway
./gateway -cfgPath ./gateway.yaml          # 成功时不打印任何东西

curl -i -H 'Host: blog.example.com' http://127.0.0.1:8080/posts   # 按 host
curl -i http://127.0.0.1:8080/grafana/api/health                  # 按 path
curl -s http://127.0.0.1:8080/metrics                             # 指标
```

访问日志在 stdout，一行一条 JSON：`./gateway -cfgPath ./gateway.yaml | jq -c .`

`-cfgPath` 默认 `/etc/gateway/gateway.yaml`。配置有错时退出码 1，并列出全部问题：

```text
/tmp/bad.yaml:routes: at least one route is required
/tmp/bad.yaml:3: timeouts.dial: must be > 0, got 0s
```

---

## 配置

### 结构

```yaml
listen: "host:port"

timeouts:            # B 侧：网关 → 上游
  dial:
  response_header:
  idle:

server:              # A 侧：客户端 → 网关
  read_header:
  write:
  idle:

routes:              # 至少一条
  - name:
    host:
    path_prefix:
    upstream:
    strip_prefix:
    health:
```

六个超时字段**全部必填且必须 > 0**，没有默认值。

### `listen`

`host:port`，端口 1–65535。**绑哪个地址，就只接受该网卡上的连接**：

| 值 | 谁能访问 | 场景 |
|---|---|---|
| `127.0.0.1:8080` | 仅本机进程 | 前面有本机 Nginx |
| `192.168.1.10:8080` | 整个局域网 | 家里其他设备直连网关 |
| `100.x.y.z:8080` | 仅 tailnet | DESIGN 默认形态 |
| `0.0.0.0:8080` | 所有网卡 | 机器有公网 IP 时**等于公网可达**，慎用 |

注意 `0.0.0.0` 不会“只绑定内网”：它是“监听所有网卡”，包括公网那块。所以带公网 IP 的机器上应显式写内网/tailnet 地址；防火墙只能当第二道保险，不能替代绑定地址。

### `timeouts` / `server`

| 字段 | 映射到 | 作用 |
|---|---|---|
| `timeouts.dial` | `net.Dialer.Timeout` | TCP 建连；上游没开机会在这里失败 |
| `timeouts.response_header` | `Transport.ResponseHeaderTimeout` | 等响应头；超时返回 504 |
| `timeouts.idle` | `Transport.IdleConnTimeout` | 连接池空闲回收 |
| `server.read_header` | `Server.ReadHeaderTimeout` | 收请求头上限，防慢速攻击 |
| `server.write` | `Server.WriteTimeout` | 整个响应写出的上限；**太小会切断流式/大文件** |
| `server.idle` | `Server.IdleTimeout` | 长连接空闲关闭 |

```yaml
timeouts:
  dial: 3s
  response_header: 10s   # 卡的是"上游开始响应之前"，不是总耗时
  idle: 90s              # 略大于上游 Nginx keepalive_timeout
server:
  read_header: 5s
  write: 30s             # 要大于 response_header + 响应传输时间
  idle: 90s
```

### `routes[]`

| 字段 | 必填 | 说明 |
|---|---|---|
| `name` | 是 | 唯一；同时是日志字段和指标标签 `route` |
| `host` | 否 | 精确匹配 `Host`，大小写不敏感、自动去端口 |
| `path_prefix` | 否 | **纯字节前缀**，不按路径段 |
| `upstream` | 是 | 完整 URL（含 scheme）；IP 字面量必须内网段 |
| `strip_prefix` | 否 | 转发前剥掉 `path_prefix` |
| `health` | 否 | 保留字段，v0.1 解析但不使用 |

`host` 与 `path_prefix` 都为空 = 兜底路由。

### 三种典型写法

```yaml
- name: blog                # 按域名分流
  host: blog.example.com
  upstream: "http://127.0.0.1:8081"

- name: grafana             # 按路径分流
  path_prefix: "/grafana/"
  upstream: "http://127.0.0.1:3000"
  strip_prefix: true

- name: fallback            # 兜底
  upstream: "http://127.0.0.1:8081"
```

### 匹配优先级

不是先到先得，而是打分取最高：

```
score = len(path_prefix) + (host 非空且命中 ? 1<<30 : 0)
```

1. **带 `host` 的路由永远优先于只按路径的路由**
2. 同 host 下，`path_prefix` 越长越优先
3. 分数相同，配置里靠前的赢

```
GET /grafana/api/health, Host: blog.example.com
  blog    (host=blog.example.com)   → 1<<30   ← 命中
  grafana (无 host, /grafana/)       → 10
```

想在同一域名下按路径分流，就给每条路由都写同一个 `host`。

### `strip_prefix` 细节

| 请求路径（前缀 `/grafana/`） | 上游收到 |
|---|---|
| `/grafana/api/health` | `/api/health` |
| `/grafana/` | `/` |
| `/grafana` | 不命中（`HasPrefix("/grafana", "/grafana/")` 为假） |

两个坑：前缀是**纯字节比较**，`/api` 也会匹配 `/apix`，想避免就写成 `/api/`；剥完会自动补前导斜杠，否则上游回 400。

### 超时与错误码

| 场景 | 客户端看到 | 日志 `err` |
|---|---|---|
| 上游没监听 / DNS 失败 | `502 bad gateway` | `dial tcp ...: connection refused` |
| 上游不给响应头 | `504 gateway timeout` | `net/http: timeout awaiting response headers` |
| 上游返回 500 | `500`（透传） | 空 |
| 路由未命中 | `404 404 page not found` | `no_route` |
| 网关 panic | `500` | `panic: ...` |
| 客户端中途断开 | 连接中断 | `panic: net/http: abort Handler` |

### 加载期校验（8 条）

任何一条失败，退出码 1，输出 `文件:行号:字段:原因`。

1. `listen` 非空、可 `SplitHostPort`、端口 1–65535
2. `routes` 至少一条
3. `route.name` 唯一
4. `upstream` 可 `url.Parse`，scheme 为 `http` / `https`
5. IP 字面量必须在 `127/8`、`10/8`、`172.16/12`、`192.168/16`、`100.64/10`、`fc00::/7`；主机名不校验
6. `strip_prefix: true` 时 `path_prefix` 非空
7. 六个超时字段都 > 0
8. `upstream` 不指向网关自身（同端口 + 本机/通配/localhost/与 listen host 相同）

```text
gateway.yaml:16: routes[0].upstream: points to the gateway itself (127.0.0.1:8080), forms a loop
gateway.yaml:14: routes[1].upstream: non-private IP 8.8.8.8 is not allowed
```

---

## 日志

一行一条 JSON 写 stdout；诊断写 stderr（systemd 下分别是 `info` / `err` 优先级）。

```json
{"ts":"2026-10-28T01:37:29Z","route":"echo","method":"POST","path":"/echo/upload",
 "status":200,"dur_ms":8,"bytes":18,"upstream":"http://127.0.0.1:18006"}
```

| 字段 | 含义 |
|---|---|
| `ts` / `route` | UTC RFC3339 / 命中的路由名（未命中为空串） |
| `method` / `path` | 请求方法 / 路径（不含 query） |
| `status` / `dur_ms` / `bytes` | 客户端收到的最终状态码 / 耗时（毫秒）/ 响应字节数 |
| `upstream` / `err` | 命中的上游 URL / 出错原因（正常请求不出现） |

---

## 指标

`GET /metrics`，`text/plain; version=0.0.4; charset=utf-8`。

| 指标 | 类型 | 标签 |
|---|---|---|
| `gateway_requests_total` | counter | `route`, `code` |
| `gateway_duration_seconds_sum` | counter | `route` |
| `gateway_duration_seconds_count` | counter | `route` |
| `gateway_upstream_errors_total` | counter | `route`, `reason` |
| `gateway_inflight` | gauge | `route` |

`reason` 只有 `dial` / `timeout` / `5xx`。

```promql
sum(rate(gateway_requests_total[5m])) by (route)
rate(gateway_duration_seconds_sum[5m]) / rate(gateway_duration_seconds_count[5m])
sum(rate(gateway_upstream_errors_total[5m])) by (reason)
```

- `/metrics` 请求本身不计入指标、也不写访问日志，避免自我污染
- `/metrics` 优先于所有 `routes`，且**没有鉴权**，不要暴露到公网或局域网
- 标签只来自配置与固定枚举；`path` 这类用户可控值绝不能进标签（基数爆炸）

---

## 部署

仓库只交付**一个静态二进制**，进程怎么托管由你自己决定。三种常见方式：

| 方式 | 适合 | 注意 |
|---|---|---|
| systemd | 长期运行、需要崩溃自愈与开机自启 | 用专用非 root 用户跑 |
| 前台 / `nohup` / `tmux` | 临时调试、第一次试跑 | 退出终端就没了，不适合长期 |
| 容器 | 想和现有 compose 一起管 | 上游是 `127.0.0.1` 时必须 `network_mode: host`，否则连不到宿主机服务 |

### 配置文件放哪

位置决定权限与可运维性，三种都可行：

| 位置 | 场景 | 权限建议 |
|---|---|---|
| `/etc/gateway/gateway.yaml` | systemd 长期运行（推荐） | `0640`，属主为运行用户 |
| 当前目录 `./gateway.yaml` | 本地试跑 | 无需特别处理 |
| 挂载进容器 `/etc/gateway/gateway.yaml` | 容器 | 只读挂载 `:ro` |

`-cfgPath` 的默认值就是 `/etc/gateway/gateway.yaml`，所以 systemd 场景可以不传参。

### 以非 root 运行（必须）

网关能访问所有后端，不要用 root 跑。两种做法：

```bash
# 做法一：专用系统用户 + systemd
sudo useradd --system --no-create-home --shell /usr/sbin/nologin gateway
sudo install -m 0755 gateway /usr/local/bin/gateway
sudo install -d -o gateway -g gateway -m 0750 /etc/gateway
sudo install -m 0640 -o gateway -g gateway gateway.yaml /etc/gateway/gateway.yaml
```

`install` 不是“安装软件”，它就是 **cp + chown + chmod 一步做完**：

```bash
# 等价于
sudo cp   gateway.yaml /etc/gateway/gateway.yaml
sudo chown gateway:gateway /etc/gateway/gateway.yaml
sudo chmod 0640 /etc/gateway/gateway.yaml
```

用 `install` 而不是 `cp` 的原因：**`cp` 覆盖已有文件时不会改权限**（保留目标原有 mode），目标若是 `0666` 就一直是 `0666`；`install -m` 每次都强制设成指定权限。配置文件里有内网地址，权限可控比省一条命令重要。

做法二：临时试跑时降权（不需要 systemd）：

```bash
setpriv --reuid=gateway --regid=gateway --clear-groups ./gateway -cfgPath /etc/gateway/gateway.yaml
```

### systemd unit 长什么样

仓库不分发 unit 文件；下面是最小示例，按需裁剪后放到 `/etc/systemd/system/gateway.service`：

```ini
[Unit]
Description=homelab-gateway
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=gateway
Group=gateway
ExecStart=/usr/local/bin/gateway -cfgPath /etc/gateway/gateway.yaml
Restart=always
RestartSec=2s
StandardOutput=journal
StandardError=journal
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=

[Install]
WantedBy=multi-user.target
```

> `CapabilityBoundingSet=` 会丢掉绑定 1024 以下端口的能力。监听 8080 没问题；要监听 80/443，必须加 `AmbientCapabilities=CAP_NET_BIND_SERVICE`。

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now gateway
systemd-analyze verify /etc/systemd/system/gateway.service
journalctl -u gateway -o cat -p info | jq -c .     # 访问日志
```

| 改了什么 | 要执行什么 |
|---|---|
| `gateway.yaml` | `systemctl restart gateway` |
| `gateway.service` | `systemctl daemon-reload` + `restart` |

升级保留旧二进制（`gateway.old`）→ 覆盖 → `restart`；回滚换回来再 `restart`。

---

## 故障排查

| 现象 | 最可能的原因 | 怎么确认 |
|---|---|---|
| `502` + `connection refused` | 上游没启动 / 端口错 | `ss -ltnp \| grep <端口>` |
| `504` + `timeout awaiting response headers` | 上游太慢，或 `response_header` 太小 | 日志 `dur_ms` 是否≈配置值 |
| `404` + `err=no_route` | `host` / `path_prefix` 没匹配 | 带 `Host` 头复现，注意大小写与端口 |
| 200 但页面资源 404 | `strip_prefix` 改变了路径 | 对比日志 `path` 与上游收到的路径 |
| 上传大文件被切断 | `server.write` 太小 | 日志 `dur_ms`≈`server.write` |
| 别处 `curl /metrics` 失败 | 绑的是 `127.0.0.1` | 改 `listen` 为 tailnet IP |
| systemd 反复重启 | `ExecStart` 参数错（如 `-cfgPath` 没值） | `journalctl -u gateway` 找 `status=2/INVALIDARGUMENT` |

---

## 目录结构

```
homelab-gateway/
├── gateway.yaml               # 配置，本地生成（已被 .gitignore 忽略）
├── cmd/gateway/main.go        # 读配置 → 交给 server
└── internal/{config,router,proxy,accesslog,metrics,server}
```

仓库不含测试文件：测试以 `go test -overlay` 从仓库外注入，验收记录见 [NOTES.md](NOTES.md)。
