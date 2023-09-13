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
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// flagEnvelopeCompressed indicates that the data is compressed. It has the
// same meaning in the gRPC-Web, gRPC-HTTP2, and Connect protocols.
const flagEnvelopeCompressed = 0b00000001

// messageBuffer is a rewindable buffer that holds a message payload.
// A buffer can be rewound if it hasn't been returned to a bufferPool.
// Calling SetPool before Close will cause the buffer to be returned to the pool.
// Otherwise, Close is a no-op.
type messageBuffer interface {
	io.ReadCloser
	io.WriterTo
	// Rewind is analogous to io.Seeker.Seek(0, io.SeekStart).
	Rewind() bool
	// Len returns the length of the buffer.
	Len() int
	// SetPool sets the bufferPool that the buffer will be returned to on Close.
	SetPool(*bufferPool)
}

// messagePayload is a marshaled message payload.
type messagePayload struct {
	mu     sync.Mutex
	Data   []byte
	offset int
	pool   *bufferPool
}

func (b *messagePayload) Read(data []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readWithLock(data)
}
func (b *messagePayload) readWithLock(data []byte) (n int, err error) {
	n = copy(data, b.Data[b.offset:])
	b.offset += n
	if n == 0 {
		err = io.EOF
	}
	return n, err
}
func (b *messagePayload) WriteTo(dst io.Writer) (n int64, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeToWithLock(dst)
}
func (b *messagePayload) writeToWithLock(dst io.Writer) (n int64, err error) {
	wroteN, err := dst.Write(b.Data[b.offset:])
	b.offset += wroteN
	n += int64(wroteN)
	return n, err
}

func (b *messagePayload) Rewind() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pool != nil {
		return false // can't rewind a pooled buffer
	}
	b.offset = 0
	return b.Data != nil
}
func (b *messagePayload) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.Data)
}
func (b *messagePayload) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pool == nil || b.Data == nil {
		return nil
	}
	b.pool.Put(bytes.NewBuffer(b.Data))
	b.Data = nil
	b.offset = 0
	return nil
}
func (b *messagePayload) SetPool(pool *bufferPool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pool = pool
}

// messageEnvelope is a payload with a 5-byte prefix.
type messageEnvelope struct {
	messagePayload

	Flags        uint8
	prefixOffset int
}

func (e *messageEnvelope) Read(data []byte) (n int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prefixOffset < 5 {
		prefix := makePrefix(e.Flags, len(e.Data))
		n = copy(data, prefix[e.prefixOffset:])
		e.prefixOffset += n
		if e.prefixOffset < 5 {
			return n, nil
		}
	}
	nn, err := e.readWithLock(data[n:])
	return nn + n, err
}
func (e *messageEnvelope) WriteTo(dst io.Writer) (n int64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.prefixOffset < 5 {
		prefix := makePrefix(e.Flags, len(e.Data))
		prefixN, err := dst.Write(prefix[e.prefixOffset:])
		e.prefixOffset += prefixN
		n += int64(prefixN)
		if e.prefixOffset < 5 {
			return n, err
		}
	}
	nn, err := e.writeToWithLock(dst)
	return nn + n, err
}
func (e *messageEnvelope) Rewind() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pool != nil {
		return false // can't rewind a pooled buffer
	}
	e.offset = 0
	e.prefixOffset = 0
	return e.Data != nil

}
func (e *messageEnvelope) Len() int {
	return e.messagePayload.Len() + 5
}

func marshal(dst *bytes.Buffer, message any, codec Codec) *Error {
	if message == nil {
		return nil
	}
	if codec, ok := codec.(marshalAppender); ok {
		// Codec supports MarshalAppend; try to re-use a []byte from the pool.
		raw, err := codec.MarshalAppend(dst.Bytes(), message)
		if err != nil {
			return errorf(CodeInternal, "marshal message: %w", err)
		}
		if cap(raw) > dst.Cap() {
			// The buffer from the pool was too small, so MarshalAppend grew the slice.
			// Pessimistically assume that the too-small buffer is insufficient for the
			// application workload, so there's no point in keeping it in the pool.
			// Instead, replace it with the larger, newly-allocated slice. This
			// allocates, but it's a small, constant-size allocation.
			*dst = *bytes.NewBuffer(raw)
		} else {
			// The buffer from the pool was large enough, MarshalAppend didn't allocate.
			// Copy to the same byte slice is a nop.
			dst.Write(raw[dst.Len():])
		}
		return nil
	}
	// Codec doesn't support MarshalAppend; let Marshal allocate a []byte.
	raw, err := codec.Marshal(message)
	if err != nil {
		return errorf(CodeInternal, "marshal message: %w", err)
	}
	dst.Write(raw)
	return nil
}

func unmarshal(src *bytes.Buffer, message any, codec Codec) *Error {
	if err := codec.Unmarshal(src.Bytes(), message); err != nil {
		return errorf(CodeInvalidArgument, "unmarshal into %T: %w", message, err)
	}
	return nil
}

func read(dst *bytes.Buffer, src io.Reader) (int, error) {
	dst.Grow(bytes.MinRead)
	b := dst.Bytes()[dst.Len():dst.Cap()]
	n, err := src.Read(b)
	_, _ = dst.Write(b[:n]) // noop
	return n, err
}

func readAll(dst *bytes.Buffer, src io.Reader, readMaxBytes int) *Error {
	var totalN int64
	for {
		readN, err := read(dst, src)
		totalN += int64(readN)
		if readMaxBytes > 0 && totalN > int64(readMaxBytes) {
			discardN, err := discard(src)
			if err != nil {
				return errorf(CodeResourceExhausted,
					"message is larger than configured max %d - unable to determine message size: %w",
					readMaxBytes, err)
			}
			return errorf(CodeResourceExhausted,
				"message size %d is larger than configured max %d",
				totalN+discardN, readMaxBytes)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			err = wrapIfContextError(err)
			if writeErr, ok := asError(err); ok {
				return writeErr
			}
			if readMaxBytesErr := asMaxBytesError(err, "read first %d bytes of message", totalN); readMaxBytesErr != nil {
				return readMaxBytesErr
			}
			return errorf(CodeUnknown, "read: %w", err)
		}
	}
}

func readEnvelope(dst *bytes.Buffer, src io.Reader, readMaxBytes int) (uint8, *Error) {
	wrapErr := func(err error) *Error {
		err = wrapIfContextError(err)
		err = wrapIfRSTError(err)
		if err, ok := asError(err); ok {
			return err
		}
		return errorf(CodeInternal, "incomplete envelope: %w", err)
	}
	prefix := [5]byte{}
	if _, err := io.ReadFull(src, prefix[:]); err != nil {
		if maxBytesErr := asMaxBytesError(err, "read 5 byte message prefix"); maxBytesErr != nil {
			// We're reading from an http.MaxBytesHandler, and we've exceeded the read limit.
			return 0, maxBytesErr
		}
		return 0, wrapErr(err)
	}

	size := int(binary.BigEndian.Uint32(prefix[1:5]))
	switch {
	case size < 0:
		return 0, errorf(CodeInvalidArgument, "message size %d overflowed uint32", size)
	case readMaxBytes > 0 && size > readMaxBytes:
		if _, err := discard(src); err != nil {
			return 0, errorf(CodeUnknown, "read enveloped message: %w", err)
		}
		return 0, errorf(CodeResourceExhausted, "message size %d is larger than configured max %d", size, readMaxBytes)
	case size == 0:
		return prefix[0], nil
	}
	// Don't allocate the entire buffer up front to avoid malicious clients.
	// Instead, limit the size of the source to the message size.
	src = io.LimitReader(src, int64(size))
	if readN, err := dst.ReadFrom(src); err != nil {
		if maxBytesErr := asMaxBytesError(err, "read %d byte message", size); maxBytesErr != nil {
			// We're reading from an http.MaxBytesHandler, and we've exceeded the read limit.
			return 0, maxBytesErr
		}
		return 0, wrapErr(err)
	} else if readN != int64(size) {
		err = io.ErrUnexpectedEOF
		return 0, wrapErr(err)
	}
	return prefix[0], nil
}

func writeAll(dst io.Writer, src *bytes.Buffer) *Error {
	if _, err := src.WriteTo(dst); err != nil {
		if writeErr, ok := asError(err); ok {
			return writeErr
		}
		return errorf(CodeInternal, "write message: %w", err)
	}
	return nil
}

func writeEnvelope(dst io.Writer, src *bytes.Buffer, flags uint8) *Error {
	prefix := makePrefix(flags, src.Len())
	if _, err := dst.Write(prefix[:]); err != nil {
		if writeErr, ok := asError(err); ok {
			return writeErr
		}
		return errorf(CodeUnknown, "write envelope: %w", err)
	}
	return writeAll(dst, src)
}

func makePrefix(flags uint8, size int) [5]byte {
	prefix := [5]byte{}
	prefix[0] = flags
	binary.BigEndian.PutUint32(prefix[1:5], uint32(size))
	return prefix
}

func checkSendMaxBytes(length, sendMaxBytes int, isCompressed bool) *Error {
	if sendMaxBytes <= 0 || length <= sendMaxBytes {
		return nil
	}
	tmpl := "message size %d exceeds sendMaxBytes %d"
	if isCompressed {
		tmpl = "compressed message size %d exceeds sendMaxBytes %d"
	}
	return errorf(CodeResourceExhausted, tmpl, length, sendMaxBytes)
}

func newErrInvalidEnvelopeFlags(flags uint8) *Error {
	return errorf(CodeInternal, "protocol error: invalid envelope flags %08b", flags)
}

// ensureEOF always returns io.EOF, unless there are extra bytes in src.
func ensureEOF(src io.Reader) error {
	if n, err := discard(src); err != nil {
		return err
	} else if n > 0 {
		return errorf(CodeInternal, "corrupt response: %d extra bytes after end of stream", n)
	}
	return io.EOF
}
