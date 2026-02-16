//go:build cgo

package osm

import (
	"github.com/DataDog/czlib"
)

var newZlibReader = czlib.NewReader
