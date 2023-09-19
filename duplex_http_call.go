// Copyright 2021-2023 Buf Technologies, Inc.
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

package connect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// duplexHTTPCall is a full-duplex stream between the client and server. The
// request body is the stream from client to server, and the response body is
// the reverse.
//
// Be warned: we need to use some lesser-known APIs to do this with net/http.
type duplexHTTPCall struct {
	ctx        context.Context
	httpClient HTTPClient
	onRequest  func(*http.Request)
	onResponse func(*http.Response) *Error
	streamType StreamType
	bufferPool *bufferPool

	request       *http.Request
	requestBody   messagePipe
	response      *http.Response
	responseErr   error
	responseReady sync.WaitGroup
	requestSent   bool
}

func (d *duplexHTTPCall) Setup(
	ctx context.Context,
	httpClient HTTPClient,
	url *url.URL,
	spec Spec,
	header http.Header,
	onResponse func(*http.Response) *Error,
	pool *bufferPool,
) {
	// ensure we make a copy of the url before we pass along to the
	// Request. This ensures if a transport out of our control wants
	// to mutate the req.URL, we don't feel the effects of it.
	url = cloneURL(url)

	d.ctx = ctx
	d.httpClient = httpClient
	d.streamType = spec.StreamType
	d.requestBody = messagePipe{
		pool:  pool,
		limit: maxRPCClientBufferSize,
	}
	if done := ctx.Done(); done != nil {
		// Always close the pipe to avoid locking on Reads and allow
		// context errors to propagate.
		//
		// See: https://github.com/golang/go/issues/53362
		go func() {
			<-done
			ctxErr := wrapIfContextError(ctx.Err())
			d.requestBody.CloseWithError(ctxErr)
		}()
	}

	// This is mirroring what http.NewRequestContext did, but
	// using an already parsed url.URL object, rather than a string
	// and parsing it again. This is a bit funny with HTTP/1.1
	// explicitly, but this is logic copied over from
	// NewRequestContext and doesn't effect the actual version
	// being transmitted.
	d.request = (&http.Request{
		Method:     http.MethodPost,
		URL:        url,
		Header:     header,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Body:       &d.requestBody,
		GetBody: func() (io.ReadCloser, error) {
			if (&d.requestBody).Rewind() {
				return &d.requestBody, nil
			}
			return nil, fmt.Errorf("retry limit exceeded: %d", maxRPCClientBufferSize)
		},
		Host:          removeEmptyPort(url.Host),
		ContentLength: -1,
	}).WithContext(ctx)
	d.responseReady.Add(1)
	d.onResponse = onResponse
	d.bufferPool = pool
}

func (d *duplexHTTPCall) isClientStream() bool {
	return d.streamType&StreamTypeClient != 0
}

// Send sends the buffer to the server.
// The buffer may be consumed by this method.
func (d *duplexHTTPCall) Send(buf *bytes.Buffer) error {
	if err := d.checkCtx(); err != nil {
		return err
	}
	payload := messagePayload{
		Data: buf.Bytes(),
	}
	*buf = bytes.Buffer{} // hijack the buffer
	return d.send(payload)
}

// SendEnvelope encodes the envelope and sends it to the server.
// The buffer may be consumed by this method.
func (d *duplexHTTPCall) SendEnvelope(buf *bytes.Buffer, flags uint8) error {
	if err := d.checkCtx(); err != nil {
		return err
	}
	payload := messagePayload{
		Data:       buf.Bytes(),
		Flags:      flags,
		IsEnvelope: true,
	}
	*buf = bytes.Buffer{} // hijack the buffer
	return d.send(payload)
}

func (d *duplexHTTPCall) send(payload messagePayload) error {
	if !d.isClientStream() {
		if d.requestSent {
			return errorf(CodeInternal, "duplicate send")
		}
		d.request.ContentLength = int64(payload.Len())
	}
	d.ensureRequestMade()
	_, err := d.requestBody.Send(payload)
	return err
}

// Close the request body. Callers *must* call CloseWrite before Read when
// using HTTP/1.x.
func (d *duplexHTTPCall) CloseWrite() error {
	if !d.isClientStream() && !d.requestSent {
		d.request.ContentLength = 0
	}
	// Even if Write was never called, we need to make an HTTP request. This
	// ensures that we've sent any headers to the server and that we have an HTTP
	// response to read from.
	d.ensureRequestMade()
	// The user calls CloseWrite to indicate that they're done sending data. It's
	// safe to close the write side of the pipe while net/http is reading from
	// it.
	//
	// Because connect also supports some RPC types over HTTP/1.1, we need to be
	// careful how we expose this method to users. HTTP/1.1 doesn't support
	// bidirectional streaming - the write side of the stream (aka request body)
	// must be closed before we start reading the response or we'll just block
	// forever. To make sure users don't have to worry about this, the generated
	// code for unary, client streaming, and server streaming RPCs must call
	// CloseWrite automatically rather than requiring the user to do it.
	ctxErr := wrapIfContextError(d.ctx.Err())
	d.requestBody.CloseWithError(ctxErr)
	return nil
}

func (d *duplexHTTPCall) Request() *http.Request {
	return d.request
}
func (d *duplexHTTPCall) Response() (*http.Response, error) {
	d.responseReady.Wait()
	return d.response, d.responseErr
}

// Receive reads the response body into the buffer.
func (d *duplexHTTPCall) Receive(buf *bytes.Buffer, readMaxBytes int) error {
	if err := d.checkCtx(); err != nil {
		return err
	}
	response, err := d.Response() //nolint:bodyclose
	if err != nil {
		return err
	}
	if err := readAll(buf, response.Body, readMaxBytes); err != nil {
		return wrapIfRSTError(err)
	}
	return nil
}

// ReceiveEnvelope reads the enveloped message into the buffer and returns the flags.
func (d *duplexHTTPCall) ReceiveEnvelope(buf *bytes.Buffer, readMaxBytes int) (uint8, error) {
	if err := d.checkCtx(); err != nil {
		return 0, err
	}
	response, err := d.Response() //nolint:bodyclose
	if err != nil {
		return 0, err
	}
	flags, rerr := readEnvelope(buf, response.Body, readMaxBytes)
	if rerr != nil {
		return 0, wrapIfRSTError(rerr)
	}
	return flags, nil
}
func (d *duplexHTTPCall) CloseRead() error {
	response, err := d.Response()
	if err != nil {
		return nil //nolint:nilerr
	}
	if _, err := discard(response.Body); err != nil {
		_ = response.Body.Close()
		return wrapIfRSTError(err)
	}
	return wrapIfRSTError(response.Body.Close())
}

// ResponseTrailer returns the response HTTP trailers.
func (d *duplexHTTPCall) ResponseTrailer() http.Header {
	if err := d.BlockUntilResponseReady(); err == nil {
		return d.response.Trailer
	}
	return nil
}

func (d *duplexHTTPCall) BlockUntilResponseReady() error {
	d.responseReady.Wait()
	return d.responseErr
}

func (d *duplexHTTPCall) checkCtx() error {
	select {
	case <-d.ctx.Done():
		return wrapIfContextError(d.request.Context().Err())
	default:
		return nil
	}
}

func (d *duplexHTTPCall) ensureRequestMade() {
	if d.requestSent {
		return
	}
	d.requestSent = true
	// Client request is streaming, so we need to start sending the request
	// before we start writing to the request body. This ensures that we've
	// sent any headers to the server.
	go d.makeRequest()
}

func (d *duplexHTTPCall) makeRequest() {
	// Free the request message buffer when we've received a response.
	defer d.requestBody.Free()
	// This runs concurrently with Write and CloseWrite. Read and CloseRead wait
	// on d.responseReady, so we can't race with them.
	defer d.responseReady.Done()

	// Promote the header Host to the request object.
	if host := d.request.Header.Get(headerHost); len(host) > 0 {
		d.request.Host = host
	}

	if d.onRequest != nil {
		d.onRequest(d.request)
	}
	// Once we send a message to the server, they send a message back and
	// establish the receive side of the stream.
	response, err := d.httpClient.Do(d.request)
	if err != nil {
		err = wrapIfContextError(err)
		err = wrapIfLikelyH2CNotConfiguredError(d.request, err)
		err = wrapIfLikelyWithGRPCNotUsedError(err)
		err = wrapIfRSTError(err)
		if _, ok := asError(err); !ok {
			err = NewError(CodeUnavailable, err)
		}
		d.responseErr = err
		// Close the pipe with the error to unblock the reader.
		d.requestBody.CloseWithError(err)
		return
	}
	d.response = response
	if err := d.onResponse(response); err != nil {
		d.responseErr = err
		return
	}
	if (d.streamType&StreamTypeBidi) == StreamTypeBidi && response.ProtoMajor < 2 {
		// If we somehow dialed an HTTP/1.x server, fail with an explicit message
		// rather than returning a more cryptic error later on.
		d.responseErr = errorf(
			CodeUnimplemented,
			"response from %v is HTTP/%d.%d: bidi streams require at least HTTP/2",
			d.request.URL,
			response.ProtoMajor,
			response.ProtoMinor,
		)
		_ = d.request.Body.Close()
		_ = response.Body.Close()
	}
}

// See: https://cs.opensource.google/go/go/+/refs/tags/go1.20.1:src/net/http/clone.go;l=22-33
func cloneURL(oldURL *url.URL) *url.URL {
	if oldURL == nil {
		return nil
	}
	newURL := new(url.URL)
	*newURL = *oldURL
	if oldURL.User != nil {
		newURL.User = new(url.Userinfo)
		*newURL.User = *oldURL.User
	}
	return newURL
}

// Given a string of the form "host", "host:port", or "[ipv6::address]:port",
// return true if the string includes a port.
func hasPort(s string) bool { return strings.LastIndex(s, ":") > strings.LastIndex(s, "]") }

// removeEmptyPort strips the empty port in ":port" to ""
// as mandated by RFC 3986 Section 6.2.3.
func removeEmptyPort(host string) string {
	if hasPort(host) {
		return strings.TrimSuffix(host, ":")
	}
	return host
}
