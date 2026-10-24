package router

import (
	"homelab-gateway/internal/config"
	"net"
	"net/http"
	"strings"
)

type Router struct {
	routes []config.Route
}

func New(routes []config.Route) *Router {
	return &Router{routes: routes}
}

func stripPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	return h
}

func (r *Router) Match(req *http.Request) (*config.Route, bool) {
	host := stripPort(req.Host)
	path := req.URL.Path
	if path == "" {
		path = "/"
	}

	var best *config.Route
	bestScore := -1
	for _, route := range r.routes {
		if route.Host != "" && !strings.EqualFold(route.Host, host) {
			continue
		}
		if route.PathPrefix != "" && !strings.HasPrefix(path, route.PathPrefix) {
			continue
		}

		score := len(route.PathPrefix)
		if route.Host != "" {
			score += 1 << 30
		}
		if score > bestScore {
			best = &route
			bestScore = score
		}
	}

	return best, best != nil
}
