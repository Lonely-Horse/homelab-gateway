# homelab-gateway 设计文档

| 项 | 内容 |
|---|---|
| 版本 | v0.3（配置契约冻结；撤销默认值 / WARN 通道） |
| 日期 | 2026-10-24 |
| 状态 | 阶段 1 已完成；阶段 2（router）待开始 |
| 语言 | Go 1.24（标准库）+ `go.yaml.in/yaml/v3`（唯一外部依赖） |
| 代码位置 | 独立仓库 `homelab-gateway`，模块名 `homelab-gateway` |

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

写进代码注释，防止范围蔓延。

| 不做 | 理由 |
|---|---|
| TLS 终止 | VPS 的 Nginx 已经在做 |
| 鉴权 / 登录 | Tailscale 已经在做（网络层隔离） |
| 管理界面（Web UI） | 只有一个用户，配置文件就是管理界面 |
| 配置热加载 | 重启耗时 < 2s，v0.1 不做 |
| 负载均衡 | 每个上游只有单实例 |
| 限流 / 熔断 | v0.2 再评估 |
| 服务发现 | 路由表是静态 YAML |
| 缓存 | 代理不缓存响应体 |

---

## 2. 系统边界

### 2.1 网络位置

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

### 2.2 硬性约束

| 约束 | 原因 |
|---|---|
| `listen` **允许 `0.0.0.0`**（容器形态必需），但端口发布只能绑定 tailnet 接口 | 网关能访问所有后端，暴露到公网等于把所有服务合并成一个跳板 |
| `upstream` **不得为公网地址** | 防止被当成开放代理滥用 |
| 转发目标**不得指向自身监听地址** | 防止配置错误形成环路 |
| 每个请求必须有 `context` 超时 | 一个挂住的上游会占住 goroutine 和连接 |

### 2.3 信任边界

- 网关**信任**来自 Tailscale 的请求（网络层已隔离）
- 网关**不信任**任何请求头（`X-Forwarded-For` 等由网关自己重写）
- 网关**不解析**请求体（纯转发，不做业务逻辑）

---

## 3. 目录结构

```
homelab-gateway/
├── go.mod
├── gateway.yaml                 # 配置（唯一真相）
├── cmd/
│   └── gateway/
│       └── main.go              # 入口：装配 + 启动（目标 < 100 行）
└── internal/
    ├── config/
    │   ├── config.go            # 读取 + 校验
    │   └── config_test.go
    ├── router/
    │   ├── router.go            # host / path_prefix 匹配
    │   └── router_test.go       # 表驱动测试（重点）
    ├── proxy/
    │   └── proxy.go             # ReverseProxy 构造 + 转发
    ├── accesslog/
    │   └── log.go               # 一行一条 JSON
    └── metrics/
        └── metrics.go           # 手写 Prometheus 文本格式
```

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
    health: "/healthz"            # 保留字段，v0.1 不使用

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
| `routes[].name` | string | ✅ | 唯一，用作指标和日志的标签 |
| `routes[].host` | string | ❌ | 精确匹配 `r.Host`，为空表示不限制 |
| `routes[].path_prefix` | string | ❌ | 前缀匹配，为空表示不限制 |
| `routes[].upstream` | string | ✅ | 完整 URL（含 scheme）；host 为 IP 字面量时必须在内网段，主机名不做加载期校验 |
| `routes[].strip_prefix` | bool | ❌ | 转发前是否剥掉 `path_prefix` |
| `routes[].health` | string | ❌ | 保留字段：v0.1 解析但**完全不使用**（不轮询、不影响转发），不要指望它生效 |

### 4.3 校验规则（加载时全部检查，失败即退出）

1. `listen` 不为空，可 `net.SplitHostPort`，端口在 1–65535（允许 `0.0.0.0`）
2. `routes` 非空
3. 每个 `route.name` 唯一
4. 每个 `route.upstream` 能被 `url.Parse` 且 scheme 为 `http`/`https`，host 非空
5. `upstream` 的 host 为 IP 字面量时，必须在内网段（10/8、172.16/12、192.168/16、127/8、100.64/10、fc00::/7），否则报错；host 为主机名时不做加载期校验（解析失败、连不上都留到运行期，表现为 502）
6. `strip_prefix` 为 true 时，`path_prefix` 不能为空
7. 六条超时都必须 `> 0`（配置里缺省即为 0，等同于写 0，一并报错）
8. `route.upstream` 不得指向网关自身监听地址（同端口 + 本机地址）：
   - 端口相同（`upstream` 未写端口时按 scheme 取默认：`http`=80、`https`=443）
   - 且 host 为以下任一：回环地址（`127.0.0.0/8`、`::1`）、通配地址（`0.0.0.0`、`::`）、`localhost`（大小写不敏感）、与 `listen` 的 host 字面相同
   - 判定失败时报 `routes[i].upstream`

**校验失败的输出必须包含具体行号和字段名**，不能只说 "invalid config"。

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

// ── metrics ───────────────────────────────────
type Metrics struct {
    mu     sync.Mutex
    counts map[metricKey]uint64   // requests_total / errors_total
    sums   map[string]float64     // duration_seconds_sum
    nums   map[string]uint64      // duration_seconds_count
    inflight map[string]int64     // gauge
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

---

## 6. 请求生命周期

### 6.1 时序

```
conn accepted
  │
  ▼
http.Server（ReadHeaderTimeout / IdleTimeout）
  │
  ▼
rootHandler
  │
  ├─ 1. rec := &statusRecorder{ResponseWriter: w}   // status 零值 = 0，表示"还没写过"
  ├─ 2. start := time.Now()
  ├─ 3. defer func() {
  │         dur := time.Since(start)
  │         log.Write(Entry{...rec.status...})
  │         metrics.Observe(route, rec.status, dur, bytes)
  │     }()
  ├─ 4. route, ok := router.Match(r)
  │         if !ok { http.NotFound(rec, r); return }
  ├─ 5. metrics.IncInflight(route.Name); defer metrics.DecInflight(...)
  ├─ 6. proxyByName[route.Name].ServeHTTP(rec, r)
  │
  ▼
ReverseProxy
  │
  ├─ Rewrite: SetURL(target) + SetXForwarded() + 可选剥前缀
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

**`ErrorHandler` 必须自定义**，默认实现只返回 502，不记录原因。

```go
rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
    // 根据 err 类型区分 502 / 504
    // 写入 rec.err 供 defer 记录
    http.Error(w, "bad gateway", http.StatusBadGateway)
}
```

---

## 7. 关键设计决策

### 7.1 每个 route 一个 ReverseProxy 实例

**问题**：全局单实例时，改写函数无法知道"当前请求属于哪个上游"。

**错误做法**：

```go
var globalProxy = &httputil.ReverseProxy{
    Rewrite: func(pr *httputil.ProxyRequest) {
        // 只能从 context 挖，或从全局 map 里按 Host 反查
        // → 并发下极易出错
    },
}
```

**正确做法**（用 `Rewrite`，理由见下方实测）：

```go
func New(route *Route, t Timeouts, shared *http.Transport) *Proxy {
    target, _ := url.Parse(route.Upstream)
    prefix := route.PathPrefix

    rp := &httputil.ReverseProxy{
        Rewrite: func(pr *httputil.ProxyRequest) {
            pr.SetURL(target)     // scheme / host / Host 头，一次搞定
            pr.SetXForwarded()    // 重建 X-Forwarded-For / -Host / -Proto

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

- `Rewrite` 是并发调用的，闭包捕获自己的 `target` 和 `prefix`，无共享状态
- `Transport` 可以共享（它是并发安全的），这样连接池复用
- **不用 `Director`**：实测在 `Director` 里 `Set("X-Forwarded-For", ip)`，上游收到的是 `"127.0.0.1, 127.0.0.1"`——库在 `Director` 返回之后又追加了一次真实 IP。`Rewrite` 路径会先删掉入站的 `Forwarded` / `X-Forwarded-*`，再由 `SetXForwarded` 重建，客户端伪造值进不来，`X-Forwarded-Host` 也自动填好

### 7.2 statusRecorder 会杀掉 SSE（必须透传 Flusher）

**问题**：为了记录状态码，需要包装 `ResponseWriter`。但包装后：

```go
w.(http.Flusher)   // ← 断言失败
```

因为接口断言看的是"这个类型有没有这个方法"，不是"它内嵌的类型有没有"。

**后果**：任何经过网关的 SSE 都会变成"卡住然后一次性到达"。

**修法**：

```go
type statusRecorder struct {
    http.ResponseWriter
    status int
    bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
    if r.status != 0 {
        return
    }
    r.status = code
    r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
    if r.status == 0 {
        r.status = http.StatusOK
    }
    n, err := r.ResponseWriter.Write(b)
    r.bytes += int64(n)
    return n, err
}

// 必须手动透传 Flush
func (r *statusRecorder) Flush() {
    if f, ok := r.ResponseWriter.(http.Flusher); ok {
        f.Flush()
    }
}

// Go 1.24 的更省事做法：给包装器实现 Unwrap，ResponseController（ReverseProxy 内部用它
// 做 Flush / Hijack）会自动沿 Unwrap 链找到下层的真实 ResponseWriter。
func (r *statusRecorder) Unwrap() http.ResponseWriter {
    return r.ResponseWriter
}
```

**通用规则**：包装 `ResponseWriter` 时，优先实现 `Unwrap()`，让 `http.ResponseController` 帮你找到下层的 `Flush` / `Hijack`；只有当调用方**直接做接口断言**（`w.(http.Flusher)`）时，才需要逐个显式透传。

### 7.3 超时必须由内向外递增

```
客户端 ──(A)──> 网关 ──(B)──> 上游
```

**必须满足 `A > B`。**

A 侧三项来自配置 `server.*`，B 侧两项来自 `timeouts.*`（均见 §4.2）。

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

### 7.4 为什么手写 /metrics，不引入 client_golang

| 理由 | 说明 |
|---|---|
| 练习目的 | 手写一遍 exposition format，才知道库在帮你做什么 |
| 依赖最小化 | 标准库足够（`fmt.Fprintf` + `sort`） |
| 可控 | 标签基数由自己控制，不会被库的默认行为坑 |

**代价**：需要自己处理转义、排序（Prometheus 不要求排序，但输出稳定便于测试）、并发安全。

**格式规范**：

```
# HELP gateway_requests_total 按路由和状态码统计的请求数
# TYPE gateway_requests_total counter
gateway_requests_total{route="blog",code="200"} 42
gateway_requests_total{route="blog",code="502"} 3
```

**三条规则**：

1. `# HELP` 和 `# TYPE` 是可选元数据，但强烈建议写
2. 样本格式：`name{label="value",...} value`
3. 标签值中的 `\`、`"`、`\n` 必须转义为 `\\`、`\"`、`\n`

**Content-Type**：`text/plain; version=0.0.4; charset=utf-8`

---

## 8. 指标规范

### 8.1 指标清单（v0.1，共 5 个）

| 指标名 | 类型 | 标签 | 含义 |
|---|---|---|---|
| `gateway_requests_total` | counter | `route`, `code` | 请求总数 |
| `gateway_duration_seconds_sum` | counter | `route` | 总耗时 |
| `gateway_duration_seconds_count` | counter | `route` | 请求数（配合 sum 算平均） |
| `gateway_upstream_errors_total` | counter | `route`, `reason` | 上游错误（`dial`/`timeout`/`5xx`） |
| `gateway_inflight` | gauge | `route` | 当前在途请求数 |

### 8.2 基数控制（重要）

**标签值必须来自有限集合。**

| 标签 | 安全吗 | 说明 |
|---|---|---|
| `route` | ✅ 安全 | 来自配置，数量固定 |
| `code` | ✅ 安全 | HTTP 状态码，最多几十个 |
| `reason` | ✅ 安全 | 枚举值（dial/timeout/5xx） |
| `path` | ❌ **禁止** | 用户可控，会导致指标爆炸 |
| `user_id` | ❌ **禁止** | 同上 |

**判据**：标签值的可能取值必须是"你能枚举出来的"。任何一个不可枚举的值，都会让 Prometheus 的内存线性增长。

### 8.3 输出示例

```
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
| `upstream` | string | 实际转发的 `host:port` |
| `err` | string | 错误原因，成功时省略 |

### 9.2 示例

```json
{"ts":"2026-10-22T10:00:00Z","route":"blog","method":"GET","path":"/posts","status":200,"dur_ms":12,"bytes":5150,"upstream":"127.0.0.1:8080"}
{"ts":"2026-10-22T10:00:05Z","route":"blog","method":"GET","path":"/admin","status":502,"dur_ms":3001,"bytes":21,"upstream":"127.0.0.1:8080","err":"dial tcp 127.0.0.1:8080: connect: connection refused"}
```

### 9.3 输出目标

- stdout（由 systemd / docker logs 收集）
- 不写文件（避免磁盘打满）

---

## 10. 实现顺序与验收

每个阶段都必须独立可运行，不允许"全部写完再跑"。

### 阶段 1：配置加载（半天）

- 写 `config.go`：结构体 + `Load(path)` + `Validate()`
- 写 `config_test.go`：8 条校验规则各一个用例
- `main.go` 只做一件事：加载配置，把路由表打印到 stdout

**验收**：配置写错时，报错信息包含字段名。

### 阶段 2：路由匹配（半天）

- 写 `router.go`：`Match(req) (*Route, bool)`
- 写 `router_test.go`：**表驱动，至少 8 个用例**

| 用例 | 期望 |
|---|---|
| host 精确匹配 | 命中 |
| host 不匹配，path_prefix 匹配 | 命中 |
| 两者都为空 | 命中（兜底路由） |
| 两条路由都匹配 | 按优先级选一条 |
| 空 path | 不 panic |
| 带 query 的 path | 只按 Path 匹配 |

**验收**：`go test ./internal/router -v` 全绿。

### 阶段 3：单路由转发（1 天）

- 写 `proxy.go`：构造 `ReverseProxy`
- 根 handler 接入 `router.Match` + `proxy.ServeHTTP`

**验收**：`curl` 通到上游，返回正确内容。

### 阶段 4：多路由 + StripPrefix（半天）

**验收**：

```
curl -H "Host: blog.example.com" http://gw:8080/posts   → 打到 blog
curl http://gw:8080/grafana/api/health                  → 打到 grafana，且上游收到 /api/health
```

### 阶段 5：日志 + statusRecorder（半天）

- 实现 `statusRecorder`（含 `Flush`）
- 实现 `accesslog.Entry`

**验收**：日志里的 `status` 与 `curl -i` 看到的一致。

### 阶段 6：指标（1 天）

- 实现 `Metrics`（5 个指标）
- 挂 `/metrics`

**验收**：`curl /metrics` 输出格式能被 `promtool check metrics` 通过（或人工对照格式规范）。

### 阶段 7：故障测试（半天）

| 测试 | 期望 |
|---|---|
| 停掉上游 | 502，日志 `err` 含 `connection refused` |
| 上游 sleep 20s | 504，`dur_ms` ≈ 10000 |
| 并发 200 请求 | `gateway_inflight` 峰值 > 0，之后归零 |
| 发 10MB body | 能正常转发（或按 v0.2 加限制） |
| panic 注入 | 返回 500，进程不退出 |

**验收**：五条全部通过，日志和指标都对得上。

---

## 11. 最终验收清单（v0.1 完成标准）

- [ ] 加一个上游 = 改 `gateway.yaml` + 重启，**不改一行 Go 代码**
- [ ] `curl /metrics` 能看到 5 个指标，格式符合 exposition format
- [ ] 停掉任一上游 → 502，日志含 route / upstream / err / dur_ms
- [ ] `go vet ./...` 无输出
- [ ] `go test ./... -race` 全绿
- [ ] `CGO_ENABLED=0 go build` 产出单个二进制，< 15MB
- [ ] 服务以非 root 用户运行
- [ ] 监听端口只发布在 tailnet 接口，未暴露到公网网卡

---

## 12. 风险与回滚

| 风险 | 影响 | 缓解 |
|---|---|---|
| 网关崩溃 | **所有服务不可达**（单点） | `systemd Restart=always`；保留 tailnet 直连作为逃生通道 |
| 配置写错 | 启动失败 | 启动时全量校验；失败即退出，不留半可用状态 |
| 指标基数爆炸 | Prometheus OOM | 标签只允许来自配置 |
| 网关成为跳板 | 内网被横向访问 | 端口只发布到 tailnet 接口 + 上游白名单双重约束 |
| `WriteTimeout` 杀掉流式 | SSE 静默失效 | 若代理流式上游，需单独评估 |

**回滚方案**：

1. 保留上一个二进制的副本（`gateway.old`）
2. 部署脚本里切换 + 重启
3. 回滚命令：`mv gateway.old gateway && systemctl restart gateway`
4. **保留 tailnet 直连端口**，作为网关不可用时的应急路径（v0.1 结束前不撤）

---

## 13. 附录：与 nginx 的对照

| nginx | homelab-gateway | 说明 |
|---|---|---|
| `listen 8080;` | `listen:` | — |
| `server_name blog.example.com;` | `routes[].host` | 精确匹配 |
| `location /grafana/` | `routes[].path_prefix` | 前缀匹配 |
| `proxy_pass http://127.0.0.1:3000;` | `routes[].upstream` | — |
| `proxy_set_header X-Forwarded-For` | `Rewrite` 内 `SetXForwarded()` | 先删入站伪造值，再重建；覆盖而非追加 |
| `proxy_read_timeout` | `timeouts.response_header` | — |
| `proxy_buffering off;` | 天然无缓冲（直接转发） | Go 的实现不引入额外缓冲 |
| `access_log` | `accesslog` 包 | JSON 格式 |
| `stub_status` | `/metrics` | Prometheus 格式 |

**核心差别**：nginx 用 DSL，本项目用 YAML；nginx 是 C，本项目是 Go，因此**每个请求一个 goroutine**，不需要配置 worker 数。
