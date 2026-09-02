package pg

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// LogSettings returns the logging GUCs (see logSettingsKeys), best-effort.
func (c *Client) LogSettings(ctx context.Context) map[string]string {
	pool, err := c.PoolFor(ctx, c.DefaultDB())
	if err != nil {
		return map[string]string{}
	}
	return settingsMap(ctx, pool, sqlMaintSettings, logSettingsKeys)
}

// LogLocation resolves the server's log_timezone; nil falls back to UTC in
// the parser. Zone abbreviations in %t are resolved against this location.
func LogLocation(settings map[string]string) *time.Location {
	if tz := settings["log_timezone"]; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	return time.UTC
}

// LoadLog reads the tail window of src and returns the parsed, classified and
// aggregated report. serverPrefix is the server's log_line_prefix (empty when
// unknown); DetectPrefix falls back to sniffing when it does not fit the file.
// This is a plain function so it is testable without a database.
func LoadLog(ctx context.Context, src LogSource, serverPrefix string, loc *time.Location, window int64, opts AggOptions) (*LogReport, error) {
	buf, win, err := src.ReadTail(ctx, window)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", src.Info().Path, err)
	}
	r := &LogReport{Source: src.Info(), Window: win, Format: DetectLogFormat(buf)}
	var m *prefixMatcher
	if r.Format == LogFormatStderr {
		m, r.PrefixDetected = DetectPrefix(buf, serverPrefix, loc)
		r.Prefix = m.prefix
		var dropped int
		buf, dropped = skipContinuation(buf, m)
		r.Window.Start += int64(dropped)
		r.Window.DroppedHead += int64(dropped)
		r.Window.Bytes = int64(len(buf))
	} else {
		r.Prefix = r.Format.String()
	}
	p := NewLogParser(r.Format, m, loc)
	p.Feed(buf, r.Window.Start)
	r.parser = p
	r.finish(opts)
	r.Cursor = src.Cursor(ctx)
	if r.Cursor != nil {
		r.Cursor.Off = r.Window.Start + int64(len(buf))
		if off := p.LastEntryOff(); off >= 0 {
			r.Cursor.Off = off
		}
	}
	return r, nil
}

// finish classifies and aggregates the parser's entries into r.
func (r *LogReport) finish(opts AggOptions) {
	r.Entries = r.parser.Entries()
	Classify(r.Entries)
	r.Entries = MergePlans(r.Entries)
	r.Window.Lines = r.parser.Lines()
	r.Unparsed = r.parser.Unparsed()
	Aggregate(r, opts)
}

// RefreshLog re-reads src for a live tail. When the file merely grew it feeds
// only the appended bytes (re-parsing from the last entry, which may have been
// mid-flush); a rotation, a shrink, a non-seekable source or a window that
// outgrew its request by half falls back to a full LoadLog.
func RefreshLog(ctx context.Context, prev *LogReport, src LogSource, loc *time.Location, opts AggOptions) (*LogReport, error) {
	full := func() (*LogReport, error) {
		hint := prev.Prefix
		if prev.PrefixDetected || prev.Format != LogFormatStderr {
			hint = ""
		}
		return LoadLog(ctx, src, hint, loc, prev.Window.Requested, opts)
	}
	cur := src.Cursor(ctx)
	if prev.Cursor == nil || cur == nil || prev.parser == nil ||
		cur.Inode != prev.Cursor.Inode || cur.Size < prev.Cursor.Size {
		return full()
	}
	if cur.Size == prev.Cursor.Size {
		return prev, nil
	}
	if prev.Window.Requested > 0 && cur.Size-prev.Window.Start > prev.Window.Requested*3/2 {
		return full()
	}
	buf, err := src.ReadFrom(ctx, prev.Cursor.Off)
	if err != nil {
		if errors.Is(err, ErrNotIncremental) {
			return full()
		}
		return nil, fmt.Errorf("read %s: %w", src.Info().Path, err)
	}
	p := prev.parser
	p.TruncateLast()
	p.Feed(buf, prev.Cursor.Off)
	r := &LogReport{
		Source: src.Info(), Window: prev.Window, Format: prev.Format,
		Prefix: prev.Prefix, PrefixDetected: prev.PrefixDetected, parser: p,
	}
	r.Window.FileSize = cur.Size
	r.Window.Bytes = cur.Size - r.Window.Start
	r.finish(opts)
	cur.Off = prev.Cursor.Off + int64(len(buf))
	if off := p.LastEntryOff(); off >= 0 {
		cur.Off = off
	}
	r.Cursor = cur
	return r, nil
}
