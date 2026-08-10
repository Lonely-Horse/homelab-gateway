# homelab-telemetry

基于 Go 原生标准库实现的高并发、无外部依赖的反向代理监控网关。专为 Homelab 环境设计，提供路径改写（Path Rewriting）、服务分流以及并发安全的 Prometheus 性能指标暴露功能。

## 核心特性

- **零第三方依赖**：仅使用 Go 原生标准库（`net/http`、`net/http/httputil` 等）构建，保证极致的轻量与底层稳定性。
- **通用前缀映射**：支持树状路径的动态剥离与转发，可自动映射子路径（例如将网关 `/api/examprep/submit` 转换为后端的 `/api/submit`）。
- **非侵入式监控拦截**：基于装饰器模式包装 `http.ResponseWriter`，在 I/O 层面无损捕获实际响应状态码与延迟。
- **并发安全指标统计**：采用硬件级 `sync/atomic` 原子指令进行计数，防止高并发下发生数据竞争，提供超高性能。
- **本地安全隔离**：`/metrics` 监控端点内置 IP 过滤，仅允许本地环回地址（`127.0.0.1` / `::1`）访问，避免内网指标泄漏至公网。
- **SRE 规范容器化**：提供多阶段构建（Multi-stage）Dockerfile，最终运行镜像大小低于 **20MB**，并配置 `USER appuser` 实现非特权运行。

## 物理架构与路径规则

网关默认监听本地端口 `:8085`。

| 网关路由 (暴露前缀) | 后置服务目标 (默认) | 代理转换后的实际路径 |
| :--- | :--- | :--- |
| `/api/socratic/` | `http://socratic-tutor:8083` | `/api/v1/socratic/` |
| `/api/examprep/` | `http://study-traker:8082` | `/api/` |
| `/metrics` | 网关本地处理 | 返回 Prometheus 格式指标且仅限内网抓取 |

## 环境变量配置

支持通过环境变量实现开发环境与容器网络的无缝降级切换：

- `SOCRATIC_TUTOR_URL`：指定 Socratic Tutor 后端的访问地址（默认：`http://127.0.0.1:8083`）。
- `EXAM_PREP_URL`：指定 Exam Prep 后端的访问地址（默认：`http://127.0.0.1:8082`）。

## 快速开始

### 1. 本地直接运行

```bash
# 开启服务（默认监听 :8085）
go run main.go metrics.go model.go exporter.go
```

### 2. 容器化部署

使用配套的 `docker-compose.yaml` 一键启动：

```bash
# 进入 docker 目录
cd docker

# 启动网关容器
docker compose up -d
```

### 3. 测试指标获取

```bash
# 本地获取网关监控指标
curl http://127.0.0.1:8085/metrics
```
