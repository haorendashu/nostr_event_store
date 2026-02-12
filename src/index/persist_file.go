package index

import (
	"encoding/binary"
	"fmt"
	"hash/crc64"
	"os"
)

type indexHeader struct {
	Magic      uint32
	IndexType  uint32
	Version    uint64
	RootOffset uint64
	NodeCount  uint64
	PageSize   uint32
	Format     uint32
	EntryCount uint64
}

type indexFile struct {
	file     *os.File
	path     string
	pageSize uint32
	header   indexHeader
}

func openIndexFile(path string, indexType uint32, pageSize uint32, createIfMissing bool) (*indexFile, error) {
	flags := os.O_RDWR
	if createIfMissing {
		flags |= os.O_CREATE
	}

	file, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return nil, fmt.Errorf("open index file: %w", err)
	}

	fi := &indexFile{file: file, path: path, pageSize: pageSize}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat index file: %w", err)
	}

	if info.Size() == 0 {
		fi.header = indexHeader{
			Magic:     indexMagic,
			IndexType: indexType,
			Version:   indexVersion,
			PageSize:  pageSize,
			Format:    indexHeaderFormat,
		}
		if err := fi.writeHeader(); err != nil {
			file.Close()
			return nil, err
		}
		return fi, nil
	}

	if err := fi.readHeader(); err != nil {
		file.Close()
		return nil, err
	}
	if fi.header.Magic != indexMagic {
		file.Close()
		return nil, fmt.Errorf("invalid index magic: 0x%X", fi.header.Magic)
	}
	if fi.header.IndexType != indexType {
		file.Close()
		return nil, fmt.Errorf("index type mismatch: expected %d, got %d", indexType, fi.header.IndexType)
	}
	if fi.header.PageSize != pageSize {
		file.Close()
		return nil, fmt.Errorf("index page size mismatch: expected %d, got %d", pageSize, fi.header.PageSize)
	}
	if fi.header.Version != indexVersion {
		file.Close()
		return nil, fmt.Errorf("index version mismatch: expected %d, got %d (requires rebuild)", indexVersion, fi.header.Version)
	}

	return fi, nil
}

func (f *indexFile) headerSize() uint64 {
	return uint64(f.pageSize)
}

func (f *indexFile) readHeader() error {
	buf := make([]byte, f.pageSize)
	if _, err := f.file.ReadAt(buf, 0); err != nil {
		return fmt.Errorf("read index header: %w", err)
	}

	f.header.Magic = binary.BigEndian.Uint32(buf[0:4])
	f.header.IndexType = binary.BigEndian.Uint32(buf[4:8])
	f.header.Version = binary.BigEndian.Uint64(buf[8:16])
	f.header.RootOffset = binary.BigEndian.Uint64(buf[16:24])
	f.header.NodeCount = binary.BigEndian.Uint64(buf[24:32])
	f.header.PageSize = binary.BigEndian.Uint32(buf[32:36])
	f.header.Format = binary.BigEndian.Uint32(buf[36:40])
	f.header.EntryCount = binary.BigEndian.Uint64(buf[40:48])
	return nil
}

func (f *indexFile) writeHeader() error {
	buf := make([]byte, f.pageSize)
	binary.BigEndian.PutUint32(buf[0:4], f.header.Magic)
	binary.BigEndian.PutUint32(buf[4:8], f.header.IndexType)
	binary.BigEndian.PutUint64(buf[8:16], f.header.Version)
	binary.BigEndian.PutUint64(buf[16:24], f.header.RootOffset)
	binary.BigEndian.PutUint64(buf[24:32], f.header.NodeCount)
	binary.BigEndian.PutUint32(buf[32:36], f.header.PageSize)
	binary.BigEndian.PutUint32(buf[36:40], f.header.Format)
	binary.BigEndian.PutUint64(buf[40:48], f.header.EntryCount)
	checksum := crc64.Checksum(buf[:f.pageSize-8], crc64Table)
	binary.BigEndian.PutUint64(buf[f.pageSize-8:], checksum)

	if _, err := f.file.WriteAt(buf, 0); err != nil {
		return fmt.Errorf("write index header: %w", err)
	}
	return nil
}

func (f *indexFile) allocateNodeOffset() uint64 {
	offset := f.headerSize() + uint64(f.header.NodeCount)*uint64(f.pageSize)
	f.header.NodeCount++
	return offset
}

func (f *indexFile) syncHeader() error {
	return f.writeHeader()
}

func (f *indexFile) readNodePage(offset uint64) ([]byte, error) {
	buf := make([]byte, f.pageSize)
	if _, err := f.file.ReadAt(buf, int64(offset)); err != nil {
		return nil, fmt.Errorf("read index node: %w", err)
	}
	return buf, nil
}

func (f *indexFile) writeNodePage(offset uint64, buf []byte) error {
	if uint32(len(buf)) != f.pageSize {
		return fmt.Errorf("invalid node page size: %d", len(buf))
	}
	if _, err := f.file.WriteAt(buf, int64(offset)); err != nil {
		return fmt.Errorf("write index node: %w", err)
	}
	return nil
}

func (f *indexFile) sync() error {
	if err := f.file.Sync(); err != nil {
		return fmt.Errorf("sync index file: %w", err)
	}
	return nil
}

func (f *indexFile) close() error {
	if err := f.file.Close(); err != nil {
		return fmt.Errorf("close index file: %w", err)
	}
	return nil
}
