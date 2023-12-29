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

package connect

import (
	"context"
	"net/http"
	"sync"
)

// Observer is a function that observes the execution of a request. An Observer
// is called with events that occur during its execution. The Observer may modify
// the context or header metadata to propagate information required by other Observers.
type Observer func(ctx context.Context, event ObserverEvent)

// ObserverEvent is an event that occurs during the execution of an RPC.
type ObserverEvent interface {
	isObserverEvent()
}

// ObserverEventRetry is called when a client retries a request.
type ObserverEventRetry struct{}

func (*ObserverEventRetry) isObserverEvent() {}

// ObserverEventEnd is emitted when the RPC ends.
type ObserverEventEnd struct {
	Err     *Error      // nil if the RPC completed successfully
	Trailer http.Header // Trailer metadata
}

func (*ObserverEventEnd) isObserverEvent() {}

// ObserverEventRequestMessage is emitted when a request message is sent or received.
type ObserverEventRequestMessage struct {
	Size        int    // Size of the message on the wire
	Codec       string // Codec used to encode the message
	Compression string // Compression used to encode the message
}

func (*ObserverEventRequestMessage) isObserverEvent() {}

// ObserverEventResponseMessage is emitted when a response is received.
type ObserverEventResponseMessage struct {
	Size        int    // Size of the message on the wire
	Codec       string // Codec used to encode the message
	Compression string // Compression used to encode the message
}

func (*ObserverEventResponseMessage) isObserverEvent() {}

// ObserverEventResponseHeader is emitted when an RPC header is received. The header
// may be modified to propagate information required by other Observers.
type ObserverEventResponseHeader struct {
	Header http.Header
}

func (*ObserverEventResponseHeader) isObserverEvent() {}

type maybeObserver struct {
	mu       sync.Mutex
	observer Observer
}

func (o *maybeObserver) maybe(ctx context.Context, event ObserverEvent) {
	if o.observer != nil {
		o.mu.Lock()
		o.observer(ctx, event)
		o.mu.Unlock()
	}
}
