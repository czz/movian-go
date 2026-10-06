package htsmsg

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Binary serialization/deserialization for HTSMsg

// DeserializeBinary deserializes a message from binary format
func DeserializeBinary(data []byte) (*HTSMsg, error) {
	if len(data) < 4 {
		return nil, errors.New("data too short")
	}

	// Read length prefix (4 bytes big-endian)
	length := binary.BigEndian.Uint32(data[0:4])
	if uint32(len(data)) < length+4 {
		return nil, errors.New("data length mismatch")
	}

	msg := NewMap()
	buf := data[4 : 4+length]

	if err := deserialize0(msg, buf); err != nil {
		msg.Release()
		return nil, err
	}

	return msg, nil
}

// deserialize0 is the recursive deserialization implementation
func deserialize0(msg *HTSMsg, buf []byte) error {
	offset := 0

	for offset+6 <= len(buf) {
		// Read header: type (1) + namelen (1) + datalen (4)
		fieldType := buf[offset]
		namelen := buf[offset+1]
		datalen := binary.BigEndian.Uint32(buf[offset+2 : offset+6])
		offset += 6

		// Check if we have enough data
		if offset+int(namelen)+int(datalen) > len(buf) {
			return errors.New("insufficient data for field")
		}

		// Read name
		var name string
		if namelen > 0 {
			name = string(buf[offset : offset+int(namelen)])
			offset += int(namelen)
		}

		// Create field
		f := &HTSMsgField{
			name:      name,
			fieldType: fieldType,
			flags:     0,
		}

		if namelen > 0 {
			f.flags = HmfNameAlloced
		}

		// Read data based on type
		switch fieldType {
		case HmfStr:
			f.strValue = string(buf[offset : offset+int(datalen)])
			f.flags |= HmfAlloced
			offset += int(datalen)

		case HmfBin:
			f.binData = buf[offset : offset+int(datalen)]
			offset += int(datalen)

		case HmfS64:
			// Read variable-length big-endian integer
			u64 := uint64(0)
			for i := int(datalen) - 1; i >= 0; i-- {
				u64 = (u64 << 8) | uint64(buf[offset+i])
			}
			f.s64Value = int64(u64)
			offset += int(datalen)

		case HmfMap:
			sub := NewMap()
			f.childs = sub
			subData := buf[offset : offset+int(datalen)]
			if err := deserialize0(sub, subData); err != nil {
				return err
			}
			offset += int(datalen)

		case HmfList:
			sub := NewList()
			f.childs = sub
			subData := buf[offset : offset+int(datalen)]
			if err := deserialize0(sub, subData); err != nil {
				return err
			}
			offset += int(datalen)

		default:
			// C: htsmsg_binary_des0 default → -1 (HMF_DBL included —
			// the binary format has no double encoding).
			return fmt.Errorf("unknown field type: %d", fieldType)
		}

		msg.fields = append(msg.fields, f)
	}

	return nil
}

// SerializeBinary serializes a message to binary format.
// Returns an error if the message contains double fields,
// matching C's behavior (abort on HMF_DBL).
func SerializeBinary(msg *HTSMsg, maxlen int) ([]byte, error) {
	if msg == nil {
		return nil, errors.New("nil message")
	}

	// Check for unsupported field types (C aborts on these)
	if err := binaryCheckSupported(msg); err != nil {
		return nil, err
	}

	// Count required size
	length := binaryCount(msg)

	if length+4 > maxlen {
		return nil, errors.New("serialized data exceeds max length")
	}

	// Allocate buffer
	data := make([]byte, length+4)

	// Write length prefix
	binary.BigEndian.PutUint32(data[0:4], uint32(length))

	// Write message data
	binaryWrite(msg, data[4:])

	return data, nil
}

// binaryCheckSupported returns an error if the message contains
// field types not supported by C's binary format (HMF_DBL).
func binaryCheckSupported(msg *HTSMsg) error {
	for _, f := range msg.fields {
		if f.fieldType == HmfDbl {
			return errors.New("binary format does not support double fields")
		}
		if f.fieldType == HmfMap || f.fieldType == HmfList {
			if f.childs != nil {
				if err := binaryCheckSupported(f.childs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// binaryCount calculates the size needed for binary serialization
func binaryCount(msg *HTSMsg) int {
	size := 0

	for _, f := range msg.fields {
		size += 6 // header (type + namelen + datalen)
		size += len(f.name)

		switch f.fieldType {
		case HmfMap, HmfList:
			if f.childs != nil {
				size += binaryCount(f.childs)
			}
		case HmfStr:
			size += len(f.strValue)
		case HmfBin:
			size += len(f.binData)
		case HmfS64:
			u64 := uint64(f.s64Value)
			for u64 != 0 {
				size++
				u64 >>= 8
			}
		}
	}

	return size
}

// binaryWrite writes the message to binary format
func binaryWrite(msg *HTSMsg, data []byte) {
	offset := 0

	for _, f := range msg.fields {
		namelen := len(f.name)

		// Calculate data length
		var datalen int
		switch f.fieldType {
		case HmfMap, HmfList:
			if f.childs != nil {
				datalen = binaryCount(f.childs)
			}
		case HmfStr:
			datalen = len(f.strValue)
		case HmfBin:
			datalen = len(f.binData)
		case HmfS64:
			u64 := uint64(f.s64Value)
			for u64 != 0 {
				datalen++
				u64 >>= 8
			}
		}

		// Write header
		data[offset] = f.fieldType
		data[offset+1] = byte(namelen)
		binary.BigEndian.PutUint32(data[offset+2:offset+6], uint32(datalen))
		offset += 6

		// Write name
		if namelen > 0 {
			copy(data[offset:offset+namelen], f.name)
			offset += namelen
		}

		// Write data
		switch f.fieldType {
		case HmfMap, HmfList:
			if f.childs != nil {
				binaryWrite(f.childs, data[offset:offset+datalen])
			}
		case HmfStr:
			copy(data[offset:offset+datalen], f.strValue)
		case HmfBin:
			copy(data[offset:offset+datalen], f.binData)
		case HmfS64:
			u64 := uint64(f.s64Value)
			for i := range datalen {
				data[offset+i] = byte(u64)
				u64 >>= 8
			}
		}

		offset += datalen
	}
}
