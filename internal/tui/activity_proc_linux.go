//go:build linux

package tui

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// sampleAllPids reads /proc stats for each PID in pids. PIDs that have exited
// or are unreadable (e.g. the postgres process runs as a different user and
// /proc/<pid>/status is not world-readable on this kernel) are silently skipped.
func sampleAllPids(pids []int32) []procRaw {
	now := time.Now()
	out := make([]procRaw, 0, len(pids))
	for _, pid := range pids {
		if r, ok := readProcRaw(pid, now); ok {
			out = append(out, r)
		}
	}
	return out
}

func readProcRaw(pid int32, now time.Time) (procRaw, bool) {
	base := fmt.Sprintf("/proc/%d/", pid)
	r := procRaw{PID: pid, At: now, ReadBytes: -1, WriteBytes: -1}

	// RSS from /proc/<pid>/status. If this file is unreadable the process is
	// gone or we lack permission — skip the entire PID.
	data, err := os.ReadFile(base + "status")
	if err != nil {
		return procRaw{}, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "VmRSS:") {
			// "VmRSS:   12345 kB"
			if fields := strings.Fields(line[6:]); len(fields) >= 1 {
				if kb, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
					r.RSSBytes = kb * 1024
				}
			}
			break
		}
	}

	// CPU ticks from /proc/<pid>/stat.
	if data, err := os.ReadFile(base + "stat"); err == nil {
		if ticks, ok := parseStatTicks(data); ok {
			r.CPUTicks = ticks
		}
	}

	// I/O counters from /proc/<pid>/io (requires same UID as the process or
	// CAP_SYS_PTRACE; unreadable = -1, shown as — in the columns).
	if data, err := os.ReadFile(base + "io"); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "read_bytes:"):
				if v, err := strconv.ParseInt(strings.TrimSpace(line[11:]), 10, 64); err == nil {
					r.ReadBytes = v
				}
			case strings.HasPrefix(line, "write_bytes:"):
				if v, err := strconv.ParseInt(strings.TrimSpace(line[12:]), 10, 64); err == nil {
					r.WriteBytes = v
				}
			}
		}
	}

	return r, true
}

// parseStatTicks extracts utime+stime from the raw content of /proc/<pid>/stat.
// The comm field (field 2) can contain spaces and parentheses, so we find the
// last ')' and parse the well-structured fields that follow.
func parseStatTicks(data []byte) (uint64, bool) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 || end+2 >= len(data) {
		return 0, false
	}
	// Fields after ')':
	//   [0]=state [1]=ppid [2]=pgrp [3]=session [4]=tty_nr [5]=tpgid
	//   [6]=flags [7]=minflt [8]=cminflt [9]=majflt [10]=cmajflt
	//   [11]=utime [12]=stime
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}
