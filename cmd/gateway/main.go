package main

import (
	"flag"
	"homelab-gateway/internal/config"
	"homelab-gateway/internal/proxy"
	"homelab-gateway/internal/router"
	"log"
	"net"
	"net/http"
)

func buildTransport(t config.Timeouts) *http.Transport {
	d := &net.Dialer{Timeout: t.Dial}
	return &http.Transport{
		DialContext:           d.DialContext,
		ResponseHeaderTimeout: t.ResponseHeader,
		IdleConnTimeout:       t.Idle,
	}
}

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfgPath", "/etc/gateway/gateway.yaml", "配置文件路径（yaml）")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	transport := buildTransport(cfg.Timeouts)
	proxies := make(map[string]*proxy.Proxy)
	for _, route := range cfg.Routes {
		proxies[route.Name] = proxy.New(&route, cfg.Timeouts, transport)
	}

	r := router.New(cfg.Routes)
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rt, ok := r.Match(req)
		if !ok {
			http.NotFound(w, req)
			return
		}
		proxies[rt.Name].ServeHTTP(w, req)
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
