package pageinspect

import (
	"bytes"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// zstd is the one container format the standard library cannot read, so it is
// also the only reason this package has a dependency at all — kept in its own
// file so the import stays visible.

// zstdMaxWindow bounds the decoder's window buffer. Everything decoded here is
// an inline varlena (at most one heap page), so a frame declaring a huge window
// is either not ours or a decompression bomb; 8 MiB covers every practical
// compression level while keeping the allocation bounded.
const zstdMaxWindow = 8 << 20

// The decoder is stateful across a stream, so a single reused instance is
// serialised rather than shared: the byte-layout overlay decodes one value per
// visible row on every render, and building a fresh decoder each time would
// pay the table allocations for every keypress.
var (
	zstdMu  sync.Mutex
	zstdDec = sync.OnceValue(func() *zstd.Decoder {
		d, err := zstd.NewReader(nil,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxWindow(zstdMaxWindow))
		if err != nil {
			return nil
		}
		return d
	})
)

func decompressZstd(b []byte) (data []byte, truncated, ok bool) {
	d := zstdDec()
	if d == nil {
		return nil, false, false
	}
	zstdMu.Lock()
	defer zstdMu.Unlock()
	if err := d.Reset(bytes.NewReader(b)); err != nil {
		return nil, false, false
	}
	// A bounded read leaves the stream unfinished; resetting to nil drops the
	// reference to it (and any decoder state) until the next value.
	defer d.Reset(nil) //nolint:errcheck // Reset(nil) cannot fail
	return readBounded(d)
}
