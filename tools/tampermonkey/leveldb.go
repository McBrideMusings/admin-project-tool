package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

// LevelDB write-ahead log reader. Fresh writes always land in *.log; a record
// Chromium has compacted into an .ldb table is invisible here, so "not seen"
// means exactly that, not "absent".

const (
	logBlock  = 32768
	logHeader = 7 // crc32 (4) + length (2) + type (1)

	recFull, recFirst, recMiddle, recLast = 1, 2, 3, 4
)

// logBatches splits a LevelDB log file into its write batches. A torn tail
// (the browser mid-write) ends the scan with what already parsed.
func logBatches(data []byte) [][]byte {
	var batches [][]byte
	var pending []byte
	pos := 0
	for pos+logHeader <= len(data) {
		if room := logBlock - pos%logBlock; room < logHeader {
			pos += room
			continue
		}
		length := int(binary.LittleEndian.Uint16(data[pos+4:]))
		typ := data[pos+6]
		if length == 0 && typ == 0 {
			break
		}
		end := pos + logHeader + length
		if end > len(data) {
			break
		}
		payload := data[pos+logHeader : end]
		pos = end
		switch typ {
		case recFull:
			batches = append(batches, payload)
		case recFirst:
			pending = append([]byte(nil), payload...)
		case recMiddle:
			pending = append(pending, payload...)
		case recLast:
			batches = append(batches, append(pending, payload...))
			pending = nil
		}
	}
	return batches
}

type seqValue struct {
	seq     uint64
	value   []byte
	deleted bool
}

// mergeBatch applies one write batch to latest, keeping the highest sequence
// number per key. A malformed batch stops at the first record it cannot read.
func mergeBatch(batch []byte, latest map[string]seqValue) {
	if len(batch) < 12 {
		return
	}
	seq := binary.LittleEndian.Uint64(batch)
	count := binary.LittleEndian.Uint32(batch[8:])
	i := 12
	for n := uint32(0); n < count; n++ {
		if i >= len(batch) {
			return
		}
		kind := batch[i]
		i++
		key, next, ok := lengthPrefixed(batch, i)
		if !ok {
			return
		}
		i = next
		rec := seqValue{seq: seq + uint64(n), deleted: true}
		if kind == 1 {
			val, next, ok := lengthPrefixed(batch, i)
			if !ok {
				return
			}
			i = next
			rec.value, rec.deleted = val, false
		}
		if prev, seen := latest[string(key)]; !seen || rec.seq > prev.seq {
			latest[string(key)] = rec
		}
	}
}

func lengthPrefixed(buf []byte, i int) ([]byte, int, bool) {
	n, width := binary.Uvarint(buf[i:])
	if width <= 0 || i+width+int(n) > len(buf) {
		return nil, i, false
	}
	start := i + width
	return buf[start : start+int(n)], start + int(n), true
}

// leveldbLogKV returns the newest live record per key across a LevelDB's *.log
// files, with its sequence number. Deleted keys are left out.
func leveldbLogKV(dbDir string) map[string]seqValue {
	logs, _ := filepath.Glob(filepath.Join(dbDir, "*.log"))
	sort.Strings(logs)
	latest := map[string]seqValue{}
	for _, path := range logs {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, batch := range logBatches(data) {
			mergeBatch(batch, latest)
		}
	}
	out := map[string]seqValue{}
	for k, v := range latest {
		if !v.deleted {
			out[k] = v
		}
	}
	return out
}
