package main

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
)

func MetricsHandler(w http.ResponseWriter, r *http.Request) {

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 Forbidden"))
		return
	}
	if host != "127.0.0.1" && host != "::1" {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 Forbidden"))
		return
	}

	reqs := atomic.LoadUint64(&GlobalMetrics.TotalRequests)
	errs := atomic.LoadUint64(&GlobalMetrics.TotalErrors)
	durationms := atomic.LoadUint64(&GlobalMetrics.TotalDurationMs)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	//标准写入prometheus格式的request数据
	fmt.Fprintf(w, "# HELP homelab_requests_total Total number of HTTP requests processed.\n")
	fmt.Fprintf(w, "# TYPE homelab_requests_total counter\n")
	fmt.Fprintf(w, "homelab_requests_total %d\n", reqs)

	//标准输入prometheus格式的errors数据
	fmt.Fprintf(w, "# HELP homelab_errors_total Total number of HTTP requests processed.\n")
	fmt.Fprintf(w, "# TYPE homelab_errors_total counter\n")
	fmt.Fprintf(w, "homelab_errors_total %d\n", errs)

	//标准输入prometheus格式的durationms数据
	fmt.Fprintf(w, "# HELP homelab_duration_total Total duration of HTTP requests in millseconds.\n")
	fmt.Fprintf(w, "# TYPE homelab_duration_total counter\n")
	fmt.Fprintf(w, "homelab_duration_total %d\n", durationms)

}
