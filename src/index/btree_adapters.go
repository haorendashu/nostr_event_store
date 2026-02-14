package index

import (
	"github.com/haorendashu/nostr_event_store/src/cache"
)

// btreeNodeAdapter adapts btreeNode to implement cache.BTreeNode interface
type btreeNodeAdapter struct {
	node *btreeNode
}

func newBTreeNodeAdapter(node *btreeNode) cache.BTreeNode {
	return &btreeNodeAdapter{node: node}
}

func (a *btreeNodeAdapter) Offset() uint64 {
	return a.node.offset
}

func (a *btreeNodeAdapter) IsDirty() bool {
	return a.node.dirty
}

func (a *btreeNodeAdapter) SetDirty(dirty bool) {
	a.node.dirty = dirty
}

func (a *btreeNodeAdapter) Serialize(pageSize uint32) ([]byte, error) {
	return serializeNode(a.node, pageSize)
}

// indexFileAdapter adapts indexFile to implement cache.NodeWriter interface
type indexFileAdapter struct {
	file *indexFile
}

func newIndexFileAdapter(file *indexFile) cache.NodeWriter {
	return &indexFileAdapter{file: file}
}

func (a *indexFileAdapter) WriteNodePage(offset uint64, buf []byte) error {
	return a.file.writeNodePage(offset, buf)
}

func (a *indexFileAdapter) PageSize() uint32 {
	return a.file.pageSize
}
