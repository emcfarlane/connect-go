// Copyright 2021-2023 The Connect Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package observability

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
)

type Middleware struct {
	observability Observability
}

var _ connect.Interceptor = (*Middleware)(nil)

func NewMiddleware(observability Observability) *Middleware {
	return &Middleware{
		observability: observability,
	}
}

func (m *Middleware) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		s := &subject{
			middleware: m,
			startAt:    time.Now(),
		}
		ctx := request.Context()
		ctx = setSubject(ctx, s)
		request = request.WithContext(ctx)
		request.Body = bodyReader{
			base:  request.Body,
			total: &s.recvTotalBytes,
		}
		wrapped := responseController{
			base: response.(responseWriteFlusher),
			sub:  s,
		}
		next.ServeHTTP(wrapped, request)
		s.end()
	})
}

func (m *Middleware) WrapTransport(next http.RoundTripper) http.RoundTripper {
	return tripper{base: next}
}

func (m *Middleware) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	// TODO
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, observer := m.observability(ctx, req.Spec())
		if req.Spec().IsClient {
			sub := &subject{
				middleware:       m,
				startAt:          time.Now(),
				observer:         observer,
				peer:             req.Peer(),
				sendMessageCount: 1,
			}
			ctx = setSubject(ctx, sub)
			rsp, err := next(ctx, req)
			if err != nil {
				sub.setError(err)
				sub.end()
				return nil, err
			}
			sub.recvMessageCount = 1
			sub.trailer = rsp.Trailer()
			sub.end()
			return rsp, err
		} else {
			sub := getSubject(ctx)
			sub.observer = observer
			sub.peer = req.Peer()
			sub.emitRequest(req.Header())
			sub.recvMessageCount = 1
			rsp, err := next(ctx, req)
			if err != nil {
				sub.setError(err)
				return nil, err
			}
			sub.sendMessageCount = 1
			sub.emitResponse(rsp.Header())
			sub.trailer = rsp.Trailer()
			// End is emitted by the HTTP handler.
			return rsp, err
		}
	}
}

func (m *Middleware) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	// TODO
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		ctx, observer := m.observability(ctx, spec)
		sub := &subject{
			middleware: m,
			observer:   observer,
		}
		ctx = setSubject(ctx, sub)
		conn := next(ctx, spec)
		sub.peer = conn.Peer()
		wrapped := streamingClientConn{
			base: conn,
			sub:  sub,
		}
		return wrapped
	}
}
func (m *Middleware) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, stream connect.StreamingHandlerConn) error {
		sub := getSubject(ctx)
		ctx, observer := m.observability(ctx, stream.Spec())
		sub.observer = observer
		wrapped := streamingHandlerConn{
			base: stream,
			sub:  sub,
		}
		if err := next(ctx, wrapped); err != nil {
			sub.setError(err)
			return err
		}
		return nil
	}
}

type subjectKey struct{}

type subject struct {
	middleware *Middleware
	// TODO: shared fields...
	mu               sync.Mutex
	observer         Observer2
	peer             connect.Peer
	trailer          http.Header
	startAt          time.Time
	recvTotalBytes   int64
	sendTotalBytes   int64
	recvMessageCount int64
	sendMessageCount int64
	err              error
}

func (s *subject) setError(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}
func (s *subject) end() {
	s.mu.Lock()
	if s.observer.End != nil {
		s.observer.End(EventEnd{
			Err:                  s.err,
			Trailer:              s.trailer,
			Duration:             time.Since(s.startAt),
			SentTotalBytes:       s.sendTotalBytes,
			ReceivedTotalBytes:   s.recvTotalBytes,
			SentMessageCount:     s.sendMessageCount,
			ReceivedMessageCount: s.recvMessageCount,
		})
	}
	s.mu.Unlock()
}
func (s *subject) emitRequest(header http.Header) {
	s.mu.Lock()
	if s.observer.Request != nil {
		s.observer.Request(EventRequest{
			Header: header,
			Peer:   s.peer,
		})
	}
	s.mu.Unlock()
}
func (s *subject) emitResponse(header http.Header) {
	s.mu.Lock()
	if s.observer.Response != nil {
		s.observer.Response(EventResponse{
			Header: header,
		})
	}
	s.mu.Unlock()
}

func getSubject(ctx context.Context) *subject {
	return ctx.Value(subjectKey{}).(*subject)
}
func setSubject(ctx context.Context, sub *subject) context.Context {
	return context.WithValue(ctx, subjectKey{}, sub)
}

type bodyReader struct {
	base  io.ReadCloser
	total *int64
}

func (r bodyReader) Read(p []byte) (int, error) {
	n, err := r.base.Read(p)
	fmt.Println("bodyReader.Read", n, err)
	*r.total += int64(n)
	return n, err
}
func (r bodyReader) Close() error {
	return r.base.Close()
}

type responseWriteFlusher interface {
	http.ResponseWriter
	http.Flusher
}

type responseController struct {
	base responseWriteFlusher
	sub  *subject
}

var _ responseWriteFlusher = (*responseController)(nil)

func (r responseController) Header() http.Header {
	return r.base.Header()
}
func (r responseController) Write(p []byte) (int, error) {
	n, err := r.base.Write(p)
	r.sub.sendTotalBytes += int64(n)
	return n, err
}
func (r responseController) WriteHeader(statusCode int) {
	r.base.WriteHeader(statusCode)
}
func (r responseController) Flush() {
	r.base.Flush()
}
func (r responseController) Unwrap() http.ResponseWriter {
	return r.base
}

type tripper struct {
	base http.RoundTripper
}

func (t tripper) RoundTrip(request *http.Request) (*http.Response, error) {
	fmt.Println("METHOD:", request.Method)
	sub := getSubject(request.Context())
	request.Body = bodyReader{
		base:  request.Body,
		total: &sub.sendTotalBytes,
	}
	sub.emitRequest(request.Header)
	// TODO: getBody?
	// Add monitoring to the request body
	response, err := t.base.RoundTrip(request)
	if err != nil {
		sub.setError(err)
		return response, err
	}
	sub.emitResponse(response.Header)
	// Add monitoring to the response body
	response.Body = bodyReader{
		base:  response.Body,
		total: &sub.recvTotalBytes,
	}
	return response, nil
}

type streamingHandlerConn struct {
	base connect.StreamingHandlerConn
	sub  *subject
}

func (c streamingHandlerConn) Spec() connect.Spec {
	return c.base.Spec()
}
func (c streamingHandlerConn) Peer() connect.Peer {
	return c.base.Peer()
}
func (c streamingHandlerConn) RequestHeader() http.Header {
	return c.base.RequestHeader()
}
func (c streamingHandlerConn) ResponseHeader() http.Header {
	return c.base.ResponseHeader()
}
func (c streamingHandlerConn) ResponseTrailer() http.Header {
	return c.base.ResponseTrailer()
}

func (c streamingHandlerConn) Receive(m any) error {
	if err := c.base.Receive(m); err != nil {
		return err
	}
	c.sub.recvMessageCount++
	return nil
}
func (c streamingHandlerConn) Send(m any) error {
	if err := c.base.Send(m); err != nil {
		return err
	}
	c.sub.sendMessageCount++
	return nil
}

// streamingClientConn will call sub.end() when Close() is called or when
// an error is returned from Send() or Receive().
type streamingClientConn struct {
	base connect.StreamingClientConn
	sub  *subject
}

func (s streamingClientConn) Spec() connect.Spec {
	return s.base.Spec()
}
func (s streamingClientConn) Peer() connect.Peer {
	return s.base.Peer()
}
func (s streamingClientConn) Send(msg any) error {
	if err := s.base.Send(msg); err != nil {
		s.sub.setError(err)
		s.sub.end()
		return err
	}
	s.sub.sendMessageCount++
	return nil
}
func (s streamingClientConn) RequestHeader() http.Header {
	return s.base.RequestHeader()
}
func (s streamingClientConn) CloseRequest() error {
	return s.base.CloseRequest()
}
func (s streamingClientConn) Receive(msg any) error {
	if err := s.base.Receive(msg); err != nil {
		s.sub.setError(err)
		s.sub.end()
		return err
	}
	s.sub.recvMessageCount++
	return nil
}
func (s streamingClientConn) ResponseHeader() http.Header {
	return s.base.ResponseHeader()
}
func (s streamingClientConn) ResponseTrailer() http.Header {
	return s.base.ResponseTrailer()
}
func (s streamingClientConn) CloseResponse() error {
	err := s.base.CloseResponse()
	s.sub.trailer = s.base.ResponseTrailer()
	s.sub.end()
	return err
}
