package websocket

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

// WebSocket opcodes
const (
	WSOpcodeContinuation = 0x0
	WSOpcodeText         = 0x1
	WSOpcodeBinary       = 0x2
	WSOpcodeClose        = 0x8
	WSOpcodePing         = 0x9
	WSOpcodePong         = 0xA
)

// WebSocket status codes
const (
	WSStatusNormalClose      = 1000
	WSStatusGoingAway        = 1001
	WSStatusProtocolError    = 1002
	WSStatusUnsupportedData  = 1003
	WSStatusNoStatusReceived = 1005
	WSStatusAbnormalClose    = 1006
	WSStatusInvalidPayload   = 1007
	WSStatusPolicyViolation  = 1008
	WSStatusMessageTooBig    = 1009
	WSStatusMandatoryExt     = 1010
	WSStatusInternalError    = 1011
	WSStatusTLSHandshake     = 1015
)

// WebSocketState represents WebSocket connection state
type WebSocketState struct {
	Opcode     int
	PacketSize int
	Packet     []byte
	MaskGen    []byte
	mu         struct{}
}

// WebSocketAppendHdr appends a WebSocket frame header
func WebSocketAppendHdr(buf *bytes.Buffer, opcode int, length int, mask []byte) {
	var firstByte byte
	firstByte = byte(opcode) | 0x80 // FIN bit set

	buf.WriteByte(firstByte)

	var secondByte byte
	if mask != nil {
		secondByte |= 0x80
	}

	// Handle payload length
	if length < 126 {
		secondByte |= byte(length)
		buf.WriteByte(secondByte)
	} else if length < 65536 {
		secondByte |= 126
		buf.WriteByte(secondByte)
		binary.Write(buf, binary.BigEndian, uint16(length))
	} else {
		secondByte |= 127
		buf.WriteByte(secondByte)
		binary.Write(buf, binary.BigEndian, uint64(length))
	}

	// Write mask if present
	if mask != nil {
		buf.Write(mask)
	}
}

// WebSocketAppend appends a WebSocket frame
func WebSocketAppend(buf *bytes.Buffer, opcode int, data []byte, length int, state *WebSocketState) {
	if state == nil {
		state = &WebSocketState{}
	}

	// Generate mask if needed
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		// Fallback to zero mask
		mask = []byte{0, 0, 0, 0}
	}

	// Append header
	WebSocketAppendHdr(buf, opcode, length, mask)

	// Write masked data
	if len(data) > 0 {
		maskedData := make([]byte, length)
		copy(maskedData, data[:length])
		for i := range length {
			maskedData[i] ^= mask[i%4]
		}
		buf.Write(maskedData)
	}
}

// WebSocketParse parses WebSocket frames from a buffer
// Returns:
//
//	0 - Not enough data in input buffer, call again when more is available
//	1 - Fatal error, disconnect
//	2 - Frame parsed successfully
func WebSocketParse(buf *bytes.Buffer, cb func(opaque any, opcode int, data []byte, len int), opaque any, state *WebSocketState) int {
	if state == nil {
		state = &WebSocketState{}
	}

	data := buf.Bytes()

	// Minimum frame size is 2 bytes
	if len(data) < 2 {
		return 0
	}

	firstByte := data[0]
	secondByte := data[1]

	fin := (firstByte & 0x80) != 0
	opcode := int(firstByte & 0x0F)
	masked := (secondByte & 0x80) != 0
	payloadLen := int(secondByte & 0x7F)

	headerLen := 2

	// Get extended payload length
	if payloadLen == 126 {
		if len(data) < 4 {
			return 0
		}
		payloadLen = int(binary.BigEndian.Uint16(data[2:4]))
		headerLen = 4
	} else if payloadLen == 127 {
		if len(data) < 10 {
			return 0
		}
		payloadLen = int(binary.BigEndian.Uint64(data[2:10]))
		headerLen = 10
	}

	// Get mask
	var mask []byte
	if masked {
		if len(data) < headerLen+4 {
			return 0
		}
		mask = data[headerLen : headerLen+4]
		headerLen += 4
	}

	// Check if we have the full payload
	if len(data) < headerLen+payloadLen {
		return 0
	}

	// Extract payload
	payload := data[headerLen : headerLen+payloadLen]

	// Unmask if needed
	if masked && len(mask) == 4 {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	// Handle continuation frames
	if opcode == WSOpcodeContinuation {
		if state.Packet == nil {
			return 1 // Protocol error
		}

		// Append to existing packet
		state.Packet = append(state.Packet, payload...)
		state.PacketSize += len(payload)

		if fin {
			// Final continuation frame
			if cb != nil {
				cb(opaque, state.Opcode, state.Packet, state.PacketSize)
			}
			state.Packet = nil
			state.PacketSize = 0
			state.Opcode = 0
		}
	} else {
		if fin {
			// Single frame
			if cb != nil {
				cb(opaque, opcode, payload, len(payload))
			}
		} else {
			// Start of fragmented message
			state.Opcode = opcode
			state.Packet = make([]byte, len(payload))
			copy(state.Packet, payload)
			state.PacketSize = len(payload)
		}
	}

	// Remove parsed data from buffer
	buf.Next(headerLen + payloadLen)

	return 2
}

// WebSocketFrame represents a WebSocket frame
type WebSocketFrame struct {
	Fin     bool
	Opcode  int
	Masked  bool
	Mask    []byte
	Payload []byte
	Raw     []byte
}

// ParseWebSocketFrame parses a single WebSocket frame
func ParseWebSocketFrame(data []byte) (*WebSocketFrame, error) {
	if len(data) < 2 {
		return nil, errors.New("insufficient data for frame header")
	}

	frame := &WebSocketFrame{
		Raw: data,
	}

	firstByte := data[0]
	secondByte := data[1]

	frame.Fin = (firstByte & 0x80) != 0
	frame.Opcode = int(firstByte & 0x0F)
	frame.Masked = (secondByte & 0x80) != 0
	payloadLen := int(secondByte & 0x7F)

	headerLen := 2

	// Get extended payload length
	if payloadLen == 126 {
		if len(data) < 4 {
			return nil, errors.New("insufficient data for extended payload length")
		}
		payloadLen = int(binary.BigEndian.Uint16(data[2:4]))
		headerLen = 4
	} else if payloadLen == 127 {
		if len(data) < 10 {
			return nil, errors.New("insufficient data for extended payload length")
		}
		payloadLen = int(binary.BigEndian.Uint64(data[2:10]))
		headerLen = 10
	}

	// Get mask
	if frame.Masked {
		if len(data) < headerLen+4 {
			return nil, errors.New("insufficient data for mask")
		}
		frame.Mask = data[headerLen : headerLen+4]
		headerLen += 4
	}

	// Get payload
	if len(data) < headerLen+payloadLen {
		return nil, errors.New("insufficient data for payload")
	}

	frame.Payload = data[headerLen : headerLen+payloadLen]

	// Unmask if needed
	if frame.Masked && len(frame.Mask) == 4 {
		for i := range frame.Payload {
			frame.Payload[i] ^= frame.Mask[i%4]
		}
	}

	return frame, nil
}

// BuildWebSocketFrame builds a WebSocket frame
func BuildWebSocketFrame(fin bool, opcode int, payload []byte, masked bool) []byte {
	buf := &bytes.Buffer{}

	var firstByte byte
	if fin {
		firstByte = 0x80
	}
	firstByte |= byte(opcode)

	var secondByte byte
	if masked {
		secondByte = 0x80
	}

	length := len(payload)

	// Handle payload length
	if length < 126 {
		secondByte |= byte(length)
		buf.WriteByte(firstByte)
		buf.WriteByte(secondByte)
	} else if length < 65536 {
		secondByte |= 126
		buf.WriteByte(firstByte)
		buf.WriteByte(secondByte)
		binary.Write(buf, binary.BigEndian, uint16(length))
	} else {
		secondByte |= 127
		buf.WriteByte(firstByte)
		buf.WriteByte(secondByte)
		binary.Write(buf, binary.BigEndian, uint64(length))
	}

	// Generate and write mask if needed
	if masked {
		mask := make([]byte, 4)
		if _, err := rand.Read(mask); err != nil {
			mask = []byte{0, 0, 0, 0}
		}
		buf.Write(mask)

		// Write masked payload
		maskedPayload := make([]byte, length)
		copy(maskedPayload, payload)
		for i := range maskedPayload {
			maskedPayload[i] ^= mask[i%4]
		}
		buf.Write(maskedPayload)
	} else {
		buf.Write(payload)
	}

	return buf.Bytes()
}

// WebSocketCloseFrame builds a close frame
func WebSocketCloseFrame(code int, reason string) []byte {
	buf := &bytes.Buffer{}
	binary.Write(buf, binary.BigEndian, uint16(code))
	buf.WriteString(reason)
	return BuildWebSocketFrame(true, WSOpcodeClose, buf.Bytes(), false)
}

// WebSocketPingFrame builds a ping frame
func WebSocketPingFrame(data []byte) []byte {
	return BuildWebSocketFrame(true, WSOpcodePing, data, false)
}

// WebSocketPongFrame builds a pong frame
func WebSocketPongFrame(data []byte) []byte {
	return BuildWebSocketFrame(true, WSOpcodePong, data, false)
}

// WebSocketTextFrame builds a text frame
func WebSocketTextFrame(text string) []byte {
	return BuildWebSocketFrame(true, WSOpcodeText, []byte(text), false)
}

// WebSocketBinaryFrame builds a binary frame
func WebSocketBinaryFrame(data []byte) []byte {
	return BuildWebSocketFrame(true, WSOpcodeBinary, data, false)
}

// WebSocketReader reads WebSocket frames from a reader
type WebSocketReader struct {
	reader io.Reader
	buffer *bytes.Buffer
	state  *WebSocketState
}

// NewWebSocketReader creates a new WebSocket reader
func NewWebSocketReader(reader io.Reader) *WebSocketReader {
	return &WebSocketReader{
		reader: reader,
		buffer: &bytes.Buffer{},
		state:  &WebSocketState{},
	}
}

// ReadFrame reads a single frame
func (r *WebSocketReader) ReadFrame() (*WebSocketFrame, error) {
	// Try to parse from buffer first
	if r.buffer.Len() > 0 {
		frame, err := ParseWebSocketFrame(r.buffer.Bytes())
		if err == nil {
			// Remove parsed data from buffer
			r.buffer.Next(len(frame.Raw))
			return frame, nil
		}
	}

	// Read more data
	buf := make([]byte, 4096)
	n, err := r.reader.Read(buf)
	if err != nil {
		return nil, err
	}

	r.buffer.Write(buf[:n])

	// Try to parse again
	frame, err := ParseWebSocketFrame(r.buffer.Bytes())
	if err != nil {
		return nil, err
	}

	// Remove parsed data from buffer
	r.buffer.Next(len(frame.Raw))
	return frame, nil
}

// Read reads data using the callback interface
func (r *WebSocketReader) Read(cb func(opaque any, opcode int, data []byte, len int), opaque any) int {
	return WebSocketParse(r.buffer, cb, opaque, r.state)
}

// WebSocketWriter writes WebSocket frames to a writer
type WebSocketWriter struct {
	writer io.Writer
	masked bool
}

// NewWebSocketWriter creates a new WebSocket writer
func NewWebSocketWriter(writer io.Writer, masked bool) *WebSocketWriter {
	return &WebSocketWriter{
		writer: writer,
		masked: masked,
	}
}

// WriteFrame writes a frame
func (w *WebSocketWriter) WriteFrame(frame *WebSocketFrame) error {
	data := BuildWebSocketFrame(frame.Fin, frame.Opcode, frame.Payload, w.masked)
	_, err := w.writer.Write(data)
	return err
}

// WriteText writes a text frame
func (w *WebSocketWriter) WriteText(text string) error {
	return w.WriteFrame(&WebSocketFrame{
		Fin:     true,
		Opcode:  WSOpcodeText,
		Payload: []byte(text),
	})
}

// WriteBinary writes a binary frame
func (w *WebSocketWriter) WriteBinary(data []byte) error {
	return w.WriteFrame(&WebSocketFrame{
		Fin:     true,
		Opcode:  WSOpcodeBinary,
		Payload: data,
	})
}

// WriteClose writes a close frame
func (w *WebSocketWriter) WriteClose(code int, reason string) error {
	data := WebSocketCloseFrame(code, reason)
	_, err := w.writer.Write(data)
	return err
}

// WritePing writes a ping frame
func (w *WebSocketWriter) WritePing(data []byte) error {
	frame := WebSocketPingFrame(data)
	_, err := w.writer.Write(frame)
	return err
}

// WritePong writes a pong frame
func (w *WebSocketWriter) WritePong(data []byte) error {
	frame := WebSocketPongFrame(data)
	_, err := w.writer.Write(frame)
	return err
}
