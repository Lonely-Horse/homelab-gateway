# homelab-gateway

把所有 homelab 服务的入口收成一个进程：**读一份 YAML，按 host / path 转发，自带日志与指标。**

设计与取舍以 [DESIGN.md](DESIGN.md) 为唯一真相源，本文件只说明怎么用。

## 项目定位

| 现状 | 问题 |
|---|---|
| 每个服务各自暴露一个端口 | 加一个服务 = 改 N 处（防火墙、Nginx、备份、监控） |
| 公网入口只有 VPS Nginx | homelab 内部的路由规则散落在 Nginx 配置里，本地无法复现 |
| 观测靠 node_exporter | 只知道机器活着，不知道**哪个服务**慢、**哪个上游**在报错 |

**非目标**（明确不做）：TLS 终止、鉴权、管理界面、配置热加载、负载均衡、限流熔断、服务发现、缓存。

## 网络位置

```
公网
  │  HTTPS
  ▼
VPS Nginx（TLS 终止 + 路径 ACL）
  │  HTTP over Tailscale
  ▼
homelab-gateway（本项目，监听 tailnet IP）
  │  HTTP over 127.0.0.1 / tailnet
  ├──> blog        :8080
  ├──> exam-prep   :8081
  ├──> reader      :8082
  └──> grafana     :3000
```

### 硬性约束

| 约束 | 原因 |
|---|---|
| `listen` 允许 `0.0.0.0`（容器形态），但端口发布只能绑定 tailnet 接口 | 网关能访问所有后端，暴露到公网等于把所有服务合并成一个跳板 |
| `upstream` 不得为公网地址 | 防止被当成开放代理滥用 |
| 转发目标不得指向自身监听地址 | 防止配置错误形成环路 |
| 每个请求必须有 `context` 超时 | 一个挂住的上游会占住 goroutine 和连接 |

## 目录结构

```
homelab-gateway/
├── go.mod
├── gateway.yaml                 # 配置（唯一真相）
├── cmd/gateway/main.go          # 入口：装配 + 启动
└── internal/
    ├── config/config.go         # 读取 + 校验
    ├── router/router.go         # host / path_prefix 匹配
    ├── proxy/proxy.go           # ReverseProxy 构造 + 转发
    ├── accesslog/log.go         # 一行一条 JSON
    └── metrics/metrics.go       # 手写 Prometheus 文本格式
```

## 配置

```yaml
listen: "0.0.0.0:8080"          # 容器形态；systemd 部署改为 100.x.y.z:8080

timeouts:                        # B 侧：网关 → 上游
  dial: 3s
  response_header: 10s
  idle: 90s

server:                          # A 侧：客户端 → 网关
  read_header: 5s
  write: 30s
  idle: 90s

routes:
  - name: blog
    host: blog.example.com
    upstream: "http://127.0.0.1:8081"
    health: "/healthz"

  - name: grafana
    path_prefix: "/grafana/"
    upstream: "http://127.0.0.1:3000"
    strip_prefix: true
    health: "/api/health"
```

### 字段

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `listen` | string | 是 | `host:port`；允许 `0.0.0.0`，端口 1–65535 |
| `timeouts.dial` | duration | 是 | 连接上游的超时 |
| `timeouts.response_header` | duration | 是 | 等待响应头的超时 |
| `timeouts.idle` | duration | 是 | 上游连接池空闲回收（`Transport.IdleConnTimeout`） |
| `server.read_header` | duration | 是 | `http.Server.ReadHeaderTimeout` |
| `server.write` | duration | 是 | `http.Server.WriteTimeout` |
| `server.idle` | duration | 是 | `http.Server.IdleTimeout` |
| `routes[].name` | string | 是 | 唯一，用作指标和日志的标签 |
| `routes[].host` | string | 否 | 精确匹配 `r.Host`，为空表示不限制 |
| `routes[].path_prefix` | string | 否 | 前缀匹配，为空表示不限制 |
| `routes[].upstream` | string | 是 | 完整 URL（含 scheme）；IP 必须在内网段 |
| `routes[].strip_prefix` | bool | 否 | 转发前是否剥掉 `path_prefix` |
| `routes[].health` | string | 否 | 健康检查路径（v0.1 仅用于文档，不主动轮询） |

### 加载期校验

配置文件在启动时全量校验，任何一条失败即退出（exit 1），并给出**文件:行号:字段:原因**。

1. `listen` 不为空，可 `net.SplitHostPort`，端口在 1–65535（允许 `0.0.0.0`）
2. `routes` 非空
3. 每个 `route.name` 唯一
4. 每个 `route.upstream` 能被 `url.Parse` 且 scheme 为 `http`/`https`
5. `upstream` 是 IP 字面量时，必须在内网段（10/8、172.16/12、192.168/16、127/8、100.64/10、fc00::/7）；是主机名时不做加载期校验
6. `strip_prefix` 为 true 时，`path_prefix` 不能为空
7. `timeouts` 与 `server` 下的六条超时都必须 > 0
8. `route.upstream` 不得指向网关自身监听地址（同端口 + 本机地址）
   - 端口相同（`upstream` 未写端口时按 scheme 取默认：`http`=80、`https`=443）
   - 且 host 为以下任一：回环地址（`127.0.0.0/8`、`::1`）、通配地址（`0.0.0.0`、`::`）、`localhost`（大小写不敏感）、与 `listen` 的 host 字面相同

报错示例：

```text
gateway.yaml:16: routes[0].upstream: points to the gateway itself (127.0.0.1:8080), forms a loop
```

## 快速开始

```bash
# 依赖：Go 1.24+。唯一外部依赖是 YAML 解析库 go.yaml.in/yaml/v3
go run ./cmd/gateway -cfgPath ./gateway.yaml
```

校验通过时会打印路由表后退出（当前为阶段 1，尚未开始监听）：

```text
route=blog host=blog.example.com prefix= upstream=http://127.0.0.1:8081 strip=false
route=grafana host= prefix=/grafana/ upstream=http://127.0.0.1:3000 strip=true
listen=0.0.0.0:8080 dial=3s read_header=5s
```

## 实施进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| 1 | 配置加载：`Load` + `Validate`（8 条规则）+ 行号定位 | 已完成 |
| 2 | 路由匹配：`Router.Match` | 未开始 |
| 3 | 单路由转发：`ReverseProxy` | 未开始 |
| 4 | 多路由 + `strip_prefix` | 未开始 |
| 5 | 访问日志 + `statusRecorder` | 未开始 |
| 6 | `/metrics`（手写 exposition format） | 未开始 |
| 7 | 故障测试（502 / 504 / 并发 / panic） | 未开始 |
