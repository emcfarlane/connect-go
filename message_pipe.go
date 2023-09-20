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
	"errors"
	"io"
	"sync"
)

const maxRPCClientBufferSize = 256 * 1024 // 256KB

type messagePipe struct {
	ctx  context.Context //nolint:containedctx
	inCh chan messagePayload
	pool *bufferPool

	once sync.Once
	done chan struct{}
	werr error

	mu    sync.Mutex
	msgs  []messagePayload
	index int
	total int64
	limit int64
}

func newMessagePipe(ctx context.Context, pool *bufferPool, limit int64) *messagePipe {
	return &messagePipe{
		ctx:   ctx,
		done:  make(chan struct{}),
		inCh:  make(chan messagePayload),
		pool:  pool,
		limit: limit,
	}
}

func (p *messagePipe) Read(data []byte) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
				if !p.isBuffering() {
					p.freeWithLock()
				}
			}
			if n > 0 || err != nil {
				return n, err
			}
		default: // wait
			select {
			case <-p.ctx.Done():
				if !p.isBuffering() {
					p.freeWithLock()
				}
				return 0, p.ctx.Err()
			case msg := <-p.inCh:
				p.msgs = append(p.msgs, msg)
			case <-p.done:
				if !p.isBuffering() {
					p.freeWithLock()
				}
				return 0, p.werr
			}
		}
	}
}

func (p *messagePipe) Send(payload messagePayload) error {
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case p.inCh <- payload:
		return nil
	case <-p.done:
		if cerr := p.ctx.Err(); cerr != nil {
			return wrapIfContextError(cerr)
		}
		return p.werr
	}
}

func (p *messagePipe) Close() error {
	p.CloseWithError(nil)
	return nil
}
func (p *messagePipe) CloseWithError(err error) {
	if err == nil {
		err = io.EOF
	}
	p.once.Do(func() {
		if p.werr != nil || p.inCh == nil {
			return
		}
		p.werr = err
		close(p.done)
	})
}

// Rewind is analogous to Seek(0, io.SeekStart). On success, it returns true.
// It returns false if the message pipe is not buffering and therefore
// cannot rewind.
func (p *messagePipe) Rewind() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	return true
}

// Free all messages that have been read and disables buffering.
func (p *messagePipe) Free() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.freeWithLock()
}

func (p *messagePipe) freeWithLock() {
	for i := range p.msgs[:p.index] {
		msg := &p.msgs[i]
		if p.pool != nil {
			p.pool.Put(bytes.NewBuffer(msg.Data))
		}
		msg.Data = nil // release reference in slice
	}
	p.msgs = append(p.msgs[:0], p.msgs[p.index:]...) // empty message buffer
	p.index = 0                                      // reset index
	p.limit = 0                                      // disable buffering
}

func (p *messagePipe) isBuffering() bool {
	return p.total < p.limit && p.pool != nil
}

// messagePayload is a rewindable buffer for a message payload.
// If IsEnvelope is true, then the payload has a 5 byte envelope prefix.
// The envelope prefix is a uint8 flags byte followed by a uint32 size.
// Rewind is analogous to io.Reader.Seek(0, io.SeekStart) returning true if the
// buffer is not pooled.
type messagePayload struct {
	offset     int
	Data       []byte
	Flags      uint8
	IsEnvelope bool
}

func (b *messagePayload) Read(data []byte) (n int, err error) {
	prefixSize := 0
	if b.IsEnvelope {
		if b.offset < 5 {
			prefix := makePrefix(b.Flags, len(b.Data))
			n = copy(data, prefix[b.offset:])
			b.offset += n
			if b.offset < 5 {
				return n, nil
			}
			data = data[n:]
		}
		prefixSize = 5
	}
	wroteN := copy(data, b.Data[b.offset-prefixSize:])
	b.offset += wroteN
	n += wroteN
	if n == 0 && b.offset == b.Len() {
		err = io.EOF
	}
	return n, err
}
func (b *messagePayload) WriteTo(dst io.Writer) (n int64, err error) {
	prefixSize := 0
	if b.IsEnvelope {
		if b.offset < 5 {
			prefix := makePrefix(b.Flags, len(b.Data))
			prefixN, err := dst.Write(prefix[b.offset:])
			b.offset += prefixN
			n += int64(prefixN)
			if b.offset < 5 {
				return n, err
			}
		}
		prefixSize = 5
	}
	wroteN, err := dst.Write(b.Data[b.offset-prefixSize:])
	b.offset += wroteN
	n += int64(wroteN)
	return n, err
}

// Rewind is analogous to io.Reader.Seek(0, io.SeekStart) returning true
// if the buffer is not pooled.
func (b *messagePayload) Rewind() bool {
	b.offset = 0
	return b.Data != nil
}

// Len returns the length of the payload.
func (b *messagePayload) Len() int {
	n := len(b.Data)
	if b.IsEnvelope {
		n += 5
	}
	return n
}
