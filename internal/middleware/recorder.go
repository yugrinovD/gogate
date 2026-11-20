package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Recorder captures response status, bytes and first-byte latency.
type Recorder struct {
	http.ResponseWriter
	status    int
	bytes     int64
	firstByte time.Time
	wrote     bool
}

// NewRecorder wraps a response writer.
func NewRecorder(w http.ResponseWriter) *Recorder {
	return &Recorder{ResponseWriter: w, status: http.StatusOK}
}

func (r *Recorder) WriteHeader(code int) {
	if r.wrote {
		return
	}
	r.status = code
	r.wrote = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *Recorder) Write(p []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	if r.firstByte.IsZero() && len(p) > 0 {
		r.firstByte = time.Now()
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (r *Recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap allows http.ResponseController to reach the underlying writer.
func (r *Recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack supports websocket-like upgrades if an upstream ever needs one.
func (r *Recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
}

// Status returns the response status code.
func (r *Recorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// Bytes returns the number of written body bytes.
func (r *Recorder) Bytes() int64 { return r.bytes }

// FirstByte returns the time of the first body byte, or zero.
func (r *Recorder) FirstByte() time.Time { return r.firstByte }

// WroteHeader reports whether the header was sent.
func (r *Recorder) WroteHeader() bool { return r.wrote }
