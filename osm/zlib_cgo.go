//go:build cgo

package osm

import (
	"fmt"

	"github.com/DataDog/czlib"
)

var newZlibReader = czlib.NewReader

func init() {
	fmt.Println("CGO!")
}
