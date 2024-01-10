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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"connectrpc.com/connect/internal/assert"
	pingv1 "connectrpc.com/connect/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/internal/gen/connect/ping/v1/pingv1connect"
	"connectrpc.com/connect/internal/memhttp/memhttptest"
	"github.com/google/go-cmp/cmp"
)

func TestObservability(t *testing.T) {
	const testHeader = "Test"
	type result struct {
		name   string
		spec   connect.Spec
		events []Event
	}
	var (
		resultsMutex  sync.Mutex
		clientResults = make(map[string]*result)
		serverResults = make(map[string]*result)
	)
	observability := func(ctx context.Context, spec connect.Spec) (context.Context, Observer2) {
		fmt.Println("OBSERVER CALLED:", spec.IsClient)
		result := &result{
			spec: spec,
		}
		return ctx, Observer2{
			Request: func(e EventRequest) {
				fmt.Println("REQUEST EVENT:", e)
				testname := e.Header.Get(testHeader)
				resultsMutex.Lock()
				defer resultsMutex.Unlock()
				if spec.IsClient {
					clientResults[testname] = result
				} else {
					serverResults[testname] = result
				}
				result.events = append(result.events, e)
			},
			Response: func(e EventResponse) {
				fmt.Println("RESPONSE EVENT:", e)
				result.events = append(result.events, e)
			},
			End: func(e EventEnd) {
				fmt.Println("END EVENT:", e)
				result.events = append(result.events, e)
			},
		}
	}

	middleware := NewMiddleware(observability)

	mux := http.NewServeMux()
	mux.Handle(pingv1connect.NewPingServiceHandler(
		pingServer{},
		connect.WithInterceptors(middleware),
	))
	handler := middleware.WrapHandler(mux)
	server := memhttptest.NewServer(t, handler)

	transport := middleware.WrapTransport(server.Transport())
	client := &http.Client{Transport: transport}

	ctx := context.Background()
	pinger := pingv1connect.NewPingServiceClient(
		client,
		server.URL(),
		connect.WithInterceptors(middleware),
		connect.WithAcceptCompression("gzip", nil, nil),
	)
	checkEvents := func(t *testing.T, events []Event, want []Event) {
		t.Helper()
		if !assert.Equal(t, len(events), len(want)) {
			return
		}
		t.Log("len(events)", len(events))
		if diff := cmp.Diff(events, want); diff != "" {
			t.Errorf("events mismatch (-want +got):\n%s", diff)
		}
	}
	t.Run("unary", func(t *testing.T) {
		text := "hello, world!"
		t.Log("len(text)", len(text))
		req := connect.NewRequest(&pingv1.PingRequest{Text: text})
		req.Header().Set(testHeader, t.Name())
		rsp, err := pinger.Ping(ctx, req)
		assert.Nil(t, err)
		assert.Equal(t, rsp.Msg.GetText(), text)

		resultsMutex.Lock()
		defer resultsMutex.Unlock()
		clientResult := clientResults[t.Name()]
		serverResult := serverResults[t.Name()]
		checkEvents(t, clientResult.events, []Event{
			EventRequest{
				Header: req.Header(),
				Peer: connect.Peer{
					Addr:     "1.2.3.4",
					Protocol: connect.ProtocolConnect,
				},
			},
			EventResponse{
				Header: rsp.Header(),
			},
			EventEnd{
				Err:     nil,
				Trailer: rsp.Trailer(),
				// TODO: fake clock.
				Duration:             clientResult.events[2].(EventEnd).Duration,
				SentTotalBytes:       15,
				ReceivedTotalBytes:   15,
				SentMessageCount:     1,
				ReceivedMessageCount: 1,
			},
		})
		checkEvents(t, serverResult.events, []Event{
			EventRequest{
				Header: req.Header(),
				Peer: connect.Peer{
					Addr:     "pipe",
					Protocol: connect.ProtocolConnect,
					Query:    url.Values{},
				},
			},
			EventResponse{
				Header: rsp.Header(),
			},
			EventEnd{
				Err:                  nil,
				Trailer:              rsp.Trailer(),
				Duration:             serverResult.events[2].(EventEnd).Duration,
				SentTotalBytes:       15,
				ReceivedTotalBytes:   15,
				SentMessageCount:     1,
				ReceivedMessageCount: 1,
			},
		})
	})
	t.Run("clientStream", func(t *testing.T) {

	})
	t.Run("serverStream", func(t *testing.T) {

	})
	t.Run("bidiStream", func(t *testing.T) {

	})
}

type pingServer struct {
	pingv1connect.UnimplementedPingServiceHandler

	checkMetadata bool
}

func (p pingServer) Ping(ctx context.Context, request *connect.Request[pingv1.PingRequest]) (*connect.Response[pingv1.PingResponse], error) {
	response := connect.NewResponse(
		&pingv1.PingResponse{
			Number: request.Msg.GetNumber(),
			Text:   request.Msg.GetText(),
		},
	)
	return response, nil
}

func (p pingServer) Sum(
	ctx context.Context,
	stream *connect.ClientStream[pingv1.SumRequest],
) (*connect.Response[pingv1.SumResponse], error) {
	var sum int64
	for stream.Receive() {
		sum += stream.Msg().GetNumber()
	}
	if stream.Err() != nil {
		return nil, stream.Err()
	}
	response := connect.NewResponse(&pingv1.SumResponse{Sum: sum})
	return response, nil
}

func (p pingServer) CountUp(
	ctx context.Context,
	request *connect.Request[pingv1.CountUpRequest],
	stream *connect.ServerStream[pingv1.CountUpResponse],
) error {
	if request.Msg.GetNumber() <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"number must be positive: got %v",
			request.Msg.GetNumber(),
		))
	}
	for i := int64(1); i <= request.Msg.GetNumber(); i++ {
		if err := stream.Send(&pingv1.CountUpResponse{Number: i}); err != nil {
			return err
		}
	}
	return nil
}

func (p pingServer) CumSum(
	ctx context.Context,
	stream *connect.BidiStream[pingv1.CumSumRequest, pingv1.CumSumResponse],
) error {
	var sum int64
	for {
		msg, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		sum += msg.GetNumber()
		if err := stream.Send(&pingv1.CumSumResponse{Sum: sum}); err != nil {
			return err
		}
	}
}
