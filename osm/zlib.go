//go:build !cgo && !cgo2

package osm

import "github.com/klauspost/compress/zlib"

var newZlibReader = zlib.NewReader
