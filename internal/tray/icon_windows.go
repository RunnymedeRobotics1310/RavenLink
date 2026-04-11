//go:build windows

package tray

import (
	"bytes"
	"encoding/binary"
)

// makeIcon returns icon bytes in the format expected by fyne.io/systray
// on Windows: ICO container. We wrap the generated PNG in a minimal ICO
// header (Vista+ supports PNG-inside-ICO for the actual image data).
//
// Without this wrapper, systray.SetIcon silently fails on Windows and
// no tray icon appears.
func makeIcon(name string) []byte {
	return wrapPNGAsICO(renderIconPNG(name))
}

// wrapPNGAsICO constructs a minimal single-entry ICO file containing
// a PNG-encoded image. Vista and later accept PNG directly inside ICO.
//
// Format reference: https://en.wikipedia.org/wiki/ICO_(file_format)
//
//	ICONDIR          (6 bytes)
//	  reserved   u16 = 0
//	  type       u16 = 1   (1 = ICO, 2 = CUR)
//	  count      u16 = 1
//	ICONDIRENTRY     (16 bytes)
//	  width      u8  (0 means 256)
//	  height     u8  (0 means 256)
//	  colorCount u8  = 0
//	  reserved   u8  = 0
//	  planes     u16 = 1
//	  bpp        u16 = 32
//	  dataSize   u32 = len(pngData)
//	  dataOffset u32 = 6 + 16 = 22
//	PNG data         (variable)
func wrapPNGAsICO(pngData []byte) []byte {
	var buf bytes.Buffer

	// ICONDIR
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // type = icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // count = 1

	// ICONDIRENTRY for a 64x64 image
	buf.WriteByte(64) // width
	buf.WriteByte(64) // height
	buf.WriteByte(0)  // colorCount
	buf.WriteByte(0)  // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))               // planes
	_ = binary.Write(&buf, binary.LittleEndian, uint16(32))              // bpp
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pngData)))    // dataSize
	_ = binary.Write(&buf, binary.LittleEndian, uint32(6+16))            // dataOffset

	// Image data
	buf.Write(pngData)
	return buf.Bytes()
}
