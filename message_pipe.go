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
	"errors"
	"io"
	"sync"
)

const maxRPCClientBufferSize = 256 * 1024 // 256KB

// messagePipe is based on io.Pipe.
// See: https://github.com/golang/go/blob/cb7a091d729eab75ccfdaeba5a0605f05addf422/src/io/pipe.go#L39
// And: https://github.com/golang/go/blob/cb7a091d729eab75ccfdaeba5a0605f05addf422/src/net/net_fake.go#L259
type messagePipe struct {
	mu    sync.Mutex
	wait  sync.Cond
	pool  *bufferPool      // pool to return buffers on Free
	werr  error            // signal errors and EOF on Close
	msgs  []messagePayload // buffered messages
	index int              // index into msgs
	limit int64            // max total bytes to buffer
	total int64            // total bytes read
	busy  bool             // signal the next message
}

func (p *messagePipe) lock() {
	p.mu.Lock()
	if p.wait.L == nil {
		p.wait.L = &p.mu
	}
}
func (p *messagePipe) unlock() {
	p.mu.Unlock()
}

func (p *messagePipe) Read(data []byte) (n int, err error) {
	p.lock()
	defer p.unlock()
	for {
		switch {
		case p.index < len(p.msgs):
			msg := &p.msgs[p.index]
			for n < len(data) && err == nil {
				var nn int
				nn, err = msg.Read(data[n:])
				p.total += int64(nn)
				n += nn
			}
			if errors.Is(err, io.EOF) || msg.offset == msg.Len() {
				err = nil // ignore EOF
				p.index++ // advance to the next message
				p.wait.Broadcast()
			}
			if n > 0 || err != nil {
				return n, err
			}
		case p.werr != nil:
			return 0, p.werr
		default:
			p.wait.Wait()
		}
	}
}

func (p *messagePipe) WriteTo(dst io.Writer) (n int64, err error) {
	p.lock()
	defer p.unlock()
	for {
		switch {
		case p.index < len(p.msgs):
			msg := &p.msgs[p.index]
			nn, err := msg.WriteTo(dst)
			p.total += nn
			n += nn
			if err != nil {
				return n, err
			}
			p.index++ // advance to the next message
			p.wait.Broadcast()
		case p.werr != nil:
			err = p.werr
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return n, err
		default:
			p.wait.Wait()
		}
	}
}

func (p *messagePipe) Send(msg messagePayload) (int, error) {
	p.lock()
	defer p.unlock()
	for p.busy {
		if p.werr != nil {
			return 0, p.werr
		}
		p.wait.Wait()
	}
	if p.werr != nil {
		return 0, p.werr
	}
	current := p.total
	p.busy = true
	p.msgs = append(p.msgs, msg)
	p.wait.Broadcast()
	for {
		switch {
		case p.index >= len(p.msgs):
			if !p.isBuffering() {
				p.freeWithLock() // free messages
			}
			p.busy = false
			p.wait.Broadcast()
			return int(p.total - current), nil
		case p.werr != nil:
			p.freeWithLock() // free itself
			p.busy = false
			p.wait.Broadcast()
			return int(p.total - current), p.werr
		default:
			p.wait.Wait() // here
		}
	}
}

func (p *messagePipe) CloseWithError(err error) {
	if err == nil {
		err = io.EOF
	}
	p.lock()
	defer p.unlock()
	p.werr = err
	p.wait.Broadcast()
}
func (p *messagePipe) Close() error {
	p.CloseWithError(io.EOF)
	return nil
}

// Rewind is analogous to Seek(0, io.SeekStart). On success, it returns true.
// It returns false if the pipe is not buffering.
func (p *messagePipe) Rewind() bool {
	p.lock()
	defer p.unlock()
	if !p.isBuffering() {
		return false
	}
	for i := range p.msgs {
		msg := &p.msgs[i]
		if !msg.Rewind() {
			return false
		}
	}
	p.index = 0
	p.total = 0
	p.wait.Broadcast()
	return true
}

// Free all messages that have been read and disables buffering.
func (p *messagePipe) Free() {
	p.lock()
	p.freeWithLock()
	p.unlock()
}
func (p *messagePipe) freeWithLock() {
	for i := range p.msgs[:p.index] {
		msg := &p.msgs[i]
		if p.pool != nil {
			p.pool.Put(bytes.NewBuffer(msg.Data))
		}
		msg.Data = nil // release data
	}
	p.msgs = append(p.msgs[:0], p.msgs[p.index:]...) // empty message buffer
	p.index = 0                                      // reset index
	p.limit = 0                                      // disable buffering
}
func (p *messagePipe) isBuffering() bool {
	return p.total < p.limit
}
