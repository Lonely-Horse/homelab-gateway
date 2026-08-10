package main

import "net/http"

type Metrics struct {
	TotalRequests   uint64
	TotalErrors     uint64
	TotalDurationMs uint64
}

type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode int
}
