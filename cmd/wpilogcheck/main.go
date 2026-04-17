package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/wpilog"
)

func main() {
	data, err := os.ReadFile("/Users/jeff/src/1310/RavenLink/20260417T121554Z_424bafe6.jsonl")
	if err != nil {
		fmt.Println("read err:", err); os.Exit(1)
	}
	out, err := wpilog.Convert(data, 1310, "424bafe6")
	if err != nil {
		fmt.Println("convert err:", err); os.Exit(1)
	}
	fmt.Printf("WPILog output size: %d bytes\n\n", len(out))

	// Quick scan for control records (Start = 0x01) and pull entry name+type.
	// WPILog header is 12 bytes: "WPILOG" + u16 ver + u32 extraHeaderLen + extraHeader.
	if !bytes.HasPrefix(out, []byte("WPILOG")) {
		fmt.Println("missing WPILOG header"); os.Exit(1)
	}
	pos := 6 + 2 // magic + version
	exLen := binary.LittleEndian.Uint32(out[pos:])
	pos += 4 + int(exLen)

	type entry struct{ id uint32; name, typ string }
	var entries []entry
	dataCounts := map[uint32]int{}
	dataLastPayload := map[uint32][]byte{}

	for pos < len(out) {
		// Decode header bitfield byte
		hdr := out[pos]; pos++
		entryLen := int((hdr & 0x03) + 1)
		sizeLen  := int(((hdr >> 2) & 0x03) + 1)
		tsLen    := int(((hdr >> 4) & 0x07) + 1)
		readLE := func(n int) uint64 {
			b := make([]byte, 8)
			copy(b, out[pos:pos+n]); pos += n
			return binary.LittleEndian.Uint64(b)
		}
		entryID := uint32(readLE(entryLen))
		size := int(readLE(sizeLen))
		_ = readLE(tsLen) // ts
		payload := out[pos:pos+size]; pos += size

		if entryID != 0 {
			dataCounts[entryID]++
			dataLastPayload[entryID] = payload
		}
		if entryID == 0 {
			// Control record: first byte is type (0=Start, 1=Finish, 2=SetMetadata)
			if len(payload) > 0 && payload[0] == 0x00 {
				// Start: u32 entryID, u32 nameLen, name, u32 typeLen, type, u32 metaLen, meta
				p := 1
				eid := binary.LittleEndian.Uint32(payload[p:]); p += 4
				nl := binary.LittleEndian.Uint32(payload[p:]); p += 4
				name := string(payload[p:p+int(nl)]); p += int(nl)
				tl := binary.LittleEndian.Uint32(payload[p:]); p += 4
				typ := string(payload[p:p+int(tl)])
				entries = append(entries, entry{eid, name, typ})
			}
		}
	}

	fmt.Printf("Topic Start records: %d\n\n", len(entries))
	fmt.Println("=== Schema/struct entries in WPILog (with data record counts) ===")
	for _, e := range entries {
		if bytes.Contains([]byte(e.typ), []byte("struct")) ||
			bytes.Contains([]byte(e.name), []byte(".schema")) {
			n := dataCounts[e.id]
			lp := dataLastPayload[e.id]
			summary := ""
			if e.typ == "structschema" && len(lp) > 0 {
				summary = fmt.Sprintf("  schema=%q", string(lp))
			} else if len(lp) > 0 {
				summary = fmt.Sprintf("  last_payload=%d bytes", len(lp))
			}
			fmt.Printf("  id=%-3d data_records=%-5d name=%-50s type=%-15s%s\n",
				e.id, n, e.name, e.typ, summary)
		}
	}
}
