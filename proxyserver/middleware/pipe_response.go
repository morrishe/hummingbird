//  Copyright (c) 2017 Rackspace
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
//  implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package middleware

import (
	"io"
	"net/http"
	"strings"

	"github.com/troubling/hummingbird/common/srv"
	"go.uber.org/zap"
)

type PipeResponseWriter struct {
	w      *io.PipeWriter
	status int
	header http.Header
	ready  chan struct{}
	Logger srv.LowLevelLogger
}

func (w *PipeResponseWriter) Write(stuff []byte) (int, error) {
	written, err := w.w.Write(stuff)
	if err != nil {
		if !strings.Contains(err.Error(), "closed pipe") {
			w.Logger.Error("PipeResponseWriter Write() error", zap.Error(err))
		}
	}
	return written, err
}

func (w *PipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *PipeResponseWriter) WriteHeader(status int) {
	w.status = status
	close(w.ready)
}

func (w *PipeResponseWriter) Close() {
	w.w.Close()
}

func NewPipeResponseWriter(writer *io.PipeWriter, ready chan struct{}, logger srv.LowLevelLogger) *PipeResponseWriter {
	header := make(map[string][]string)
	return &PipeResponseWriter{
		w:      writer,
		header: header,
		ready:  ready,
		Logger: logger,
	}
}

func PipedGet(urlStr string, request *http.Request, source string, auth AuthorizeFunc) (io.ReadCloser, http.Header, int) {
	ctx := GetProxyContext(request)
	subRequest, err := ctx.newSubrequest("GET", urlStr, nil, request, source)
	if err != nil {
		ctx.Logger.Error("getSourceObject GET error", zap.Error(err))
		return nil, nil, 400
	}
	if request.URL.Query().Get("multipart-manifest") == "get" {
		subRequest.URL.RawQuery = "multipart-manifest=get&format=raw"
	}
	CopyItems(subRequest.Header, request.Header)
	// FIXME. Are we going to do X-Newest?
	subRequest.Header.Set("X-Newest", "true")
	subRequest.Header.Del("X-Backend-Storage-Policy-Index")

	if auth != nil {
		GetProxyContext(subRequest).Authorize = auth
	}

	pipeReader, pipeWriter := io.Pipe()
	ready := make(chan struct{})
	writer := NewPipeResponseWriter(pipeWriter, ready, ctx.Logger)
	go func() {
		defer writer.Close()
		ctx.serveHTTPSubrequest(writer, subRequest)
	}()
	<-ready

	return pipeReader, writer.Header(), writer.status
}
