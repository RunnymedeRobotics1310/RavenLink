// Package wpilog encodes data into the WPILog (.wpilog) binary format
// used by WPILib and AdvantageScope. It implements version 1.0 of the
// format specification.
//
// All multi-byte integers are little-endian. Timestamps are unsigned
// 64-bit microseconds. Records use a compact variable-length header
// to minimise file size.
//
// Specification: https://github.com/wpilibsuite/allwpilib/blob/main/wpiutil/doc/datalog.adoc
package wpilog

import (
	"encoding/binary"
	"io"
)

// Format constants.
var headerMagic = [6]byte{'W', 'P', 'I', 'L', 'O', 'G'}

const headerVersion uint16 = 0x0100 // v1.0: minor=0x00, major=0x01

// Control record type bytes (first byte of payload when entry ID == 0).
const (
	controlStart       = 0x00
	controlFinish      = 0x01
	controlSetMetadata = 0x02
)

// WriteHeader writes the WPILog file header. extraHeader is an
// arbitrary UTF-8 string (typically JSON metadata); pass "" for none.
func WriteHeader(w io.Writer, extraHeader string) error {
	if _, err := w.Write(headerMagic[:]); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, headerVersion); err != nil {
		return err
	}
	ehBytes := []byte(extraHeader)
	if err := binary.Write(w, binary.LittleEndian, uint32(len(ehBytes))); err != nil {
		return err
	}
	if len(ehBytes) > 0 {
		if _, err := w.Write(ehBytes); err != nil {
			return err
		}
	}
	return nil
}

// WriteStartRecord writes a control Start record that declares a new
// data channel. The entry must be Started before any data records
// reference its entryID.
func WriteStartRecord(w io.Writer, entryID uint32, name, typeName, metadata string, timestamp uint64) error {
	nameBytes := []byte(name)
	typeBytes := []byte(typeName)
	metaBytes := []byte(metadata)

	// Payload: type(1) + entryID(4) + nameLen(4) + name + typeLen(4) + type + metaLen(4) + meta
	payloadLen := 1 + 4 + 4 + len(nameBytes) + 4 + len(typeBytes) + 4 + len(metaBytes)
	payload := make([]byte, payloadLen)

	off := 0
	payload[off] = controlStart
	off++
	binary.LittleEndian.PutUint32(payload[off:], entryID)
	off += 4
	binary.LittleEndian.PutUint32(payload[off:], uint32(len(nameBytes)))
	off += 4
	copy(payload[off:], nameBytes)
	off += len(nameBytes)
	binary.LittleEndian.PutUint32(payload[off:], uint32(len(typeBytes)))
	off += 4
	copy(payload[off:], typeBytes)
	off += len(typeBytes)
	binary.LittleEndian.PutUint32(payload[off:], uint32(len(metaBytes)))
	off += 4
	copy(payload[off:], metaBytes)

	return writeRecord(w, 0, timestamp, payload)
}

// WriteFinishRecord writes a control Finish record indicating no more
// data records will be sent for the given entry.
func WriteFinishRecord(w io.Writer, entryID uint32, timestamp uint64) error {
	payload := make([]byte, 5)
	payload[0] = controlFinish
	binary.LittleEndian.PutUint32(payload[1:], entryID)
	return writeRecord(w, 0, timestamp, payload)
}

// WriteDataRecord writes a data record for the given entry.
func WriteDataRecord(w io.Writer, entryID uint32, timestamp uint64, payload []byte) error {
	return writeRecord(w, entryID, timestamp, payload)
}

// writeRecord writes one record (control or data) with the compact
// variable-length header.
//
// Header bitfield (1 byte):
//
//	bits 1-0: entry ID byte count - 1   (0..3 → 1..4 bytes)
//	bits 3-2: payload size byte count - 1 (0..3 → 1..4 bytes)
//	bits 6-4: timestamp byte count - 1   (0..7 → 1..8 bytes)
//	bit 7:    spare (0)
func writeRecord(w io.Writer, entryID uint32, timestamp uint64, payload []byte) error {
	entryBytes := minBytes(uint64(entryID), 4)
	sizeBytes := minBytes(uint64(len(payload)), 4)
	tsBytes := minBytes(timestamp, 8)

	headerByte := byte((entryBytes - 1) | ((sizeBytes - 1) << 2) | ((tsBytes - 1) << 4))

	// Max header: 1 + 4 + 4 + 8 = 17 bytes.
	var buf [17]byte
	buf[0] = headerByte
	off := 1
	off += putVarInt(buf[off:], uint64(entryID), entryBytes)
	off += putVarInt(buf[off:], uint64(len(payload)), sizeBytes)
	off += putVarInt(buf[off:], timestamp, tsBytes)

	if _, err := w.Write(buf[:off]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// minBytes returns the minimum number of bytes (1..max) needed to
// represent v in little-endian.
func minBytes(v uint64, max int) int {
	if v == 0 {
		return 1
	}
	n := 1
	for n < max && v >= (1<<(8*n)) {
		n++
	}
	return n
}

// putVarInt writes v as a little-endian integer using exactly numBytes.
// Returns numBytes.
func putVarInt(buf []byte, v uint64, numBytes int) int {
	for i := 0; i < numBytes; i++ {
		buf[i] = byte(v >> (8 * i))
	}
	return numBytes
}
