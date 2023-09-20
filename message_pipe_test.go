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
	"fmt"
	"io"
	"testing"
	"time"

	"connectrpc.com/connect/internal/assert"
)

// Test a single read/write pair.
func TestPipe1(t *testing.T) {
	t.Parallel()
	done := make(chan int)
	pipe := newMessagePipe(context.Background(), nil, 0)
	var buf = make([]byte, 64)
	go func() {
		data := []byte("hello, world")
		msg := messagePayload{Data: data}
		err := pipe.Send(msg)
		assert.Nil(t, err)
		done <- 0
	}()
	n, err := pipe.Read(buf)
	assert.Nil(t, err)
	if n != 12 || string(buf[0:12]) != "hello, world" {
		t.Errorf("bad read: got %d %q", n, string(buf[0:n]))
	}
	<-done
	assert.Nil(t, pipe.Close())
	assert.ErrorIs(t, io.EOF, pipe.Send(messagePayload{}))
}

// Test a sequence of read/write pairs.
func TestPipe2(t *testing.T) {
	t.Parallel()
	cBytes := make(chan int)
	pipe := newMessagePipe(context.Background(), nil, 0)
	go func() {
		var buf = make([]byte, 64)
		for {
			rbytes, err := pipe.Read(buf)
			if errors.Is(err, io.EOF) {
				cBytes <- 0
				break
			}
			if err != nil {
				t.Errorf("read: %v", err)
			}
			cBytes <- rbytes
		}
	}()
	var buf = make([]byte, 64)
	for i := 0; i < 5; i++ {
		pbuf := buf[0 : 5+i*10]
		msg := messagePayload{Data: pbuf}
		if err := pipe.Send(msg); err != nil {
			t.Errorf("send: %v", err)
		}
		rbytes := <-cBytes
		wbytes := msg.Len()
		if rbytes != wbytes {
			t.Errorf("wrote %d, read got %d", wbytes, rbytes)
		}
	}
	assert.Nil(t, pipe.Close())
	nn := <-cBytes
	if nn != 0 {
		t.Errorf("final read got %d", nn)
	}
}

// Test a large write that requires multiple reads to satisfy.
func TestPipe3(t *testing.T) {
	t.Parallel()
	type pipeReturn struct {
		n   int
		err error
	}
	cReturns := make(chan pipeReturn)
	pipe := newMessagePipe(context.Background(), nil, 0)
	var wdat = make([]byte, 128)
	for i := 0; i < len(wdat); i++ {
		wdat[i] = byte(i)
	}
	msg := messagePayload{Data: wdat}
	go func() {
		err := pipe.Send(msg)
		pipe.Close()
		cReturns <- pipeReturn{msg.Len(), err}
	}()
	var rdat = make([]byte, 1024)
	tot := 0
	for count := 1; count <= 256; count *= 2 {
		bytesRead, err := pipe.Read(rdat[tot : tot+count])
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("read: %v", err)
		}

		// only final two reads should be short - 1 byte, then 0
		expect := count
		switch count {
		case 128:
			expect = 1
		case 256:
			expect = 0
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read at end: %v", err)
			}
		}
		if bytesRead != expect {
			t.Fatalf("read %d, expected %d, got %d", count, expect, bytesRead)
		}
		tot += bytesRead
	}
	pr := <-cReturns
	if pr.n != 128 || pr.err != nil {
		t.Fatalf("write 128: %d, %v", pr.n, pr.err)
	}
	if tot != 128 {
		t.Fatalf("total read %d != 128", tot)
	}
	for i := 0; i < 128; i++ {
		if rdat[i] != byte(i) {
			t.Fatalf("rdat[%d] = %d", i, rdat[i])
		}
	}
}

// Test read after/before writer close.
func TestPipeReadClose(t *testing.T) {
	t.Parallel()
	type pipeTest struct {
		async          bool
		err            error
		closeWithError bool
	}

	var pipeTests = []pipeTest{
		{true, nil, false},
		{true, nil, true},
		{true, io.ErrShortWrite, true},
		{false, nil, false},
		{false, nil, true},
		{false, io.ErrShortWrite, true},
	}
	delayClose := func(t *testing.T, closer func(err error) error, done chan int, tt pipeTest) {
		t.Helper()
		time.Sleep(1 * time.Millisecond)
		var err error
		if tt.closeWithError {
			err = closer(tt.err)
		} else {
			err = closer(io.EOF)
		}
		assert.Nil(t, err) // delayClose
		done <- 0
	}
	for _, testcase := range pipeTests {
		testcase := testcase
		t.Run(fmt.Sprintf(
			"async=%v err=%v closeWithError=%v",
			testcase.async, testcase.err, testcase.closeWithError,
		), func(t *testing.T) {
			t.Parallel()
			done := make(chan int, 1)
			pipe := newMessagePipe(context.Background(), nil, 0)
			closer := func(err error) error {
				pipe.CloseWithError(err)
				return nil
			}
			if testcase.async {
				go delayClose(t, closer, done, testcase)
			} else {
				delayClose(t, closer, done, testcase)
			}
			var buf = make([]byte, 64)
			rbytes, err := pipe.Read(buf)
			<-done
			want := testcase.err
			if want == nil {
				want = io.EOF
			}
			assert.ErrorIs(t, err, want) // pipe.Read
			assert.Equal(t, rbytes, 0)   // pipe.Read
			assert.Nil(t, pipe.Close())
		})
	}
}

func TestPipeWriteEmpty(t *testing.T) {
	t.Parallel()
	pipe := newMessagePipe(context.Background(), nil, 0)
	go func() {
		msg := messagePayload{Data: []byte{}}
		t.Log("msg len:", msg.Len())
		_ = pipe.Send(msg)
		_ = pipe.Close()
	}()
	var b [2]byte
	_, _ = io.ReadFull(pipe, b[0:2])
	_ = pipe.Close()
}

func TestPipeBuffer(t *testing.T) {
	t.Parallel()
	t.Run("SingleWrite", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := newBufferPool()
		pipe := newMessagePipe(ctx, pool, maxRPCClientBufferSize)

		go func() {
			msg := messagePayload{Data: []byte("hello")}
			err := pipe.Send(msg)
			assert.Nil(t, err)
			_ = pipe.Close()
		}()

		var buf [5]byte
		_, err := pipe.Read(buf[:])
		assert.Nil(t, err)
		assert.Equal(t, string(buf[:]), "hello")

		assert.True(t, pipe.Rewind())

		buf = [5]byte{}
		_, err = pipe.Read(buf[:])
		assert.Nil(t, err)
		assert.Equal(t, string(buf[:]), "hello")

		n, err := pipe.Read(buf[:])
		t.Log(n, err)
		assert.Equal(t, n, 0)
		assert.ErrorIs(t, err, io.EOF)
	})
	t.Run("MultipleWrite", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		pool := newBufferPool()
		pipe := newMessagePipe(ctx, pool, maxRPCClientBufferSize)

		go func() {
			msg := messagePayload{Data: []byte("hello")}
			err := pipe.Send(msg)
			assert.Nil(t, err)
			msg = messagePayload{Data: []byte("world")}
			err = pipe.Send(msg)
			assert.Nil(t, err)
			_ = pipe.Close()
		}()

		var buf [6]byte
		rbytes, err := pipe.Read(buf[:])
		assert.Nil(t, err)
		nn, err := pipe.Read(buf[rbytes:])
		assert.Nil(t, err)
		assert.Equal(t, string(buf[:nn+rbytes]), "hellow")

		assert.True(t, pipe.Rewind())

		var buf2 [5]byte
		t.Logf("msgs: %d", len(pipe.msgs))
		t.Logf("msgs[0]: %s", string(pipe.msgs[0].Data))
		t.Logf("msgs[0]: %s", string(pipe.msgs[1].Data))
		t.Logf("index: %d", pipe.index)
		rbytes, err = pipe.Read(buf2[:])
		assert.Nil(t, err)
		assert.Equal(t, string(buf2[:rbytes]), "hello")
		rbytes, err = pipe.Read(buf2[:])
		assert.Nil(t, err)
		assert.Equal(t, string(buf2[:rbytes]), "world")

		_, err = pipe.Read(buf[:])
		assert.ErrorIs(t, err, io.EOF)
	})
}

func TestBuffers_payload(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"number": 42}`)
	buf := &bytes.Buffer{}
	assert.Nil(t, writeEnvelope(buf, bytes.NewBuffer(payload), 8))

	msg := &messagePayload{
		Data:       payload,
		Flags:      8,
		IsEnvelope: true,
	}
	rdFrom := &bytes.Buffer{}
	_, err := rdFrom.ReadFrom(msg)
	assert.Nil(t, err)
	assert.Equal(t, buf.Bytes(), rdFrom.Bytes())

	assert.True(t, msg.Rewind())
	wrTo := &bytes.Buffer{}
	_, err = msg.WriteTo(wrTo)
	assert.Nil(t, err)
	assert.Equal(t, buf.Bytes(), wrTo.Bytes())
}

func BenchmarkPipe(b *testing.B) {
	for _, size := range []int{1, 512, 1024, 1024 * 1024} {
		size := size
		bbuf := bytes.NewBuffer(make([]byte, size))
		b.Run(fmt.Sprintf("io.Pipe:size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			pipeReader, pipeWriter := io.Pipe()
			go func() {
				for i := 0; i < b.N; i++ {
					buf := bbuf.Bytes()
					_, _ = pipeWriter.Write(buf)
				}
				_ = pipeWriter.Close()
			}()
			buf, err := io.ReadAll(pipeReader)
			assert.Nil(b, err)
			assert.Equal(b, size*b.N, len(buf))
		})
		b.Run(fmt.Sprintf("messagePipe:size=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			pipe := newMessagePipe(context.Background(), nil, 0)
			go func() {
				for i := 0; i < b.N; i++ {
					msg := messagePayload{Data: bbuf.Bytes()}
					_ = pipe.Send(msg)
				}
				_ = pipe.Close()
			}()
			buf, err := io.ReadAll(pipe)
			assert.Nil(b, err)
			assert.Equal(b, size*b.N, len(buf))
		})
	}
}
