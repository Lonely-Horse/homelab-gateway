package accesslog

import (
	"net/http"
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
	Byte   int64
	errMsg string
}

func (r *Recorder) WriteHeader(code int) {

}

func (r *Recorder) Write(b []byte) (int, error)

func (r *Recorder) Uwrap() http.ResponseWriter

func (r *Recorder) SetError(msg string)

func (r *Recorder) FinalStatus() int

var mu sync.Mutex

func Write(e Entry)
