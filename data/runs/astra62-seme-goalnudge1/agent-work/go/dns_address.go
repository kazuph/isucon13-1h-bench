package main

import (
	"bytes"
	"encoding/binary"
	"strings"
)

var zoneLabels = []byte("\x01u\x06isucon\x03dev\x00")

func wireName(name string) string {
	var b strings.Builder
	for _, label := range strings.Split(strings.TrimSuffix(strings.ToLower(name), "."), ".") {
		if len(label) > 63 || len(label) == 0 {
			return ""
		}
		b.WriteByte(byte(len(label)))
		b.WriteString(label)
	}
	b.WriteByte(0)
	return b.String()
}

// Answer ordinary, uncompressed IN/A questions without re-encoding their
// names. More complex DNS messages still go through the general parser.
func addressAnswer(q, dst []byte) ([]byte, bool) {
	if len(q) < 17 || q[2]&0xf8 != 0 || binary.BigEndian.Uint16(q[4:6]) != 1 {
		return nil, false
	}
	pos := 12
	origin := -1
	for {
		if pos >= len(q) || pos-12 >= 255 {
			return nil, false
		}
		if origin < 0 && len(q)-pos >= len(zoneLabels) && bytes.Equal(q[pos:pos+len(zoneLabels)], zoneLabels) {
			origin = pos
		}
		n := int(q[pos])
		pos++
		if n == 0 {
			break
		}
		if n > 63 || n > len(q)-pos {
			return nil, false
		}
		for _, v := range q[pos : pos+n] {
			if v >= 'A' && v <= 'Z' {
				return nil, false
			}
		}
		pos += n
	}
	if origin < 0 || pos-origin != len(zoneLabels) || len(q)-pos < 4 || binary.BigEndian.Uint32(q[pos:pos+4]) != 0x00010001 {
		return nil, false
	}
	zone.RLock()
	ip, found := zone.wire[string(q[12:pos])]
	zone.RUnlock()
	out := append(dst, q[:pos+4]...)
	flags := uint16(0x8400) | uint16(q[2]&1)<<8
	clear(out[6:12])
	if found {
		binary.BigEndian.PutUint16(out[2:4], flags)
		out[7] = 1
		out = append(out, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		out = append(out, ip[:]...)
		return out, true
	}
	binary.BigEndian.PutUint16(out[2:4], flags|3)
	out[9] = 1
	pointer := uint16(0xc000 | origin)
	out = binary.BigEndian.AppendUint16(out, pointer)
	// SOA with two compressed names, followed by the five 32-bit timers.
	out = append(out, 0, 6, 0, 1, 0, 0, 0, 0, 0, 39, 3, 'n', 's', '1')
	out = binary.BigEndian.AppendUint16(out, pointer)
	out = append(out, 10)
	out = append(out, "hostmaster"...)
	out = binary.BigEndian.AppendUint16(out, pointer)
	for _, n := range [...]uint32{0, 10800, 3600, 604800, 0} {
		out = binary.BigEndian.AppendUint32(out, n)
	}
	return out, true
}
