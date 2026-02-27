package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc64"

	"github.com/haorendashu/nostr_event_store/src/types"
)

type btreeNode struct {
	nodeType byte
	offset   uint64
	keys     [][]byte
	values   []types.RecordLocation
	children []uint64
	next     uint64
	prev     uint64
	dirty    bool
}

func (n *btreeNode) isLeaf() bool {
	return n.nodeType == nodeTypeLeaf
}

func (n *btreeNode) cloneKey(key []byte) []byte {
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	return keyCopy
}

func (n *btreeNode) leafSize(pageSize uint32) int {
	size := 1 + 2 + 1
	for i, key := range n.keys {
		size += 2 + len(key)
		_ = i
		size += 8
	}
	size += 8 + 8 + 8
	return size
}

func (n *btreeNode) internalSize(pageSize uint32) int {
	size := 1 + 2 + 1
	if len(n.children) > 0 {
		size += 8
	}
	for i, key := range n.keys {
		size += 2 + len(key)
		_ = i
		size += 8
	}
	size += 8
	return size
}

func serializeNode(n *btreeNode, pageSize uint32) ([]byte, error) {
	buf := make([]byte, pageSize)
	pos := 0
	buf[pos] = n.nodeType
	pos++
	binary.BigEndian.PutUint16(buf[pos:pos+2], uint16(len(n.keys)))
	pos += 2
	buf[pos] = 0
	pos++

	if n.isLeaf() {
		if len(n.values) != len(n.keys) {
			return nil, fmt.Errorf("leaf node values mismatch: keys=%d values=%d", len(n.keys), len(n.values))
		}
		for i, key := range n.keys {
			if pos+2+len(key)+8 > int(pageSize)-24 {
				return nil, fmt.Errorf("leaf node overflow")
			}
			binary.BigEndian.PutUint16(buf[pos:pos+2], uint16(len(key)))
			pos += 2
			copy(buf[pos:pos+len(key)], key)
			pos += len(key)

			binary.BigEndian.PutUint32(buf[pos:pos+4], n.values[i].SegmentID)
			binary.BigEndian.PutUint32(buf[pos+4:pos+8], n.values[i].Offset)
			pos += 8
		}

		binary.BigEndian.PutUint64(buf[int(pageSize)-24:int(pageSize)-16], n.next)
		binary.BigEndian.PutUint64(buf[int(pageSize)-16:int(pageSize)-8], n.prev)
		checksum := crc64.Checksum(buf[:int(pageSize)-8], crc64Table)
		binary.BigEndian.PutUint64(buf[int(pageSize)-8:], checksum)
		return buf, nil
	}

	if len(n.children) != len(n.keys)+1 {
		return nil, fmt.Errorf("internal node children mismatch")
	}
	binary.BigEndian.PutUint64(buf[pos:pos+8], n.children[0])
	pos += 8
	for i, key := range n.keys {
		if pos+2+len(key)+8 > int(pageSize)-8 {
			return nil, fmt.Errorf("internal node overflow")
		}
		binary.BigEndian.PutUint16(buf[pos:pos+2], uint16(len(key)))
		pos += 2
		copy(buf[pos:pos+len(key)], key)
		pos += len(key)
		binary.BigEndian.PutUint64(buf[pos:pos+8], n.children[i+1])
		pos += 8
	}

	checksum := crc64.Checksum(buf[:int(pageSize)-8], crc64Table)
	binary.BigEndian.PutUint64(buf[int(pageSize)-8:], checksum)
	return buf, nil
}

func deserializeNode(offset uint64, pageSize uint32, buf []byte) (*btreeNode, error) {
	if uint32(len(buf)) != pageSize {
		return nil, fmt.Errorf("invalid node page size: %d", len(buf))
	}
	stored := binary.BigEndian.Uint64(buf[int(pageSize)-8:])
	calc := crc64.Checksum(buf[:int(pageSize)-8], crc64Table)
	if stored != calc {
		return nil, fmt.Errorf("node checksum mismatch")
	}

	pos := 0
	nodeType := buf[pos]
	pos++
	keyCount := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
	pos += 2
	pos++

	node := &btreeNode{
		nodeType: nodeType,
		offset:   offset,
		keys:     make([][]byte, 0, keyCount),
	}

	if nodeType == nodeTypeLeaf {
		node.values = make([]types.RecordLocation, 0, keyCount)
		for i := 0; i < keyCount; i++ {
			if pos+2 > len(buf) {
				return nil, fmt.Errorf("leaf node parse overflow")
			}
			keyLen := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
			pos += 2
			if pos+keyLen+8 > len(buf) {
				return nil, fmt.Errorf("leaf node parse overflow")
			}
			key := make([]byte, keyLen)
			copy(key, buf[pos:pos+keyLen])
			pos += keyLen

			segID := binary.BigEndian.Uint32(buf[pos : pos+4])
			offsetVal := binary.BigEndian.Uint32(buf[pos+4 : pos+8])
			pos += 8
			node.keys = append(node.keys, key)
			node.values = append(node.values, types.RecordLocation{SegmentID: segID, Offset: offsetVal})
		}
		node.next = binary.BigEndian.Uint64(buf[int(pageSize)-24 : int(pageSize)-16])
		node.prev = binary.BigEndian.Uint64(buf[int(pageSize)-16 : int(pageSize)-8])
		return node, nil
	}

	if nodeType != nodeTypeInternal {
		return nil, fmt.Errorf("unknown node type: %d", nodeType)
	}

	node.children = make([]uint64, 0, keyCount+1)
	if pos+8 > len(buf) {
		return nil, fmt.Errorf("internal node parse overflow")
	}
	child0 := binary.BigEndian.Uint64(buf[pos : pos+8])
	pos += 8
	node.children = append(node.children, child0)

	for i := 0; i < keyCount; i++ {
		if pos+2 > len(buf) {
			return nil, fmt.Errorf("internal node parse overflow")
		}
		keyLen := int(binary.BigEndian.Uint16(buf[pos : pos+2]))
		pos += 2
		if pos+keyLen+8 > len(buf) {
			return nil, fmt.Errorf("internal node parse overflow")
		}
		key := make([]byte, keyLen)
		copy(key, buf[pos:pos+keyLen])
		pos += keyLen
		child := binary.BigEndian.Uint64(buf[pos : pos+8])
		pos += 8
		node.keys = append(node.keys, key)
		node.children = append(node.children, child)
	}

	return node, nil
}

func compareKeys(a []byte, b []byte) int {
	return bytes.Compare(a, b)
}
