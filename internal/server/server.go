package server

import (
	"fmt"
	"homelab-gateway/internal/accesslog"
	"homelab-gateway/internal/config"
	"homelab-gateway/internal/metrics"
	"homelab-gateway/internal/proxy"
	"homelab-gateway/internal/router"
	"log"
	"net"
	"net/http"
	"time"
)

func buildTransport(t config.Timeouts) *http.Transport {
	d := &net.Dialer{Timeout: t.Dial}
	return &http.Transport{
		DialContext:           d.DialContext,
		ResponseHeaderTimeout: t.ResponseHeader,
		IdleConnTimeout:       t.Idle,
	}
}

func Run(cfg *config.Config) {
	transport := buildTransport(cfg.Timeouts)
	proxies := make(map[string]*proxy.Proxy)
	for _, route := range cfg.Routes {
		proxies[route.Name] = proxy.New(&route, cfg.Timeouts, transport)
	}
	r := router.New(cfg.Routes)

	m := metrics.New()

	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/metrics" {
			m.ServeHTTP(w, req)
			return
		}
		wr := &accesslog.Recorder{ResponseWriter: w}
		start := time.Now()

		var routeName, upstream string
		defer func() {
			v := recover()
			if v != nil {
				if wr.Status == 0 {
					wr.WriteHeader(http.StatusInternalServerError)
				}
				wr.SetError(fmt.Sprintf("panic: %v", v))
			}

			entry := accesslog.Entry{
				TS:       time.Now().UTC().Format(time.RFC3339),
				Route:    routeName,
				Method:   req.Method,
				Path:     req.URL.Path,
				Status:   wr.FinalStatus(),
				DurMS:    time.Since(start).Milliseconds(),
				Bytes:    wr.Bytes,
				Upstream: upstream,
				Err:      wr.ErrMsg,
			}
			accesslog.Write(entry)

			reason := wr.ErrReason
			if reason == "" && wr.FinalStatus() >= 500 {
				reason = "5xx"
			}
			m.Observe(routeName, wr.FinalStatus(), time.Since(start), reason)

			if v == http.ErrAbortHandler {
				panic(v)
			}
		}()
		rt, ok := r.Match(req)
		if !ok {
			wr.SetError("no_route")
			http.NotFound(wr, req)
			return
		}
		routeName, upstream = rt.Name, rt.Upstream
		m.IncInflight(rt.Name)
		defer m.DecInflight(rt.Name)
		proxies[rt.Name].ServeHTTP(wr, req)
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: cfg.Server.ReadHeader,
		WriteTimeout:      cfg.Server.Write,
		IdleTimeout:       cfg.Server.Idle,
	}
	log.Fatal(srv.ListenAndServe())
}
