# homelab-gateway 设计文档

| 项 | 内容 |
|---|---|
| 版本 | v0.3.1（对账修订 + 修复包；配置契约不变） |
| 日期 | 2026-11-09 |
| 状态 | v0.3 功能已实现（config / router / proxy / accesslog / metrics / server）；阶段 7（手工故障验收）待做；本版定义修复与验收 |
| 语言 | Go 1.24（标准库）+ `go.yaml.in/yaml/v3`（唯一外部依赖） |
| 代码位置 | 独立仓库 `homelab-gateway`，模块名 `homelab-gateway` |
| 上游文档 | v0.3（2026-10-24，配置契约冻结） |

> 本文以**实际代码**为准修订：凡是 v0.3 文档与 `main` @ 2026-10-28 不一致的地方，在 §14.2 列出，并在正文中给出 v0.3.1 的处理方式。配置 schema 不变，不新增字段。

---

## 1. 项目定位

### 1.1 一句话

把所有 homelab 服务的入口收成一个进程：**读一份配置，按 host / path 转发，自带日志与指标。**

### 1.2 为什么做

| 现状 | 问题 |
|---|---|
| 每个服务各自暴露一个端口 | 加一个服务 = 改 N 处（防火墙、Nginx、备份、监控） |
| 公网入口只有 VPS Nginx | homelab 内部的路由规则散落在 Nginx 配置里，本地无法复现 |
| 观测靠 node_exporter | 只知道机器活着，不知道**哪个服务**慢、**哪个上游**在报错 |

### 1.3 非目标（明确不做）

| 不做 | 理由 |
|---|---|
| TLS 终止 | VPS 的 Nginx 已经在做 |
| 鉴权 / 登录 | Tailscale 已经在做（网络层隔离） |
| 管理界面（Web UI） | 只有一个用户，配置文件就是管理界面 |
| 配置热加载 | 重启耗时 < 2s，v0.3 不做 |
| 负载均衡 | 每个上游只有单实例 |
| 限流 / 熔断 | 非目标；有真实需求再立项 |
| 服务发现 | 路由表是静态 YAML |
| 缓存 | 代理不缓存响应体 |
| method 匹配 / 405 | 配置契约冻结；v0.4 再提案（§14.3） |
| 自动化测试 | v0.3.1 不写；用 §11 手工清单验收 |

---

## 2. 系统边界

### 2.1 网络位置

**目标形态（尚未切换）**：

```text
公网
  │  HTTPS
  ▼
VPS Nginx（TLS 终止 + 路径 ACL）
  │  wg0：10.99.0.1/32
  │  AllowedIPs = 10.99.0.2/32
  ▼
MP9（homelab-gateway，双归属）
  │  tailscale0 / LAN
  ├──> blog        :8080
  ├──> exam-prep   :8081
  ├──> reader      :8082
  └──> grafana     :3000
```

**当前形态（2026-11-09）**：

```text
公网 → VPS Nginx → Tailscale IP → 各服务
```

网关仍是实验入口，未切主流量。切换条件见 §13.2。

### 2.2 硬性约束

| 约束 | 原因 |
|---|---|
| `listen` **允许 `0.0.0.0`**（容器形态必需），但端口发布只能绑定 tailnet 接口 | 网关能访问所有后端，暴露到公网等于把所有服务合并成一个跳板 |
| `upstream` **不得为公网地址** | 防止被当成开放代理滥用 |
| 转发目标**不得指向自身监听地址** | 防止配置错误形成环路 |
| 每个请求必须有 `context` 超时 | 一个挂住的上游会占住 goroutine 和连接 |
| 路径匹配必须先归一化 | 防止 `/api/../admin` 绕过前缀策略 |

### 2.3 信任边界

- 网关**信任**来自 Tailscale 的请求（网络层已隔离）。
- 网关**不信任**任何请求头：
  - `X-Forwarded-For`：先删除入站值，再由 `SetXForwarded` 重建。
  - `Forwarded`：直接删除，不向上游传递。
  - `X-Forwarded-Host`：由 `SetXForwarded` 用 `r.In.Host` 覆盖，客户端传入的值无效。
- 网关**不解析**请求体（纯转发，不做业务逻辑）。

> v0.3 代码只调用了 `pr.SetXForwarded()`，没有先删除 XFF / `Forwarded`；v0.3.1 按本节修正。详见 §14.2 #4。

---

## 3. 目录结构（实际）

```text
homelab-gateway/
├── go.mod                       # module homelab-gateway；go 1.24.4
├── gateway.yaml                 # 配置（唯一真相；不进 Git）
├── gateway.service              # systemd unit（非 root + 加固）
├── cmd/
│   └── gateway/
│       └── main.go              # 入口：flag + config.Load + server.Run
└── internal/
    ├── config/
    │   ├── config.go            # 读取 + 校验 + 行号索引
    ├── router/
    │   ├── router.go            # host / path_prefix 匹配
    ├── proxy/
    │   ├── proxy.go             # ReverseProxy 构造 + 转发
    ├── accesslog/
    │   └── log.go               # 一行一条 JSON + Recorder
    ├── metrics/
    │   └── metrics.go           # 手写 Prometheus 文本格式
    └── server/
        ├── server.go            # 中间件链 + panic recover + /metrics
```

> v0.3 文档的目录结构漏了 `internal/server`。以本表为准。

---

## 4. 配置规范

### 4.1 完整示例

```yaml
listen: "0.0.0.0:8080"          # 容器形态；systemd 部署改为 100.x.y.z:8080

timeouts:                        # B 侧：网关 → 上游
  dial: 3s
  response_header: 10s
  idle: 90s                      # Transport.IdleConnTimeout

server:                          # A 侧：客户端 → 网关
  read_header: 5s
  write: 30s
  idle: 90s

routes:
  - name: blog
    host: blog.example.com
    upstream: "http://127.0.0.1:8080"
    health: "/healthz"            # 保留字段，v0.3 不使用

  - name: grafana
    path_prefix: "/grafana/"
    upstream: "http://127.0.0.1:3000"
    strip_prefix: true
    health: "/api/health"
```

### 4.2 字段说明

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `listen` | string | ✅ | `host:port`；允许 `0.0.0.0`（容器形态），端口 1–65535 |
| `timeouts.dial` | duration | ✅ | 连接上游的超时 |
| `timeouts.response_header` | duration | ✅ | 等待响应头的超时 |
| `timeouts.idle` | duration | ✅ | 上游连接池空闲回收（`Transport.IdleConnTimeout`） |
| `server.read_header` | duration | ✅ | `http.Server.ReadHeaderTimeout` |
| `server.write` | duration | ✅ | `http.Server.WriteTimeout`（会杀掉 SSE，见 §7.3） |
| `server.idle` | duration | ✅ | `http.Server.IdleTimeout` |
| `routes[].name` | string | ✅ | 唯一，用作指标和日志的标签；限 `[A-Za-z0-9._-]+` |
| `routes[].host` | string | ❌ | 精确匹配 `r.Host`（大小写不敏感、去端口、去尾点），为空表示不限制 |
| `routes[].path_prefix` | string | ❌ | 段边界前缀匹配，为空表示不限制；必须以 `/` 开头 |
| `routes[].upstream` | string | ✅ | 完整 URL（含 scheme）；host 为 IP 字面量时必须在内网段 |
| `routes[].strip_prefix` | bool | ❌ | 转发前是否剥掉 `path_prefix` |
| `routes[].health` | string | ❌ | 保留字段：v0.3 解析但**完全不使用** |

### 4.3 校验规则（加载时全部检查，失败即退出）

**v0.3 已有（保持）**：

1. `listen` 不为空，可 `net.SplitHostPort`，端口在 1–65535（允许 `0.0.0.0`）。
2. `routes` 非空。
3. 每个 `route.name` 唯一。
4. 每个 `route.upstream` 能被 `url.Parse` 且 scheme 为 `http`/`https`，host 非空。
5. `upstream` 的 host 为 IP 字面量时，必须在内网段（10/8、172.16/12、192.168/16、127/8、100.64/10、fc00::/7）；host 为主机名时不做加载期校验。
6. `strip_prefix` 为 true 时，`path_prefix` 不能为空。
7. 六条超时都必须 `> 0`。
8. `route.upstream` 不得指向网关自身监听地址（同端口 + 本机地址）。

**v0.3.1 收紧（不新增字段）**：

9. `path_prefix` 非空时必须以 `/` 开头；拒绝 `..` 段、`//`、`?`、`#`；`/` 归一化为空。
10. `route.name` 必须匹配 `[A-Za-z0-9._-]+`。
11. 两条路由的 `(host, path_prefix)` 归一化后不得完全相同。
12. `strip_prefix: true` 时，upstream URL 的 path 必须为空或 `/`。
13. upstream 拒绝 userinfo（`user:pass@`）。

> 第 9–13 条不新增配置字段，但会拒绝“以前能加载、实际不工作或会打挂 `/metrics`”的配置。发布说明里要写明。

**校验失败的输出必须包含具体行号和字段名**，不能只说 `invalid config`。

### 4.4 归一化规则（v0.3.1）

**Host**：

1. 取 `r.Host`，去掉端口（`net.SplitHostPort`；无端口则原样）。
2. 转小写。
3. 去掉末尾的 `.`（FQDN 尾点）。
4. 配置里的 `host` 做同样的归一化；拒绝 scheme / path / port / 通配符。

**Path**：

1. 空路径 → `/`。
2. `path.Clean`（解析 `.`、`..`、`//`）。
3. 保证以 `/` 开头。
4. 清空 `URL.RawPath`（后续统一用 `URL.Path`）。
5. 请求的 query 不参与匹配，原样转发。

**PathPrefix**：

1. 必须以 `/` 开头。
2. `path.Clean`；拒绝包含 `..` 段或 `//` 的原始值。
3. 去掉末尾 `/`（`/api/` → `/api`）；单独的 `/` → 空（兜底）。
4. `strip_prefix` 使用归一化后的 prefix。

### 4.5 匹配优先级（v0.3.1）

```text
score = len(path_prefix) + (host 非空且命中 ? 1<<30 : 0)
```

1. **带 `host` 的路由永远优先于只按路径的路由**。
2. 同 host 下，`path_prefix` 越长越优先。
3. 分数相同，配置里靠前的赢；但完全相同的 `(host, prefix)` 会在加载期报错，所以同分只可能来自不同 host / 不同前缀的合法组合。

**前缀按段比较**：

```text
p == prefix || strings.HasPrefix(p, prefix+"/")
```

- `/api` 命中 `/api`、`/api/`、`/api/x`。
- `/api` **不命中** `/apix`。
- 前缀为空（或 `/`）时匹配所有路径。

**示例**：

```text
GET /grafana/api/health, Host: blog.example.com
  blog    (host=blog.example.com)   → 1<<30   ← 命中
  grafana (无 host, /grafana)        → 8
```

想在同一域名下按路径分流，就给每条路由都写同一个 `host`。

**不做 method 匹配**：所有路由接受所有 HTTP method；没有 405。method / 405 是 v0.4 提案（§14.3）。

---

## 5. 核心数据结构

```go
// ── config ────────────────────────────────────
type Config struct {
    Listen   string         `yaml:"listen"`
    Timeouts Timeouts       `yaml:"timeouts"`
    Server   ServerTimeouts `yaml:"server"`
    Routes   []Route        `yaml:"routes"`
}

// B 侧：网关 → 上游
type Timeouts struct {
    Dial           time.Duration `yaml:"dial"`
    ResponseHeader time.Duration `yaml:"response_header"`
    Idle           time.Duration `yaml:"idle"`
}

// A 侧：客户端 → 网关
type ServerTimeouts struct {
    ReadHeader time.Duration `yaml:"read_header"`
    Write      time.Duration `yaml:"write"`
    Idle       time.Duration `yaml:"idle"`
}

type Route struct {
    Name        string `yaml:"name"`
    Host        string `yaml:"host"`
    PathPrefix  string `yaml:"path_prefix"`
    Upstream    string `yaml:"upstream"`
    StripPrefix bool   `yaml:"strip_prefix"`
    Health      string `yaml:"health"`
}

// ── router ────────────────────────────────────
type Router struct {
    routes []Route
}

// 归一化路径；server 在 Match 前调用
func NormalizePath(p string) string

// 返回匹配的 route；priority: host 精确匹配 > path_prefix 最长匹配
func (r *Router) Match(req *http.Request) (*Route, bool)

// ── proxy ─────────────────────────────────────
// 一个 route 一个实例，初始化时构造
type Proxy struct {
    route *Route
    rp    *httputil.ReverseProxy
}

func New(route *Route, t Timeouts, shared *http.Transport) *Proxy
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)

// ── accesslog ─────────────────────────────────
type Entry struct {
    TS       string `json:"ts"`
    Route    string `json:"route"`
    Method   string `json:"method"`
    Path     string `json:"path"`
    Status   int    `json:"status"`
    DurMS    int64  `json:"dur_ms"`
    Bytes    int64  `json:"bytes"`
    Upstream string `json:"upstream"`
    Err      string `json:"err,omitempty"`
}

type Recorder struct {
    http.ResponseWriter
    Status    int
    Bytes     int64
    ErrMsg    string
    ErrReason string
}

// ── metrics ───────────────────────────────────
type Metrics struct {
    mu       sync.Mutex
    requests map[key]uint64
    errs     map[key]uint64
    durSum   map[string]float64
    durCount map[string]uint64
    inflight map[string]int64
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

> `Route` 没有 `method` 字段；v0.3.1 不改这个结构体。

---

## 6. 请求生命周期

### 6.1 时序

```text
conn accepted
  │
  ▼
http.Server（ReadHeaderTimeout / WriteTimeout / IdleTimeout）
  │
  ▼
rootHandler
  │
  ├─ 1. path := router.NormalizePath(req.URL.Path)
  │      req.URL.Path = path; req.URL.RawPath = ""
  ├─ 2. rec := &accesslog.Recorder{ResponseWriter: w}   // Status 零值 = 还没写过
  ├─ 3. start := time.Now()
  ├─ 4. defer func() {
  │         // recover panic；记录 accesslog.Entry；metrics.Observe
  │     }()
  ├─ 5. route, ok := router.Match(req)
  │         if !ok { http.NotFound(rec, req); return }
  ├─ 6. metrics.IncInflight(route.Name); defer metrics.DecInflight(...)
  ├─ 7. proxies[route.Name].ServeHTTP(rec, req)
  │
  ▼
ReverseProxy
  │
  ├─ Rewrite:
  │    Del("X-Forwarded-For")
  │    Del("Forwarded")
  │    SetURL(target)
  │    SetXForwarded()          // XFH = r.In.Host
  │    可选剥前缀（段边界已由 router 保证）
  │
  ├─ Transport: DialContext(3s) + ResponseHeaderTimeout(10s)
  │
  ▼
upstream
```

### 6.2 状态码策略

| 情况 | 返回 | 日志字段 |
|---|---|---|
| 无匹配路由 | 404 | `route=""`, `err="no_route"` |
| 上游连接失败 | 502 | `err="dial: ..."` |
| 上游响应头超时 | 504 | `err="timeout"` |
| 上游返回 5xx | 透传 | 原样记录 |
| 网关自身 panic | 500 | `err="panic: ..."`（必须 recover） |
| 客户端中途断开 | 连接中断 | `err="canceled"`（v0.3.1 记录，不写 502） |

**`ErrorHandler` 必须自定义**，默认实现只返回 502，不记录原因。

```go
rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
    // 根据 err 类型区分 502 / 504
    // 写入 rec.err 供 defer 记录
    http.Error(w, "bad gateway", http.StatusBadGateway)
}
```

> v0.3 的 `ErrorHandler` 用 `errors.Is(err, context.DeadlineExceeded)` 判断超时，抓不到 `ResponseHeaderTimeout`；v0.3.1 改用 `net.Error.Timeout()`。详见 §14.2 #7。

### 6.3 路径与 Host 归一化

见 §4.4。关键点：

- `path.Clean` 之后，`/api/../admin` 变成 `/admin`，不再命中 `/api`。
- `%2F` 解码成 `/`；`RawPath` 清空，上游收到统一路径。
- Host 大小写不敏感、去端口、去尾点；路径大小写敏感。

### 6.4 X-Forwarded-* 与 Forwarded

**v0.3 实际行为**（`proxy.go:32`）：

- 只调用 `pr.SetXForwarded()`。
- `X-Forwarded-For`：追加客户端传入的值（实测 `1.2.3.4, 9.9.9.9`）。
- `X-Forwarded-Host`：被 `r.In.Host` 覆盖，客户端值无效。
- `Forwarded`：完全没处理。

**v0.3.1 目标**：

```go
pr.Out.Header.Del("X-Forwarded-For")
pr.Out.Header.Del("Forwarded")
pr.SetXForwarded()
pr.Out.Header.Set("X-Real-IP", clientIP)   // 可选
```

**为什么不用 `Director`**：v0.3 已实测，`Director` 里手工 `Set` XFF 会被库在返回后追加一次，上游收到重复 IP。`Rewrite` 是正确路径。

---

## 7. 关键设计决策

### 7.1 每个 route 一个 ReverseProxy 实例

**问题**：全局单实例时，改写函数无法知道“当前请求属于哪个上游”。

**错误做法**：

```go
var globalProxy = &httputil.ReverseProxy{
    Rewrite: func(pr *httputil.ProxyRequest) {
        // 只能从 context 挖，或从全局 map 里按 Host 反查
        // → 并发下极易出错
    },
}
```

**正确做法**：

```go
func New(route *Route, t Timeouts, shared *http.Transport) *Proxy {
    target, _ := url.Parse(route.Upstream)
    prefix := route.PathPrefix

    rp := &httputil.ReverseProxy{
        Rewrite: func(pr *httputil.ProxyRequest) {
            pr.Out.Header.Del("X-Forwarded-For")
            pr.Out.Header.Del("Forwarded")
            pr.SetURL(target)
            pr.SetXForwarded()

            if route.StripPrefix {
                pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, prefix)
                if pr.Out.URL.Path == "" {
                    pr.Out.URL.Path = "/"
                }
            }
        },
        Transport: shared,
    }
    return &Proxy{route: route, rp: rp}
}
```

**关键点**：

- `Rewrite` 是并发调用的，闭包捕获自己的 `target` 和 `prefix`，无共享状态。
- `Transport` 可以共享（并发安全），连接池复用。
- 不用 `Director`：`Director` 路径下 XFF 会被库追加一次，出现重复 IP。
- `strip_prefix` 只在 router 保证段边界后执行，`TrimPrefix` 不会误伤 `/apix`。

### 7.2 statusRecorder 与 ResponseController

**问题**：为了记录状态码，需要包装 `ResponseWriter`。包装后直接做 `w.(http.Flusher)` / `w.(http.Hijacker)` 会失败。

**v0.3 实际实现**：

```go
type Recorder struct {
    http.ResponseWriter
    Status int
    Bytes  int64
}

func (r *Recorder) Write(b []byte) (int, error) {
    // v0.3 缺了这一步：if r.Status == 0 { r.Status = 200 }
    n, err := r.ResponseWriter.Write(b)
    r.Bytes += int64(n)
    return n, err
}

func (r *Recorder) Unwrap() http.ResponseWriter {
    return r.ResponseWriter
}
```

**v0.3.1 修正**：

1. `Write` 首次写入时把 `Status` 置 200，否则 panic 分支会误判“还没写头”，在已写 body 后再 `WriteHeader(500)`，导致日志 500、客户端 200。
2. **保留 `Unwrap`，不必显式实现 `Flush` / `Hijack`**：Go 1.24 的 `ReverseProxy` 用 `http.NewResponseController(rw)` 做 Flush（`reverseproxy.go:538`、`619`）和 Hijack（`755`），`ResponseController` 会沿 `Unwrap` 链找到底层 writer。已核对 Go 1.24.4 源码。
3. 只有调用方**直接做接口断言**（`w.(http.Flusher)`）时，才需要逐个显式透传。

### 7.3 超时必须由内向外递增

```text
客户端 ──(A)──> 网关 ──(B)──> 上游
```

**必须满足 `A > B`。**

| 位置 | 参数 | 建议值 |
|---|---|---|
| B：网关 → 上游 | `Transport.DialContext` 超时 | 3s |
| B：网关 → 上游 | `Transport.ResponseHeaderTimeout` | 10s |
| A：客户端 → 网关 | `http.Server.ReadHeaderTimeout` | 5s |
| A：客户端 → 网关 | `http.Server.WriteTimeout` | 30s |
| A：客户端 → 网关 | `http.Server.IdleTimeout` | 90s |

**若 A < B**：网关还在等上游，客户端已经断开，白等一场。
**若 B = 0**：一个挂住的上游会永久占用 goroutine 和连接。

**注意**：`WriteTimeout` 对 SSE 是致命的。若未来要代理流式上游，必须将 `WriteTimeout` 设为 0 或足够大，并用 `ResponseController` 单独控制。

### 7.4 为什么手写 `/metrics`，不引入 client_golang

| 理由 | 说明 |
|---|---|
| 练习目的 | 手写一遍 exposition format，才知道库在帮你做什么 |
| 依赖最小化 | 标准库足够（`fmt.Fprintf` + `sort`） |
| 可控 | 标签基数由自己控制，不会被库的默认行为坑 |

**代价**：需要自己处理转义、排序（Prometheus 不要求排序，但输出稳定便于人工比对）、并发安全。

**格式规范**：

```text
# HELP gateway_requests_total 按路由和状态码统计的请求数
# TYPE gateway_requests_total counter
gateway_requests_total{route="blog",code="200"} 42
gateway_requests_total{route="blog",code="502"} 3
```

**三条规则**：

1. `# HELP` 和 `# TYPE` 是可选元数据，但强烈建议写。
2. 样本格式：`name{label="value",...} value`。
3. 标签值中的 `\`、`"`、`\n` 必须转义为 `\\`、`\"`、`\n`。

**Content-Type**：`text/plain; version=0.0.4; charset=utf-8`

> v0.3 的 `escapeLabel` 定义了但没调用；v0.3.1 必须调用，详见 §8.3。

### 7.5 前缀必须是段边界

`README.md` v0.3 写“`/api` 也会匹配 `/apix`，想避免就写成 `/api/`”。这等于把正确性交给配置作者。

v0.3.1 改为代码保证：

```text
p == prefix || strings.HasPrefix(p, prefix+"/")
```

- `/api` 命中 `/api`、`/api/`、`/api/x`。
- `/api` 不命中 `/apix`。
- `strip_prefix` 只在命中后执行，结果为空补 `/`。

### 7.6 不做 method 匹配 / 405

`Route` 没有 `method` 字段，配置契约冻结。所有路由接受所有 method；没有 405。

如果将来需要，按 v0.4 提案一次性加字段（§14.3），不要零散改 schema。

### 7.7 不做热加载 / 认证 / 限流

`DESIGN.md` v0.3 已列为非目标：

- 重启 < 2s，热加载收益低。
- Tailscale 已做网络层隔离，认证收益有限。
- Nginx 的 `limit_req` 已经够用。

有真实需求再重新立项；不要为了“看起来完整”加模块。

---

## 8. 指标规范

### 8.1 指标清单（v0.3.1 保持 5 个）

| 指标名 | 类型 | 标签 | 含义 |
|---|---|---|---|
| `gateway_requests_total` | counter | `route`, `code` | 请求总数 |
| `gateway_duration_seconds_sum` | counter | `route` | 总耗时 |
| `gateway_duration_seconds_count` | counter | `route` | 请求数（配合 sum 算平均） |
| `gateway_upstream_errors_total` | counter | `route`, `reason` | 上游错误（`dial`/`timeout`/`5xx`） |
| `gateway_inflight` | gauge | `route` | 当前在途请求数 |

> v0.3.1 不新增 histogram；p95/p99 是 v0.4 候选（§14.3）。先用 `sum/count` 算平均。

### 8.2 基数控制

**标签值必须来自有限集合。**

| 标签 | 安全吗 | 说明 |
|---|---|---|
| `route` | ✅ 安全 | 来自配置，数量固定；未匹配用 `""` |
| `code` | ✅ 安全 | HTTP 状态码，最多几十个 |
| `reason` | ✅ 安全 | 枚举值（dial/timeout/5xx/canceled） |
| `path` | ❌ **禁止** | 用户可控，会导致指标爆炸 |
| `user_id` | ❌ **禁止** | 同上 |

**判据**：标签值的可能取值必须是“你能枚举出来的”。任何一个不可枚举的值，都会让 Prometheus 的内存线性增长。

### 8.3 转义

所有标签值必须经过 `escapeLabel`：

```go
func escapeLabel(s string) string {
    s = strings.ReplaceAll(s, `\`, `\\`)
    s = strings.ReplaceAll(s, `"`, `\"`)
    s = strings.ReplaceAll(s, "\n", `\n`)
    return s
}
```

v0.3 定义了但没调用；v0.3.1 在 `requests_total`、`upstream_errors_total`、`inflight` 输出时统一调用。同时 `route.name` 限制字符集（§4.3 #10），双保险。

### 8.4 输出示例

```text
# HELP gateway_requests_total 按路由和状态码统计的请求数
# TYPE gateway_requests_total counter
gateway_requests_total{code="200",route="blog"} 42
gateway_requests_total{code="502",route="blog"} 3

# HELP gateway_inflight 当前在途请求数
# TYPE gateway_inflight gauge
gateway_inflight{route="blog"} 2
```

---

## 9. 日志规范

### 9.1 字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `ts` | string | RFC3339 |
| `route` | string | 匹配到的路由名；未匹配为空 |
| `method` | string | 原样 |
| `path` | string | 请求路径（**不含 query，避免泄露敏感参数**） |
| `status` | int | 返回给客户端的状态码 |
| `dur_ms` | int64 | 总耗时（毫秒） |
| `bytes` | int64 | 响应体字节数 |
| `upstream` | string | 实际转发的 `host:port`（**不含 userinfo / path**） |
| `err` | string | 错误原因，成功时省略 |

> v0.3 的 `server.go:82` 记录的是完整 `rt.Upstream` URL；v0.3.1 改为 `u.Host`，并在加载期拒绝 userinfo。

### 9.2 示例

```json
{"ts":"2026-10-22T10:00:00Z","route":"blog","method":"GET","path":"/posts","status":200,"dur_ms":12,"bytes":5150,"upstream":"127.0.0.1:8080"}
{"ts":"2026-10-22T10:00:05Z","route":"blog","method":"GET","path":"/admin","status":502,"dur_ms":3001,"bytes":21,"upstream":"127.0.0.1:8080","err":"dial tcp 127.0.0.1:8080: connect: connection refused"}
```

### 9.3 输出目标

- stdout（由 systemd / docker logs 收集）。
- 不写文件（避免磁盘打满）。
- 不记录 `Authorization` / `Cookie`。

---

## 10. 实现顺序与验收（v0.3.1）

每个阶段都必须独立可运行，不允许“全部写完再跑”。

### 阶段 1：配置归一化 + 收紧校验（半天）

- 改 `config.go`：§4.3 #9–13 + §4.4 归一化。
- 用 §11 的第 1–2 条手工验证配置加载。
- `main.go` 不改。

**验收**：错误配置退出码 1，报错含文件名、行号、字段名；正常配置能启动。

### 阶段 2：路由段边界 + 路径归一化（半天）

- 改 `router.go`：`NormalizePath`、段边界匹配、host 归一化。
- 用 §11 的第 3–5 条手工验证路由。
- `Match` 签名保持 `(*Route, bool)`。

**验收**：`/apix` 404；`/api/../admin` 404；`/api/x` 200。

### 阶段 3：server 抽 NewHandler + 404（半天）

- 抽 `server.NewHandler(cfg) http.Handler`；`Run` 只负责 `ListenAndServe`。
- handler 里先 `NormalizePath`，再 `Match`。
- 用 §11 的第 3–5 条手工验证 404 与路径归一化。

**验收**：404 正常；panic 注入返回 500，进程不退出。

### 阶段 4：proxy 修复（1 天）

- `ErrorHandler`：`net.Error.Timeout()` → 504；其他 → 502；`context.Canceled` → `canceled`。
- `Rewrite`：`Del("X-Forwarded-For")`、`Del("Forwarded")`，再 `SetXForwarded()`。
- 日志 `upstream` 改 `u.Host`。
- 用 §11 的第 6–8 条手工验证 502/504/XFF。

**验收**：上游 sleep 超时返回 504；伪造 XFF / Forwarded 不生效。

### 阶段 5：文档同步（半天）

- 更新 `README.md`：段边界、归一化、504、XFF、`upstream` 字段。
- 更新 `DESIGN.md`：以本文为准；§14.2 的对账项逐条打勾。
- 删除 `gateway` 二进制出 Git；`.gitignore` 加 `gateway`、`gateway.old`。

**验收**：README / DESIGN 与实际代码一致；`git ls-files | grep -x gateway` 为空。

### 阶段 6：总验收（半天）

```bash
go vet ./...
CGO_ENABLED=0 go build -o /tmp/gateway ./cmd/gateway
```

手工矩阵：

| # | 请求 | 期望 |
|---|---|---|
| 1 | `curl -H "Host: blog.example.com" http://gw:8080/posts` | 200 |
| 2 | `curl http://gw:8080/apix` | 404 |
| 3 | `curl http://gw:8080/api/../admin` | 404（不落到 `/api`） |
| 4 | 停掉上游 | 502，日志 `connection refused` |
| 5 | 上游 sleep 20s | 504，`dur_ms` ≈ 10000 |
| 6 | 带伪造 `X-Forwarded-For` 请求 | 上游只看到真实 IP |
| 7 | 带伪造 `Forwarded` 请求 | 上游看不到该头 |
| 8 | `curl /metrics` | 5 个指标，格式合法 |
| 9 | 并发 200 请求 | `gateway_inflight` 峰值 > 0，之后归零 |
| 10 | panic 注入 | 500，进程不退出 |

**验收**：十条全部通过，日志和指标都对得上。

---

## 11. 手工验收清单

每条用 `curl` / 日志 / `/metrics` 人工核对一次，记录结果。

| # | 操作 | 期望 |
|---|---|---|
| 1 | 用错误配置启动（`path_prefix: api`） | 退出码 1，报错含文件名、行号、字段 |
| 2 | 启动正常配置 | 进程监听；日志无错误 |
| 3 | `curl -H "Host: blog.example.com" http://gw:8080/posts` | 200 |
| 4 | `curl http://gw:8080/apix` | 404（不命中 `/api`） |
| 5 | `curl http://gw:8080/api/../admin` | 404（不落到 `/api`） |
| 6 | 停掉上游 | 502，日志 `connection refused` |
| 7 | 上游 sleep 20s | 504，`dur_ms` ≈ 10000 |
| 8 | 带伪造 `X-Forwarded-For` / `Forwarded` 请求 | 上游只看到真实 IP；看不到 `Forwarded` |
| 9 | `curl http://gw:8080/metrics` | 5 个指标，格式合法 |
| 10 | 并发 200 请求 | `gateway_inflight` 峰值 > 0，之后归零 |
| 11 | panic 注入 | 500，进程不退出 |

> 以上是人工核对。若以后要接主流量，建议至少把第 3–9 条固化成脚本（可选）。

---

## 12. 最终验收清单（v0.3.1 完成标准）

- [ ] 不新增任何配置字段（`Route` 结构体不变）。
- [ ] `go vet ./...` 无输出。
- [ ] §11 手工验收清单全部通过。
- [ ] `/apix` 不命中 `/api`；`/api/../admin` 不命中 `/api`。
- [ ] 上游 sleep 超过 `response_header` → 504，不是 502。
- [ ] 伪造 `X-Forwarded-For` / `Forwarded` 不生效；`X-Forwarded-Host` 用 `r.In.Host`。
- [ ] 404 指标标签为 `route=""`（与 v0.3 一致）。
- [ ] `escapeLabel` 被调用；`Recorder.Write` 首次写入置 200；`upstream` 日志为 `host:port`。
- [ ] `README.md` / `DESIGN.md` 与代码一致；`gateway` 二进制移出 Git。
- [ ] `CGO_ENABLED=0 go build` 产出单个二进制，< 15MB。
- [ ] 服务以非 root 用户运行；监听端口只发布在 tailnet 接口。

---

## 13. 风险与回滚

| 风险 | 影响 | 缓解 |
|---|---|---|
| 网关崩溃 | **所有服务不可达**（单点） | `systemd Restart=always`；保留 tailnet 直连作为逃生通道 |
| 配置写错 | 启动失败 | 启动时全量校验；失败即退出，不留半可用状态 |
| 路径归一化改变语义 | 上游收不到编码斜杠 | 文档写明；需要时按路由开关（v0.4 候选） |
| 校验收紧拒绝旧配置 | 启动失败 | 发布说明写明；旧配置本来就不工作 |
| 指标基数爆炸 | Prometheus OOM | 标签只允许来自配置 |
| 网关成为跳板 | 内网被横向访问 | 端口只发布到 tailnet 接口 + 上游白名单双重约束 |
| `WriteTimeout` 杀掉流式 | SSE 静默失效 | 若代理流式上游，需单独评估 |

### 13.1 上线阶段

| 阶段 | 动作 | 通过条件 |
|---|---|---|
| P0 | v0.3.1 完成，§11 手工清单通过 | §12 清单全部打勾 |
| P1 | 网关作为**第二入口**接一条低风险路由（如课件站） | 一周内 5xx 不高于 Nginx 基线；p95 可接受 |
| P2 | VPS 切到 `wg0` → 网关，退出 tailnet | P1 稳定；回滚演练通过 |
| P3 | 网关成为唯一 L7 入口 | 监控、告警、回滚全部就绪 |

### 13.2 切换条件（P2 之前必须全部满足）

- [ ] v0.3.1 完成，§11 手工清单通过。
- [ ] 网关至少有一项 Nginx 写起来痛的能力（按路由指标 / 认证 / 热加载，至少一项）。
- [ ] VPS 的 Nginx 保留旧 upstream，改一行即可回滚。
- [ ] 网关配置进 Git，二进制保留上一版（`gateway.old`）。
- [ ] `wg0` 的 `AllowedIPs` 只有 `10.99.0.2/32`，VPS 路由表没有 `100.x`。
- [ ] 网关挂了有告警（systemd `Restart=always` + 监控）。

### 13.3 回滚

**回滚方案**：

1. 保留上一个二进制的副本（`gateway.old`）。
2. 部署脚本里切换 + 重启。
3. 回滚命令：`mv gateway.old gateway && systemctl restart gateway`。
4. **保留 tailnet 直连端口**，作为网关不可用时的应急路径。

---

## 14. 附录

### 14.1 与 nginx 的对照

| nginx | homelab-gateway | 说明 |
|---|---|---|
| `listen 8080;` | `listen:` | — |
| `server_name blog.example.com;` | `routes[].host` | 归一化后精确匹配 |
| `location /grafana/` | `routes[].path_prefix` | 段边界前缀匹配 |
| `proxy_pass http://127.0.0.1:3000;` | `routes[].upstream` | — |
| `proxy_set_header X-Forwarded-For` | `Rewrite` 内先删后 `SetXForwarded()` | 覆盖而非追加 |
| `proxy_read_timeout` | `timeouts.response_header` | — |
| `proxy_buffering off;` | 天然无缓冲（直接转发） | Go 的实现不引入额外缓冲 |
| `access_log` | `accesslog` 包 | JSON 格式 |
| `stub_status` | `/metrics` | Prometheus 格式 |

**核心差别**：nginx 用 DSL，本项目用 YAML；nginx 是 C，本项目是 Go，因此**每个请求一个 goroutine**，不需要配置 worker 数。

### 14.2 v0.3 文档与代码对账（9 处）

| # | v0.3 文档说 | 代码实际 | v0.3.1 处理 |
|---|---|---|---|
| 1 | 状态：阶段 1 完成，阶段 2 待开始 | config/router/proxy/accesslog/metrics/server 已实现 | 更新状态为“功能已实现；阶段 7（手工故障验收）待做” |
| 2 | §3 目录列出不存在的文件 | 仓库没有这些文件 | 目录中删除 |
| 3 | §3 目录没有 `internal/server` | 实际有 `internal/server/server.go` | 补进目录结构 |
| 4 | §7.1：`Rewrite` 会先删 `Forwarded` / `X-Forwarded-*` | 只调 `SetXForwarded()`；XFF 追加，`Forwarded` 没动，XFH 被覆盖 | 代码先删 XFF / `Forwarded`，再 `SetXForwarded` |
| 5 | §7.2：`statusRecorder.Write` 置 200 | `Recorder.Write` 没置 200 | 补 `if Status == 0 { Status = 200 }` |
| 6 | §7.2：`statusRecorder` 有 `Flush` | 实际只有 `Unwrap` | 不改代码；Go 1.24 `ResponseController` 沿 `Unwrap` 做 Flush/Hijack；更新文档表述 |
| 7 | §6.2：响应头超时 → 504 | 实际 502 | 用 `net.Error.Timeout()` 判 504 |
| 8 | §9.1：`upstream` 是 `host:port` | 实际是完整 URL | 改记 `u.Host`；拒绝 userinfo |
| 9 | 原验收要求自动化检查全绿 | 仓库没有对应文件 | 从验收清单移除；改为 `go vet` + §11 手工清单 |

### 14.3 v0.4 配置契约提案（不在 v0.3.1 范围）

如果确实需要以下能力，**一次性**升到 v0.4，不要零散加字段：

| 字段/能力 | 用途 | 迁移影响 |
|---|---|---|
| `route.method` + 405 + `Allow` | 按 method 限制路由 | 新增可选字段；旧配置默认任意 method |
| `server.read` | 请求体读取超时 | 新增字段；旧配置需要补 |
| `server.max_body_bytes` | 请求体上限 | 新增字段；默认 10MB |
| `metrics_listen` | 指标独立端口 | 新增字段；默认同 listener |
| `auth` | 按路由认证 | 新增字段；默认 none |
| `rate_limit` | 按路由限流 | 新增字段；默认关闭 |
| `hot_reload` | 热加载开关 | 新增字段；默认 false |

**规则**：每次 schema 变更必须同时更新本文档的版本号、字段说明、校验规则、迁移说明；不允许“代码先加、文档后补”。

### 14.4 开放问题

1. `route.method` / 405：有真实需求吗？没有就留在 v0.4 提案里。
2. 热加载：重启 < 2s 是否真的不可接受？如果否，不做。
3. 认证：是否有非 tailnet 客户端要访问？如果没有，不做。
4. `metrics` 独立端口：容器网络里是否可接受同 listener？如果是，不做。
5. `max_body_bytes` 默认值：10MB 是否够？
6. 编码斜杠（`%2F`）语义：是否有上游依赖它？如果没有，归一化。

### 14.5 文档维护

- 每完成一个阶段，更新 §10 的验收勾选与 §14.2 的对账状态。
- 语义变更必须同时改 `README.md`、本文档、代码注释。
- 本文档不记录密钥、公网 IP、tailnet 真实地址；示例一律用占位符。
