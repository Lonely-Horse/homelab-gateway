package accesslog

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
)

type Entry struct {
	TS       string `json:"ts"`
	Route    string `json:"route"`
	Method   string `json:"method"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	DurMS    int64  `json:"dur_ms"`
	Bytes    int64  `json:"bytes"`
	Upstream string `json:"upstream"`
	Err      string `json:"err,omitempty"`
}

type Recorder struct {
	http.ResponseWriter
	Status int
	Bytes  int64
	ErrMsg string
}

func (r *Recorder) WriteHeader(code int) {
	if r.Status != 0 {
		return
	}
	r.Status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *Recorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.Bytes += int64(n)

	if err != nil {
		return n, err
	}

	return n, nil
}

func (r *Recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *Recorder) SetError(msg string) {
	r.ErrMsg = msg
}

func (r *Recorder) FinalStatus() int {
	if r.Status == 0 {
		return http.StatusOK
	}
	return r.Status
}

var mu sync.Mutex
var head = log.New(os.Stderr, "[accessologs] ", 0)

func Write(e Entry) {
	mu.Lock()
	defer mu.Unlock()
	data, err := json.Marshal(e)
	if err != nil {
		head.Printf("marshal failed: err=%v", err)
		return
	}
	data = append(data, '\n')

	n, err := os.Stdout.Write(data)
	if err != nil || n != len(data) {
		head.Printf("The write failed: n =%d want=%d err=%v", n, len(data), err)
		return
	}

}
