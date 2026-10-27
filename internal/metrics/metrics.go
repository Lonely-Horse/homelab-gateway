package metrics

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type key struct {
	route string
	label string
}

type Metrics struct {
	mu       sync.Mutex
	requests map[key]uint64
	errs     map[key]uint64
	durSum   map[string]float64
	durCount map[string]uint64
	inflight map[string]int64
}

func New() *Metrics {
	return &Metrics{
		requests: make(map[key]uint64),
		errs:     make(map[key]uint64),
		durSum:   make(map[string]float64),
		durCount: make(map[string]uint64),
		inflight: make(map[string]int64),
	}
}

func (m *Metrics) IncInflight(route string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.inflight[route]++
}

func (m *Metrics) DecInflight(route string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.inflight[route]--
}

func (m *Metrics) Observe(route string, code int, dur time.Duration, errReason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests[key{route: route, label: strconv.Itoa(code)}]++
	m.durSum[route] += dur.Seconds()
	m.durCount[route]++
	if errReason != "" {
		m.errs[key{route: route, label: errReason}]++
	}
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m.mu.Lock()

	var b bytes.Buffer

	b.WriteString("# HELP gateway_requests_total 按路由和状态码统计的请求数量\n")
	b.WriteString("# TYPE gateway_requests_total counter\n")
	keys := []key{}
	for key := range m.requests {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].label < keys[j].label
	})
	for _, key := range keys {
		fmt.Fprintf(&b, "gateway_requests_total{code=\"%s\",route=\"%s\"} %d\n", key.label, key.route, m.requests[key])
	}

	b.WriteString("# HELP gateway_duration_seconds_sum 总耗时(s) \n")
	b.WriteString("# TYPE gateway_duration_seconds_sum counter\n")
	routes := make([]string, 0, len(m.durSum))
	for route := range m.durSum {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		fmt.Fprintf(&b, "gateway_duration_seconds_sum{route=\"%s\"} %s\n", route, strconv.FormatFloat(m.durSum[route], 'g', -1, 64))
	}

	b.WriteString("# HELP gateway_duration_seconds_count 请求总次数\n")
	b.WriteString("# TYPE gateway_duration_seconds_count counter\n")
	for _, route := range routes {
		fmt.Fprintf(&b, "gateway_duration_seconds_count{route=\"%s\"} %d\n", route, m.durCount[route])
	}

	b.WriteString("# HELP gateway_upstream_errors_total 上游错误总数\n")
	b.WriteString("# TYPE gateway_upstream_errors_total counter\n")
	keys = make([]key, 0, len(m.errs))
	for key := range m.errs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].label < keys[j].label
	})
	for _, key := range keys {
		fmt.Fprintf(&b, "gateway_upstream_errors_total{reason=\"%s\",route=\"%s\"} %d\n", key.label, key.route, m.errs[key])
	}

	b.WriteString("# HELP gateway_inflight 当前在途请求数\n")
	b.WriteString("# TYPE gateway_inflight gauge\n")
	infRoutes := make([]string, 0, len(m.inflight))
	for route := range m.inflight {
		infRoutes = append(infRoutes, route)
	}
	sort.Strings(infRoutes)
	for _, route := range infRoutes {
		fmt.Fprintf(&b, "gateway_inflight{route=\"%s\"} %d\n", route, m.inflight[route])
	}

	m.mu.Unlock()
	w.Write(b.Bytes())
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
