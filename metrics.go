package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

func (w *metricsResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsResponseWriter) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

var GlobalMetrics Metrics

func RecordMetrics(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		//封装一下w，让他能够合适的保留statuscode在服务端，而不是客户端
		mw := &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		//totalrequests和totalerrors则是计数器，我们看的是数量，而不是具体的数据是什么
		atomic.AddUint64(&GlobalMetrics.TotalRequests, 1)

		//totalduration使用的是累加器，所以需要+的是每次的时间，而不是数量
		defer func() {
			elapsed := time.Since(startTime).Milliseconds()
			atomic.AddUint64(&GlobalMetrics.TotalDurationMs, uint64(elapsed))
			if mw.statusCode >= 400 {
				atomic.AddUint64(&GlobalMetrics.TotalErrors, 1)
			}
		}()

		next(mw, r)
	}
}
