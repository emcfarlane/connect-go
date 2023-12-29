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

package connect_test

import (
	"context"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	"connectrpc.com/connect/internal/assert"
	pingv1 "connectrpc.com/connect/internal/gen/connect/ping/v1"
	"connectrpc.com/connect/internal/gen/connect/ping/v1/pingv1connect"
	"connectrpc.com/connect/internal/memhttp/memhttptest"
)

func TestObservability(t *testing.T) {
	observes := []connect.ObserverEvent{}
	mux := http.NewServeMux()
	mux.Handle(pingv1connect.NewPingServiceHandler(
		pingServer{checkMetadata: true},
		connect.WithObservability(func(ctx context.Context, spec connect.Spec, _ connect.Peer, _ http.Header) (context.Context, connect.Observer) {
			return ctx, func(_ context.Context, event connect.ObserverEvent) {
				t.Log("server event:", event)
				observes = append(observes, event)
			}
		}),
	))
	server := memhttptest.NewServer(t, mux)
	client := pingv1connect.NewPingServiceClient(
		server.Client(),
		server.URL(),
		connect.WithObservability(func(ctx context.Context, spec connect.Spec, _ connect.Peer, _ http.Header) (context.Context, connect.Observer) {
			return ctx, func(_ context.Context, event connect.ObserverEvent) {
				t.Log("client event:", event)
				observes = append(observes, event)
			}
		}),
	)
	t.Run("ping", func(t *testing.T) {
		num := int64(42)
		request := connect.NewRequest(&pingv1.PingRequest{Number: num})
		request.Header().Set(clientHeader, headerValue)
		expect := &pingv1.PingResponse{Number: num}
		response, err := client.Ping(context.Background(), request)
		assert.Nil(t, err)
		assert.Equal(t, response.Msg, expect)
		assert.Equal(t, response.Header().Values(handlerHeader), []string{headerValue})
		assert.Equal(t, response.Trailer().Values(handlerTrailer), []string{trailerValue})
		for _, event := range observes {
			t.Logf("event: %T %v", event, event)
		}
	})
}
