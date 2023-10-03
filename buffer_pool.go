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
	"bytes"
	"sync"
)

const (
	initialBufferSize    = bytes.MinRead
	maxRecycleBufferSize = 8 * 1024 * 1024 // if >8MiB, don't hold onto a buffer
)

type bufferPool struct {
	sync.Pool
}

func newBufferPool() *bufferPool {
	return &bufferPool{}
}

func (b *bufferPool) Get() *bytes.Buffer {
	if buf, ok := b.Pool.Get().(*bytes.Buffer); ok {
		buf.Reset()
		return buf
	}
	return bytes.NewBuffer(make([]byte, 0, initialBufferSize))
}

func (b *bufferPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > maxRecycleBufferSize {
		return
	}
	b.Pool.Put(buf)
}
