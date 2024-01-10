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
	"net/http"
	"time"

	"connectrpc.com/connect"
)

// Observability is a function that returns an Observer for a request. The Observer
// may modify the context or header metadata to propagate information required by
// other Observers.
type Observability func(context.Context, connect.Spec) (context.Context, Observer2)

// Observer is a function that observes the execution of a request. An Observer
// is called with events that occur during its execution. The Observer may modify
// the context or header metadata to propagate information required by other Observers.
type Observer func(event Event)

// Event is an event that occurs during the execution of an RPC.
type Event interface {
	isEvent()
}
type Observer2 struct {
	//Retry func(EventRetry)
	//RequestMessage  func(EventRequestMessage)
	//ResponseMessage func(EventResponseMessage)
	Request  func(EventRequest)
	Response func(EventResponse)
	End      func(EventEnd)
}

// EventRetry is called when a client retries a request.
type EventRetry struct{}

func (EventRetry) isEvent() {}

// EventEnd is emitted when the RPC ends.
type EventEnd struct {
	Err                  error       // nil if the RPC completed successfully
	Trailer              http.Header // Trailer metadata
	Duration             time.Duration
	SentTotalBytes       int64
	ReceivedTotalBytes   int64
	SentMessageCount     int64
	ReceivedMessageCount int64
}

func (EventEnd) isEvent() {}

/*// EventRequestMessage is emitted when a request message is sent or received.
type EventRequestMessage struct {
	Size        int    // Size of the message on the wire
	Codec       string // Codec used to encode the message
	Compression string // Compression used to encode the message
}

func (EventRequestMessage) isEvent() {}

// EventResponseMessage is emitted when a response is received.
type EventResponseMessage struct {
	Size        int    // Size of the message on the wire
	Codec       string // Codec used to encode the message
	Compression string // Compression used to encode the message
}

func (EventResponseMessage) isEvent() {}*/

// EventRequest is emitted when an RPC request is started. The header
// may be modified to propagate information required by other Observers.
type EventRequest struct {
	Header http.Header
	Peer   connect.Peer
}

func (EventRequest) isEvent() {}

// EventResponse is emitted when an RPC response is received. The header
// may be modified to propagate information required by other Observers.
type EventResponse struct {
	Header http.Header
}

func (EventResponse) isEvent() {}
