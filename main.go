package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// 定义对接的服务地址
const (
	SocraticTutorURL = "http://socratic-tutor:8083"
	ExamPrepURL      = "http://study-traker:8082"
)

func getEnv(key, fallback string) string {
	value, exist := os.LookupEnv(key)
	if exist {
		return value
	}
	return fallback
}

func main() {
	//动态获取环境参数
	socraticURL := getEnv("SOCRATIC_TUTOR_URL", "http://127.0.0.1:8083")
	examprepURL := getEnv("EXAM_PREP_URL", "http://127.0.0.1:8082")

	//分别解析对接项目的后端实际url
	socraticTarget, err := url.Parse(socraticURL)
	if err != nil {
		panic(err)
	}

	examPrepTarget, err := url.Parse(examprepURL)
	if err != nil {
		panic(err)
	}

	//初始化反代服务
	socraticProxy := httputil.NewSingleHostReverseProxy(socraticTarget)
	examPreproxy := httputil.NewSingleHostReverseProxy(examPrepTarget)

	originalDirector := examPreproxy.Director

	//定义stuudy-traker的对外路径
	examprepPath := "/api/examprep/"

	examPreproxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = examPrepTarget.Host

		oldProfix := examprepPath
		newProfix := "/api"

		if strings.HasPrefix(req.URL.Path, oldProfix) {
			req.URL.Path = newProfix + req.URL.Path[len(oldProfix):]
		}

		if req.URL.RawPath != "" && strings.HasPrefix(req.URL.RawPath, oldProfix) {
			req.URL.RawPath = newProfix + req.URL.RawPath[len(oldProfix):]
		}
	}

	//定义socratic服务的对外路径
	socraticPath := "/api/socratic/"

	socraticProxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = socraticTarget.Host

		oldProfix := socraticPath
		newProfix := "/api/v1/socratic"

		if strings.HasPrefix(req.URL.Path, oldProfix) {
			req.URL.Path = newProfix + req.URL.Path[len(oldProfix):]
		}

	}

	//建立新的Handler为mux，不使用默认的Handler
	mux := http.NewServeMux()

	//新建路径，分别是指向业务路径和数据记录路径
	mux.HandleFunc("/metrics", MetricsHandler)
	mux.HandleFunc(examprepPath, RecordMetrics(func(w http.ResponseWriter, r *http.Request) {
		examPreproxy.ServeHTTP(w, r)
	}))
	mux.HandleFunc(socraticPath, RecordMetrics(func(w http.ResponseWriter, r *http.Request) {
		socraticProxy.ServeHTTP(w, r)
	}))

	//设计服务的配置要求
	server := &http.Server{
		Addr:              "0.0.0.0:8085",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Println("The server on 0.0.0.0:8085...")

	//开启listen等候请求
	err = server.ListenAndServe()
	if err != nil {
		fmt.Printf("The server have some problem,detail: %s", err)
	}
}
