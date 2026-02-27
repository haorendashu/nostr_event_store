package index

import "hash/crc64"

const (
	indexMagic        = 0x494E4458
	indexVersion      = 2 // Bumped to v2 for length-prefixed search index keys
	indexHeaderFormat = 1

	indexTypePrimary    = 1
	indexTypeAuthorTime = 2
	indexTypeSearch     = 3
	indexTypeKindTime   = 4

	// Exported index type constants
	IndexTypePrimary    = indexTypePrimary
	IndexTypeAuthorTime = indexTypeAuthorTime
	IndexTypeSearch     = indexTypeSearch
	IndexTypeKindTime   = indexTypeKindTime

	nodeTypeLeaf     = 0x00
	nodeTypeInternal = 0x01
)

var crc64Table = crc64.MakeTable(crc64.ECMA)
