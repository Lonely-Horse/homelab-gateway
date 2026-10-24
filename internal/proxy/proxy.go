package proxy

import (
	"context"
	"errors"
	"homelab-gateway/internal/config"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type Proxy struct {
	route *config.Route
	rp    *httputil.ReverseProxy
}

type errorSetter interface {
	SetError(msg string)
}

func New(route *config.Route, t config.Timeouts, shared *http.Transport) *Proxy {
	target, _ := url.Parse(route.Upstream)

	p := &Proxy{route: route}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			//该函数直接将请求重置到对应地址
			pr.SetURL(target)
			//将X-Forwarded-For重建
			pr.SetXForwarded()

			if p.route.StripPrefix {
				pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, p.route.PathPrefix)
				if !strings.HasPrefix(pr.Out.URL.Path, "/") {
					pr.Out.URL.Path = "/" + pr.Out.URL.Path
				}
			}
		},
		Transport: shared,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.DeadlineExceeded) {
				http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
			} else {
				http.Error(w, "bad gateway", http.StatusBadGateway)
			}
			s, ok := w.(errorSetter)
			if ok {
				s.SetError(err.Error())
			}

		},
	}

	return p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.rp.ServeHTTP(w, r)
}
