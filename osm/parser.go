package osm

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	unsafe "unsafe"
)

const maxBlobHeaderSize = 64 * 1024
const maxBlobSize = 32 * 1024 * 1024

type NodeFunc func(Node)
type WayFunc func(Way)
type RelationFunc func(Relation)

type Tag struct {
	Key, Val string
}

type Tags []Tag

// Has returns true if the key exists in the tag list.
func (tags Tags) Has(key string) bool {
	for _, tag := range tags {
		if tag.Key == key {
			return true
		}
	}
	return false
}

// Find returns the value of a key in the tag list, or an empty string if the key doesn't exist.
func (tags Tags) Find(key string) string {
	for _, tag := range tags {
		if tag.Key == key {
			return tag.Val
		}
	}
	return ""
}

// ToMap converts the tag list to a map.
func (tags Tags) ToMap() map[string]string {
	m := make(map[string]string, len(tags))
	for _, tag := range tags {
		m[tag.Key] = tag.Val
	}
	return m
}

func (tags Tags) Clone() Tags {
	tags = slices.Clone(tags)
	for i := 0; i < len(tags); i++ {
		tags[i].Key = strings.Clone(tags[i].Key)
		tags[i].Val = strings.Clone(tags[i].Val)
	}
	return tags
}

type Node struct {
	ID       uint64
	Lon, Lat float64
	Tags     Tags
}

// Own will copy the internal memory and is only required if you need to access the node's tags after the function callback.
func (o *Node) Own() {
	o.Tags = o.Tags.Clone()
}

type Way struct {
	ID   uint64
	Refs []uint64
	Tags Tags
}

// Own will copy the internal memory and is only required if you need to access the way's refs or tags after the function callback.
func (o *Way) Own() {
	o.Refs = slices.Clone(o.Refs)
	o.Tags = o.Tags.Clone()
}

type Type int

const (
	NodeType Type = iota
	WayType
	RelationType
)

type Member struct {
	ID   uint64
	Type Type
	Role string
}

type Relation struct {
	ID      uint64
	Members []Member
	Tags    Tags
}

// Own will copy the internal memory and is only required if you need to access the relation's members or tags after the function callback.
func (o *Relation) Own() {
	o.Members = slices.Clone(o.Members)
	for i := 0; i < len(o.Members); i++ {
		o.Members[i].Role = strings.Clone(o.Members[i].Role)
	}
	o.Tags = o.Tags.Clone()
}

type blobContent struct {
	nodes, ways, relations bool
}

type Parser struct {
	r       io.ReadSeeker
	Workers int
	pos     int64

	mu           sync.Mutex
	blobContents map[int]blobContent

	blobPool sync.Pool
	zlibPool sync.Pool
}

// NewParser returns a new parser. The default amount of workers is set to runtime.GOMAXPROCS(0), or the amount of CPU threads. You can set this manually by setting the Workers field.
func NewParser(r io.ReadSeeker) *Parser {
	return &Parser{
		r:       r,
		Workers: runtime.GOMAXPROCS(0),

		blobContents: map[int]blobContent{},

		blobPool: sync.Pool{
			New: func() any {
				return []byte(nil)
			},
		},
		zlibPool: sync.Pool{
			New: func() any {
				return nil
			},
		},
	}
}

// Pos returns the current parsing progress in bytes of the file. Divide by the total file size (obtained beforehand using os.Stat for example) to calculate the parsing progress. Can be called concurrently.
func (z *Parser) Pos() int64 {
	return atomic.LoadInt64(&z.pos)
}

func readVarint(b []byte) (v uint64, n int) {
	//return binary.Uvarint(buf)

	// unrolled
	var y uint64
	if len(b) <= 0 {
		return 0, 0
	}
	v = uint64(b[0])
	if v < 0x80 {
		return v, 1
	}
	v -= 0x80

	if len(b) <= 1 {
		return 0, 0
	}
	y = uint64(b[1])
	v += y << 7
	if y < 0x80 {
		return v, 2
	}
	v -= 0x80 << 7

	if len(b) <= 2 {
		return 0, 0
	}
	y = uint64(b[2])
	v += y << 14
	if y < 0x80 {
		return v, 3
	}
	v -= 0x80 << 14

	if len(b) <= 3 {
		return 0, 0
	}
	y = uint64(b[3])
	v += y << 21
	if y < 0x80 {
		return v, 4
	}
	v -= 0x80 << 21

	if len(b) <= 4 {
		return 0, 0
	}
	y = uint64(b[4])
	v += y << 28
	if y < 0x80 {
		return v, 5
	}
	v -= 0x80 << 28

	if len(b) <= 5 {
		return 0, 0
	}
	y = uint64(b[5])
	v += y << 35
	if y < 0x80 {
		return v, 6
	}
	v -= 0x80 << 35

	if len(b) <= 6 {
		return 0, 0
	}
	y = uint64(b[6])
	v += y << 42
	if y < 0x80 {
		return v, 7
	}
	v -= 0x80 << 42

	if len(b) <= 7 {
		return 0, 0
	}
	y = uint64(b[7])
	v += y << 49
	if y < 0x80 {
		return v, 8
	}
	v -= 0x80 << 49

	if len(b) <= 8 {
		return 0, 0
	}
	y = uint64(b[8])
	v += y << 56
	if y < 0x80 {
		return v, 9
	}
	v -= 0x80 << 56

	if len(b) <= 9 {
		return 0, 0
	}
	y = uint64(b[9])
	v += y << 63
	if y < 2 {
		return v, 10
	}
	return 0, 0
}

func readSint(buf []byte) (int64, int) {
	val, index := readVarint(buf)
	return int64(val>>1) ^ int64(val)<<63>>63, index
}

func readField(buf []byte) (uint64, int, int) {
	val, index := readVarint(buf)
	return val >> 3, int(val & 7), index
}

func skipField(buf []byte, wireType int) int {
	switch wireType {
	case 0:
		for i := 0; i < 9; i++ {
			if len(buf) <= i || (buf[i]&0x80) != 0 {
				return i + 1
			}
		}
		return 10
	case 1:
		return 8
	case 2:
		size, n := readVarint(buf)
		return int(size) + n
	case 5:
		return 4
	}
	return 0
}

type Blob struct {
	Type    int
	Data    []byte
	RawSize int

	index    int
	datasize int64
}

func (z *Parser) blob(buf []byte) (Blob, error) {
	// BlobHeaderLength
	if _, err := io.ReadFull(z.r, buf[:4]); err != nil {
		return Blob{}, err
	}
	headerLength := binary.BigEndian.Uint32(buf[:4])
	if maxBlobHeaderSize < headerLength {
		return Blob{}, fmt.Errorf("BlobHeader length is too big")
	}

	// BlobHeader
	buf = buf[:headerLength]
	if _, err := io.ReadFull(z.r, buf); err != nil {
		return Blob{}, err
	}
	i := 0
	var typ []byte
	var datasize int64
	var hasDatasize bool
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field == 0 {
			return Blob{}, fmt.Errorf("invalid BlobHeader")
		} else if field == 1 {
			// type
			if wireType != 2 {
				return Blob{}, fmt.Errorf("invalid type in BlobHeader")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return Blob{}, fmt.Errorf("invalid type in BlobHeader")
			}
			typ = buf[i : i+int(size)]
			i += int(size)
		} else if field == 3 {
			// datasize
			if wireType != 0 {
				return Blob{}, fmt.Errorf("invalid datasize in BlobHeader")
			}
			val, n := readVarint(buf[i:])
			i += n
			if n == 0 {
				return Blob{}, fmt.Errorf("invalid datasize in BlobHeader")
			} else if maxBlobSize < val {
				return Blob{}, fmt.Errorf("datasize in BlobHeader is too big")
			}
			datasize = int64(val)
			hasDatasize = true
		} else {
			n := skipField(buf[i:], wireType)
			i += n
			if n == 0 {
				return Blob{}, fmt.Errorf("invalid field %v in BlobHeader", field)
			}
		}
	}
	if i != len(buf) || typ == nil || !hasDatasize {
		return Blob{}, fmt.Errorf("invalid BlobHeader")
	}
	isData := bytes.Equal(typ, []byte("OSMData"))
	atomic.AddInt64(&z.pos, 4+int64(headerLength))

	// Blob
	buf = z.blobPool.Get().([]byte)
	if int64(cap(buf)) < datasize {
		buf = make([]byte, datasize)
	} else {
		buf = buf[:datasize]
	}
	if _, err := io.ReadFull(z.r, buf); err != nil {
		return Blob{}, err
	} else if datasize == 0 || !isData {
		return Blob{}, nil
	}
	i = 0
	blob := Blob{
		datasize: datasize,
	}
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field == 0 {
			return Blob{}, fmt.Errorf("invalid Blob")
		} else if field == 2 {
			// raw_size
			if wireType != 0 {
				return Blob{}, fmt.Errorf("invalid raw_size in Blob")
			}
			val, n := readVarint(buf[i:])
			i += n
			if n == 0 {
				return Blob{}, fmt.Errorf("invalid raw_size in Blob")
			} else if maxBlobSize < val {
				return Blob{}, fmt.Errorf("raw_size in Blob is too big")
			}
			blob.RawSize = int(val)
		} else if field == 1 || field == 3 || field == 4 || field == 5 || field == 6 || field == 7 {
			// raw, zlib_data, lzma_data, bzip2_data, lz4_data, and zstd_data
			if wireType != 2 {
				return Blob{}, fmt.Errorf("invalid field %v in Blob", field)
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return Blob{}, fmt.Errorf("invalid field %v in Blob", field)
			}
			blob.Type = int(field)
			blob.Data = buf[i : i+int(size)]
			i += int(size)
		} else {
			n := skipField(buf[i:], wireType)
			i += n
			if n == 0 {
				return Blob{}, fmt.Errorf("invalid field %v in Blob", field)
			}
		}
	}
	if i != len(buf) || blob.Data == nil {
		return Blob{}, fmt.Errorf("invalid Blob")
	}
	return blob, nil
}

// reuse buffers to reduce allocations and thus GC pressure
type buffers struct {
	stringTable []string
	tags        Tags

	// node buffers
	nodeIDs    []uint64
	lats, lons []int64
	keyVals    []uint32
	keyValEnds []int

	// way and relation buffers
	keys, vals []uint32
	roles      []int32
	refs       []uint64
	types      []int8
	members    []Member
}

type Block struct {
	StringTable     []byte
	PrimitiveGroups [][]byte
	Granularity     int64
	LatOffset       int64
	LonOffset       int64
}

type ZlibResetter interface {
	Reset(io.Reader, []byte) error
}

func (z *Parser) block(blob Blob) (Block, []byte, error) {
	var buf []byte
	switch blob.Type {
	case 1:
		buf = blob.Data
	case 3:
		// zlib
		var r io.ReadCloser
		var err error
		if item := z.zlibPool.Get(); item == nil {
			r, err = newZlibReader(bytes.NewReader(blob.Data))
		} else {
			r = item.(io.ReadCloser)
			err = item.(ZlibResetter).Reset(bytes.NewReader(blob.Data), nil)
		}
		if err != nil {
			return Block{}, nil, fmt.Errorf("invalid zlib compression in Blob: %w", err)
		}
		defer r.Close()

		if 0 < blob.RawSize {
			buf = z.blobPool.Get().([]byte)
			if cap(buf) < blob.RawSize {
				buf = make([]byte, blob.RawSize)
			} else {
				buf = buf[:blob.RawSize]
			}
			if n, err := io.ReadFull(r, buf); err != nil {
				return Block{}, nil, fmt.Errorf("invalid zlib compression in Blob: %w", err)
			} else if n != blob.RawSize {
				return Block{}, nil, fmt.Errorf("invalid zlib compression in Blob")
			}
		} else if buf, err = io.ReadAll(r); err != nil {
			return Block{}, nil, fmt.Errorf("invalid zlib compression in Blob: %w", err)
		}
		z.blobPool.Put(blob.Data)
		if _, ok := r.(ZlibResetter); ok {
			z.zlibPool.Put(r)
		}
	case 4:
		// LZMA
		// TODO
		return Block{}, nil, fmt.Errorf("unsupported LZMA compression in Blob")
	case 5:
		// bzip2
		return Block{}, nil, fmt.Errorf("unsupported bzip2 compression in Blob")
	case 6:
		// LZ4
		// TODO: https://github.com/pierrec/lz4
		return Block{}, nil, fmt.Errorf("unsupported LZ4 compression in Blob")
	case 7:
		// Zstandard
		// TODO: https://github.com/klauspost/compress/tree/master/zstd
		return Block{}, nil, fmt.Errorf("unsupported Zstandard compression in Blob")
	default:
		return Block{}, nil, fmt.Errorf("unsupported block compression in Blob")
	}

	i := 0
	block := Block{
		Granularity: 100,
	}
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field == 0 {
			return Block{}, nil, fmt.Errorf("invalid PrimitiveBlock")
		} else if field == 1 {
			// stringtable
			if wireType != 2 {
				return Block{}, nil, fmt.Errorf("invalid StringTable in PrimitiveBlock")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return Block{}, nil, fmt.Errorf("invalid StringTable in PrimitiveBlock")
			}
			block.StringTable = buf[i : i+int(size)]
			i += int(size)
		} else if field == 2 {
			// primitivegroup
			if wireType != 2 {
				return Block{}, nil, fmt.Errorf("invalid PrimitiveGroup in PrimitiveBlock")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return Block{}, nil, fmt.Errorf("invalid PrimitiveGroup in PrimitiveBlock")
			} else if 0 < size {
				block.PrimitiveGroups = append(block.PrimitiveGroups, buf[i:i+int(size)])
			}
			i += int(size)
		} else if field == 17 || field == 19 || field == 20 {
			// granularity, lat_offset, and lon_offset
			val, n := readVarint(buf[i:])
			i += n
			if n == 0 {
				return Block{}, nil, fmt.Errorf("invalid field %v in PrimitiveBlock", field)
			}
			switch field {
			case 17:
				block.Granularity = int64(val)
			case 19:
				block.LatOffset = int64(val)
			case 20:
				block.LonOffset = int64(val)
			}
		} else {
			n := skipField(buf[i:], wireType)
			i += n
			if n == 0 {
				return Block{}, nil, fmt.Errorf("invalid field %v in PrimitiveBlock", field)
			}
		}
	}
	if i != len(buf) || block.StringTable == nil {
		return Block{}, nil, fmt.Errorf("invalid PrimitiveBlock")
	}
	return block, buf, nil
}

func (z *Parser) stringTable(block Block, buffers *buffers) error {
	i := 0
	buf := block.StringTable
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field == 0 {
			return fmt.Errorf("invalid StringTable")
		} else if field == 1 {
			// s
			if wireType != 2 {
				return fmt.Errorf("invalid string in StringTable")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return fmt.Errorf("invalid string in StringTable")
			}
			buffers.stringTable = append(buffers.stringTable, unsafe.String(&buf[i], size))
			i += int(size)
		} else {
			n := skipField(buf[i:], wireType)
			i += n
			if n == 0 {
				return fmt.Errorf("invalid field %v in StringTable", field)
			}
		}
	}
	if math.MaxUint32 < len(buffers.stringTable) {
		return fmt.Errorf("StringTable too big")
	}
	return nil
}

func (z *Parser) nodes(block Block, buffers *buffers, buf []byte, fn NodeFunc) error {
	if len(buffers.stringTable) == 0 {
		if err := z.stringTable(block, buffers); err != nil {
			return err
		}
	}

	field, wireType, n := readField(buf)
	i := n
	if n == 0 || field != 2 || wireType != 2 {
		return fmt.Errorf("invalid DenseNodes")
	}
	size, n := readVarint(buf[i:])
	i += n
	if n == 0 || math.MaxInt < size || i+int(size) != len(buf) {
		return fmt.Errorf("invalid DenseNodes")
	}
	buffers.nodeIDs = buffers.nodeIDs[:0]
	buffers.lats = buffers.lats[:0]
	buffers.lons = buffers.lons[:0]
	buffers.keyValEnds = buffers.keyValEnds[:0]
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field == 0 {
			return fmt.Errorf("invalid DenseNodes3")
		} else if field == 1 {
			// id
			if wireType != 2 {
				return fmt.Errorf("invalid ids in DenseNodes")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return fmt.Errorf("invalid ids in DenseNodes")
			}
			var id uint64
			buf2 := buf[:i+int(size)]
			buffers.nodeIDs = buffers.nodeIDs[:0]
			for i < len(buf2) {
				delta, n := readSint(buf2[i:])
				i += n
				if n == 0 {
					return fmt.Errorf("invalid id in DenseNodes")
				}
				if 0 <= delta {
					if math.MaxUint64-id < uint64(delta) {
						return fmt.Errorf("invalid id in DenseNodes")
					}
					id += uint64(delta)
				} else {
					if id <= uint64(-delta) {
						return fmt.Errorf("invalid id in DenseNodes")
					}
					id -= uint64(-delta)
				}
				buffers.nodeIDs = append(buffers.nodeIDs, id)
			}
			if i != len(buf2) {
				return fmt.Errorf("invalid ids in DenseNodes")
			}
		} else if field == 8 || field == 9 {
			// lat and lon
			if wireType != 2 {
				return fmt.Errorf("invalid field %v in DenseNodes", field)
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return fmt.Errorf("invalid field %v in DenseNodes", field)
			}
			var coord int64
			var coords *[]int64
			if field == 8 {
				coords = &buffers.lats
				buffers.lats = buffers.lats[:0]
			} else {
				coords = &buffers.lons
				buffers.lons = buffers.lons[:0]
			}
			buf2 := buf[:i+int(size)]
			for i < len(buf2) {
				delta, n := readSint(buf2[i:])
				coord += delta
				i += n
				if n == 0 {
					return fmt.Errorf("invalid field %v in DenseNodes", field)
				}
				*coords = append(*coords, coord)
			}
			if i != len(buf2) {
				return fmt.Errorf("invalid field %v in DenseNodes", field)
			}
		} else if field == 10 {
			// keys_vals
			if wireType != 2 {
				return fmt.Errorf("invalid key_vals in DenseNodes")
			}
			size, n := readVarint(buf[i:])
			i += n
			if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
				return fmt.Errorf("invalid key_vals in DenseNodes")
			}
			buffers.keyVals = buffers.keyVals[:0]
			if cap(buffers.keyValEnds) < len(buffers.nodeIDs) {
				buffers.keyValEnds = make([]int, 0, len(buffers.nodeIDs))
			} else {
				buffers.keyValEnds = buffers.keyValEnds[:0]
			}
			buf2 := buf[:i+int(size)]
			for i < len(buf2) {
				key, n := readVarint(buf2[i:])
				i += n
				if n == 0 || uint64(len(buffers.stringTable)) <= key {
					return fmt.Errorf("invalid key in DenseNodes")
				} else if key == 0 {
					buffers.keyValEnds = append(buffers.keyValEnds, len(buffers.keyVals))
					continue
				}
				val, n := readVarint(buf2[i:])
				i += n
				if n == 0 || uint64(len(buffers.stringTable)) <= val {
					return fmt.Errorf("invalid val in DenseNodes")
				}
				buffers.keyVals = append(buffers.keyVals, uint32(key), uint32(val))
			}
			if i != len(buf2) {
				return fmt.Errorf("invalid key_vals in DenseNodes")
			}
		} else {
			n := skipField(buf[i:], wireType)
			i += n
			if n == 0 {
				return fmt.Errorf("invalid field %v in Block", field)
			}
		}
	}
	if i != len(buf) || len(buffers.nodeIDs) != len(buffers.lats) || len(buffers.nodeIDs) != len(buffers.lons) || 0 < len(buffers.keyValEnds) && len(buffers.nodeIDs) != len(buffers.keyValEnds) {
		return fmt.Errorf("invalid number of DenseNodes")
	}

	tagsIndex := 1
	node := Node{}
	for index, id := range buffers.nodeIDs {
		buffers.tags = buffers.tags[:0]
		if 0 < len(buffers.keyValEnds) {
			for n := buffers.keyValEnds[index]; tagsIndex < n; tagsIndex += 2 {
				buffers.tags = append(buffers.tags, Tag{
					Key: buffers.stringTable[buffers.keyVals[tagsIndex-1]],
					Val: buffers.stringTable[buffers.keyVals[tagsIndex]],
				})
			}
		}

		node.ID = id
		node.Lon = 1e-9 * float64(block.LonOffset+block.Granularity*buffers.lons[index])
		node.Lat = 1e-9 * float64(block.LatOffset+block.Granularity*buffers.lats[index])
		node.Tags = buffers.tags
		fn(node)
	}
	return nil
}

func (z *Parser) ways(block Block, buffers *buffers, buf []byte, fn WayFunc) error {
	if len(buffers.stringTable) == 0 {
		if err := z.stringTable(block, buffers); err != nil {
			return err
		}
	}

	i := 0
	way := Way{}
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field != 3 || wireType != 2 {
			return fmt.Errorf("invalid Ways")
		}
		size, n := readVarint(buf[i:])
		i += n
		if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
			return fmt.Errorf("invalid Ways")
		}
		way.ID = 0
		buffers.keys = buffers.keys[:0]
		buffers.vals = buffers.vals[:0]
		buffers.refs = buffers.refs[:0]
		buf2 := buf[:i+int(size)]
		for i < len(buf2) {
			field, wireType, n := readField(buf2[i:])
			i += n
			if n == 0 || field == 0 {
				return fmt.Errorf("invalid Way")
			} else if field == 1 {
				// id
				if wireType != 0 {
					return fmt.Errorf("invalid id in Way")
				}
				id, n := readVarint(buf2[i:])
				i += n
				if n == 0 {
					return fmt.Errorf("invalid id in Way")
				}
				way.ID = id
			} else if field == 2 {
				// keys
				if wireType != 2 {
					return fmt.Errorf("invalid keys in Way")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid keys in Way")
				}
				buf3 := buf2[:i+int(size)]
				buffers.keys = buffers.keys[:0]
				for i < len(buf3) {
					key, n := readVarint(buf3[i:])
					i += n
					if n == 0 || uint64(len(buffers.stringTable)) <= key {
						return fmt.Errorf("invalid key in Way")
					}
					buffers.keys = append(buffers.keys, uint32(key))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid keys in Way")
				}
			} else if field == 3 {
				// vals
				if wireType != 2 {
					return fmt.Errorf("invalid vals in Way")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid vals in Way")
				}
				buf3 := buf2[:i+int(size)]
				buffers.vals = buffers.vals[:0]
				for i < len(buf3) {
					val, n := readVarint(buf3[i:])
					i += n
					if n == 0 || uint64(len(buffers.stringTable)) <= val {
						return fmt.Errorf("invalid val in Way")
					}
					buffers.vals = append(buffers.vals, uint32(val))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid vals in Way")
				}
			} else if field == 8 {
				// refs
				if wireType != 2 {
					return fmt.Errorf("invalid refs in Way")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid refs in Way")
				}
				var ref uint64
				buf3 := buf2[:i+int(size)]
				buffers.refs = buffers.refs[:0]
				for i < len(buf3) {
					delta, n := readSint(buf3[i:])
					i += n
					if n == 0 {
						return fmt.Errorf("invalid ref in Way")
					}
					if 0 <= delta {
						if math.MaxUint64-ref < uint64(delta) {
							return fmt.Errorf("invalid ref in Way")
						}
						ref += uint64(delta)
					} else {
						if ref <= uint64(-delta) {
							return fmt.Errorf("invalid ref in Way")
						}
						ref -= uint64(-delta)
					}
					buffers.refs = append(buffers.refs, ref)
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid refs in Way")
				}
			} else {
				n := skipField(buf2[i:], wireType)
				i += n
				if n == 0 {
					return fmt.Errorf("invalid field %v in Way", field)
				}
			}
		}
		if i != len(buf2) || way.ID == 0 || len(buffers.keys) != len(buffers.vals) {
			return fmt.Errorf("invalid Way")
		}

		buffers.tags = buffers.tags[:0]
		for k := 0; k < len(buffers.keys); k++ {
			buffers.tags = append(buffers.tags, Tag{
				Key: buffers.stringTable[buffers.keys[k]],
				Val: buffers.stringTable[buffers.vals[k]],
			})
		}
		way.Refs = buffers.refs
		way.Tags = buffers.tags
		fn(way)
	}
	if i != len(buf) {
		return fmt.Errorf("invalid Ways")
	}
	return nil
}

func (z *Parser) relations(block Block, buffers *buffers, buf []byte, fn RelationFunc) error {
	if len(buffers.stringTable) == 0 {
		if err := z.stringTable(block, buffers); err != nil {
			return err
		}
	}

	i := 0
	relation := Relation{}
	for i < len(buf) {
		field, wireType, n := readField(buf[i:])
		i += n
		if n == 0 || field != 4 || wireType != 2 {
			return fmt.Errorf("invalid Relations")
		}
		size, n := readVarint(buf[i:])
		i += n
		if n == 0 || math.MaxInt < size || len(buf) < i+int(size) {
			return fmt.Errorf("invalid Relations")
		}
		relation.ID = 0
		buffers.keys = buffers.keys[:0]
		buffers.vals = buffers.vals[:0]
		buffers.roles = buffers.roles[:0]
		buffers.refs = buffers.refs[:0]
		buffers.types = buffers.types[:0]
		buf2 := buf[:i+int(size)]
		for i < len(buf2) {
			field, wireType, n := readField(buf2[i:])
			i += n
			if n == 0 || field == 0 {
				return fmt.Errorf("invalid Relation")
			} else if field == 1 {
				// id
				if wireType != 0 {
					return fmt.Errorf("invalid id in Relation")
				}
				id, n := readVarint(buf2[i:])
				i += n
				if n == 0 {
					return fmt.Errorf("invalid id in Relation")
				}
				relation.ID = id
			} else if field == 2 {
				// keys
				if wireType != 2 {
					return fmt.Errorf("invalid keys in Relation")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid keys in Relation")
				}
				buf3 := buf2[:i+int(size)]
				buffers.keys = buffers.keys[:0]
				for i < len(buf3) {
					key, n := readVarint(buf2[i:])
					i += n
					if n == 0 || uint64(len(buffers.stringTable)) <= key {
						return fmt.Errorf("invalid key in Relation")
					}
					buffers.keys = append(buffers.keys, uint32(key))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid keys in Relation")
				}
			} else if field == 3 {
				// vals
				if wireType != 2 {
					return fmt.Errorf("invalid vals in Relation")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid vals in Relation")
				}
				buf3 := buf2[:i+int(size)]
				buffers.vals = buffers.vals[:0]
				for i < len(buf3) {
					val, n := readVarint(buf2[i:])
					i += n
					if n == 0 || uint64(len(buffers.stringTable)) <= val {
						return fmt.Errorf("invalid val in Relation")
					}
					buffers.vals = append(buffers.vals, uint32(val))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid vals in Relation")
				}
			} else if field == 8 {
				// roles_sid
				if wireType != 2 {
					return fmt.Errorf("invalid roles_sid in Relation")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid roles_sid in Relation")
				}
				buf3 := buf2[:i+int(size)]
				buffers.roles = buffers.roles[:0]
				for i < len(buf3) {
					role, n := readVarint(buf2[i:])
					i += n
					if n == 0 || role < 0 || uint64(len(buffers.stringTable)) <= role {
						return fmt.Errorf("invalid roles_sid in Relation")
					}
					buffers.roles = append(buffers.roles, int32(role))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid roles_sid in Relation")
				}
			} else if field == 9 {
				// memids
				if wireType != 2 {
					return fmt.Errorf("invalid memids in Relation")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid memids in Relation")
				}
				var ref uint64
				buf3 := buf2[:i+int(size)]
				buffers.refs = buffers.refs[:0]
				for i < len(buf3) {
					delta, n := readSint(buf2[i:])
					i += n
					if n == 0 {
						return fmt.Errorf("invalid memid in Relation")
					}
					if 0 <= delta {
						if math.MaxUint64-ref < uint64(delta) {
							return fmt.Errorf("invalid memid in Relation")
						}
						ref += uint64(delta)
					} else {
						if ref <= uint64(-delta) {
							return fmt.Errorf("invalid memid in Relation")
						}
						ref -= uint64(-delta)
					}
					buffers.refs = append(buffers.refs, ref)
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid memids in Relation")
				}
			} else if field == 10 {
				// types
				if wireType != 2 {
					return fmt.Errorf("invalid types in Relation")
				}
				size, n := readVarint(buf2[i:])
				i += n
				if n == 0 || math.MaxInt < size || len(buf2) < i+int(size) {
					return fmt.Errorf("invalid types in Relation")
				}
				buf3 := buf2[:i+int(size)]
				buffers.types = buffers.types[:0]
				for i < len(buf3) {
					typ, n := readVarint(buf2[i:])
					i += n
					if n == 0 || typ < 0 || 2 < typ {
						return fmt.Errorf("invalid type in Relation")
					}
					buffers.types = append(buffers.types, int8(typ))
				}
				if i != len(buf3) {
					return fmt.Errorf("invalid types in Relation")
				}
			} else {
				n := skipField(buf2[i:], wireType)
				i += n
				if n == 0 {
					return fmt.Errorf("invalid field %v in Relation", field)
				}
			}
		}
		if i != len(buf2) || relation.ID == 0 || len(buffers.keys) != len(buffers.vals) || len(buffers.roles) != len(buffers.refs) || len(buffers.roles) != len(buffers.types) {
			return fmt.Errorf("invalid Relation")
		}

		buffers.members = buffers.members[:0]
		for k := 0; k < len(buffers.roles); k++ {
			buffers.members = append(buffers.members, Member{
				ID:   buffers.refs[k],
				Type: Type(buffers.types[k]),
				Role: buffers.stringTable[buffers.roles[k]],
			})
		}
		buffers.tags = buffers.tags[:0]
		for k := 0; k < len(buffers.keys); k++ {
			buffers.tags = append(buffers.tags, Tag{
				Key: buffers.stringTable[buffers.keys[k]],
				Val: buffers.stringTable[buffers.vals[k]],
			})
		}
		relation.Members = buffers.members
		relation.Tags = buffers.tags
		fn(relation)
	}
	if i != len(buf) {
		return fmt.Errorf("invalid Relations")
	}
	return nil
}

// Parse parses the data and calls the object callback functions for each object. If callback functions are nil it will skip that object type, which is more efficient. Be aware that you need to call `Own` on an object if you which to retain their data after the function call; by default the memory is reused. Note that it will automatically seek to the start of the reader.
func (z *Parser) Parse(ctx context.Context, nodeFunc NodeFunc, wayFunc WayFunc, relationFunc RelationFunc) error {
	workers := z.Workers
	if workers < 1 {
		workers = runtime.GOMAXPROCS(0)
	} else if _, err := z.r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	z.pos = 0

	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(workers)

	// decompress and parse Blobs
	errs := []error{}
	muErr := sync.Mutex{}
	blobs := make(chan Blob, z.Workers*2)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()

			var buffers buffers // reuse buffers
			for blob := range blobs {
				if ctx2.Err() != nil {
					if ctx.Err() != nil {
						muErr.Lock()
						errs = append(errs, ctx.Err())
						muErr.Unlock()
					}
					return
				} else if err := func() error {
					// skip blob if not relevant
					z.mu.Lock()
					content, hasContent := z.blobContents[blob.index]
					if hasContent && !(nodeFunc != nil && content.nodes || wayFunc != nil && content.ways || relationFunc != nil && content.relations) {
						z.mu.Unlock()
						return nil
					}
					z.mu.Unlock()

					block, buf, err := z.block(blob)
					if err != nil {
						return err
					}
					buffers.stringTable = buffers.stringTable[:0]
					for _, buf := range block.PrimitiveGroups {
						field, _, n := readField(buf)
						if n == 0 || field == 0 {
							return fmt.Errorf("invalid PrimitiveGroup")
						} else if field == 1 {
							// Node: noop
						} else if field == 2 {
							// DenseNodes
							if nodeFunc != nil {
								if err := z.nodes(block, &buffers, buf, nodeFunc); err != nil {
									return err
								}
							}
							content.nodes = true
						} else if field == 3 {
							// Way
							if wayFunc != nil {
								if err := z.ways(block, &buffers, buf, wayFunc); err != nil {
									return err
								}
							}
							content.ways = true
						} else if field == 4 {
							// Relation
							if relationFunc != nil {
								if err := z.relations(block, &buffers, buf, relationFunc); err != nil {
									return err
								}
							}
							content.relations = true
						} else if field == 5 {
							// ChangeSet: noop
						}
					}
					z.blobPool.Put(buf)

					if !hasContent {
						z.mu.Lock()
						z.blobContents[blob.index] = content
						z.mu.Unlock()
					}
					return nil
				}(); err != nil {
					muErr.Lock()
					errs = append(errs, err)
					muErr.Unlock()
					cancel()
					return
				}
				atomic.AddInt64(&z.pos, blob.datasize)
			}
		}()
	}

	// find Blobs
	index := 0
	bufHeader := make([]byte, maxBlobHeaderSize)
BlobLoop:
	for {
		if ctx2.Err() != nil {
			if ctx.Err() != nil {
				muErr.Lock()
				errs = append(errs, ctx.Err())
				muErr.Unlock()
			}
			break
		} else if blob, err := z.blob(bufHeader); err != nil {
			if err == io.EOF {
				break
			}
			muErr.Lock()
			errs = append(errs, err)
			muErr.Unlock()
			cancel()
			break
		} else if blob.Data != nil {
			blob.index = index
			select {
			case <-ctx2.Done():
				if ctx.Err() != nil {
					muErr.Lock()
					errs = append(errs, ctx.Err())
					muErr.Unlock()
				}
				break BlobLoop
			case blobs <- blob:
				// noop
			}
		}
		index++
	}
	close(blobs)

	wg.Wait()
	if 0 < len(errs) {
		return errors.Join(errs...)
	}
	return nil
}
