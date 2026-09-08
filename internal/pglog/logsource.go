package pglog

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"pgdu/internal/procfs"
)

// ErrNotIncremental is returned by ReadFrom on sources that cannot seek
// (gzip); the caller falls back to a full ReadTail.
var ErrNotIncremental = errors.New("log source cannot be read incrementally")

// maxWholeFile caps "whole file" reads so a runaway log cannot exhaust memory.
const maxWholeFile = 2 << 30

// ── local plain file ─────────────────────────────────────────────────────────

type localFileSource struct {
	info SourceInfo
}

// OpenLocal opens a log on this host, picking the gzip reader by suffix.
func OpenLocal(path string) Source {
	info := SourceInfo{Kind: "local", Path: path, Size: -1, Lines: -1, Rotated: IsRotatedName(path)}
	if fi, err := os.Stat(path); err == nil {
		info.Size = fi.Size()
		info.ModTime = fi.ModTime()
		info.Lines = EstimateLines(path, fi.Size())
	}
	if strings.HasSuffix(path, ".gz") {
		info.Kind = "gz"
		return &gzFileSource{info: info}
	}
	return &localFileSource{info: info}
}

func (s *localFileSource) Info() SourceInfo { return s.info }

func (s *localFileSource) ReadTail(_ context.Context, n int64) ([]byte, Window, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, Window{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, Window{}, err
	}
	size := fi.Size()
	s.info.Size, s.info.ModTime = size, fi.ModTime()
	win := Window{Requested: n, FileSize: size}
	start := int64(0)
	if n > 0 && size > n {
		start = size - n
		win.Truncated = true
	} else if n <= 0 && size > maxWholeFile {
		start = size - maxWholeFile
		win.Truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, win, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, win, err
	}
	buf, dropped := AlignHead(buf, start > 0)
	win.Start = start + int64(dropped)
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

func (s *localFileSource) ReadFrom(_ context.Context, off int64) ([]byte, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

func (s *localFileSource) Cursor(_ context.Context) *Cursor {
	fi, err := os.Stat(s.info.Path)
	if err != nil {
		return nil
	}
	return &Cursor{Inode: procfs.Inode(fi), Size: fi.Size()}
}

// lineSample is how much of a file's head is read to measure the average line
// length for EstimateLines.
const lineSample = 64 << 10

// EstimateLines approximates a log's line count without reading it all: the
// average line length over the first 64 KiB scaled to the uncompressed size.
// For a .gz the uncompressed size comes from the gzip trailer (ISIZE, exact
// below 4 GiB) and the sample from decompressing the head. -1 when the file
// cannot be read.
func EstimateLines(path string, size int64) int64 {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	total := size
	if strings.HasSuffix(path, ".gz") {
		if size < 8 {
			return -1
		}
		var trailer [4]byte
		if _, err := f.ReadAt(trailer[:], size-4); err != nil {
			return -1
		}
		total = int64(uint32(trailer[0]) | uint32(trailer[1])<<8 | uint32(trailer[2])<<16 | uint32(trailer[3])<<24)
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return -1
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			return -1
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}
	head := make([]byte, lineSample)
	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return -1
	}
	head = head[:n]
	if n == 0 {
		return 0
	}
	lines := int64(bytes.Count(head, []byte{'\n'}))
	if int64(n) >= total {
		if n > 0 && head[n-1] != '\n' {
			lines++
		}
		return lines
	}
	if lines == 0 {
		return 1
	}
	return total * lines / int64(n)
}

// ── local gzip file ──────────────────────────────────────────────────────────

type gzFileSource struct {
	info SourceInfo
}

func (s *gzFileSource) Info() SourceInfo { return s.info }

func (s *gzFileSource) ReadTail(ctx context.Context, n int64) ([]byte, Window, error) {
	f, err := os.Open(s.info.Path)
	if err != nil {
		return nil, Window{}, err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, Window{}, fmt.Errorf("gunzip %s: %w", s.info.Path, err)
	}
	defer func() { _ = zr.Close() }()
	return TailOfStream(ctx, zr, n)
}

func (s *gzFileSource) ReadFrom(context.Context, int64) ([]byte, error) {
	return nil, ErrNotIncremental
}
func (s *gzFileSource) Cursor(context.Context) *Cursor { return nil }

// TailOfStream drains r keeping only its last n bytes (n <= 0: everything up
// to maxWholeFile), using two buffers of n so peak memory stays under 2n.
func TailOfStream(ctx context.Context, r io.Reader, n int64) ([]byte, Window, error) {
	limit := n
	if limit <= 0 {
		limit = maxWholeFile
	}
	tk := &tailKeeper{n: limit}
	rd := &ctxReader{ctx: ctx, r: r}
	if _, err := io.Copy(tk, rd); err != nil {
		return nil, Window{}, err
	}
	buf := tk.Bytes()
	win := Window{Requested: n, FileSize: tk.total, Truncated: tk.total > int64(len(buf))}
	buf, dropped := AlignHead(buf, win.Truncated)
	win.Start = tk.total - int64(len(buf))
	win.DroppedHead = int64(dropped)
	win.Bytes = int64(len(buf))
	return buf, win, nil
}

// tailKeeper is an io.Writer that remembers the last n bytes written. It
// swaps two n-sized buffers instead of shifting, so each byte is copied at
// most twice.
type tailKeeper struct {
	n     int64
	prev  []byte
	cur   []byte
	total int64
}

func (t *tailKeeper) Write(p []byte) (int, error) {
	written := len(p)
	t.total += int64(written)
	for len(p) > 0 {
		room := int(t.n) - len(t.cur)
		if room == 0 {
			t.prev, t.cur = t.cur, t.prev[:0]
			room = int(t.n)
		}
		k := min(room, len(p))
		t.cur = append(t.cur, p[:k]...)
		p = p[k:]
	}
	return written, nil
}

// Bytes returns the retained tail as one contiguous slice.
func (t *tailKeeper) Bytes() []byte {
	if len(t.prev) == 0 {
		return t.cur
	}
	need := min(int(t.n)-len(t.cur), len(t.prev))
	out := make([]byte, 0, need+len(t.cur))
	out = append(out, t.prev[len(t.prev)-need:]...)
	out = append(out, t.cur...)
	return out
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// AlignHead drops the partial first line of a window that did not start at
// offset 0, returning the aligned buffer and how many bytes went.
func AlignHead(buf []byte, cut bool) ([]byte, int) {
	if !cut {
		return buf, 0
	}
	i := bytes.IndexByte(buf, '\n')
	if i < 0 {
		return buf[:0], len(buf)
	}
	return buf[i+1:], i + 1
}

// skipContinuation drops leading lines that are not primary lines under m —
// continuation text whose primary entry lies before the window start.
func skipContinuation(buf []byte, m *prefixMatcher) ([]byte, int) {
	dropped := 0
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		line := buf
		if i >= 0 {
			line = buf[:i]
		}
		if f, ok := m.Match(line); ok {
			if _, isAttach := attachmentField(f.tag); !isAttach {
				break
			}
		}
		if i < 0 {
			dropped += len(buf)
			return buf[:0], dropped
		}
		buf = buf[i+1:]
		dropped += i + 1
	}
	return buf, dropped
}

var rotatedRe = regexp.MustCompile(`\.log\.(\d+)(\.gz)?$`)

func IsRotatedName(path string) bool { return rotatedRe.MatchString(path) }

// RotationIndex orders rotated files: the live log is 0, ".log.1" is 1, ….
func RotationIndex(path string) int {
	m := rotatedRe.FindStringSubmatch(path)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
