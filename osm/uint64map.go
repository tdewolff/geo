// This package is copied nearly verbatim from http://java-performance.info/implementing-world-fastest-java-int-to-int-hash-map/
package osm

import (
	"math"
)

// INT_PHI is for scrambling the keys
const INT_PHI = 0x9E3779B9

// FREE_KEY is the 'free' key
const FREE_KEY = 0

func phiMix(x uint64) uint64 {
	h := x * INT_PHI
	return h ^ (h >> 16)
}

// Map is a map-like data-structure for int64s
type Map struct {
	data       []uint64 // interleaved keys and values
	fillFactor float64
	threshold  int // we will resize a map once it reaches this size
	size       int

	mask  uint64 // mask to calculate the original position
	mask2 uint64

	hasFreeKey bool   // do we have 'free' key in the map?
	freeVal    uint64 // value of 'free' key
}

func nextPowerOf2(x uint32) uint32 {
	if x == math.MaxUint32 {
		return x
	}

	if x == 0 {
		return 1
	}

	x--
	x |= x >> 1
	x |= x >> 2
	x |= x >> 4
	x |= x >> 8
	x |= x >> 16

	return x + 1
}

func arraySize(exp int, fill float64) int {
	s := nextPowerOf2(uint32(math.Ceil(float64(exp) / fill)))
	if s < 2 {
		s = 2
	}
	return int(s)
}

// NewUint64Map returns a map initialized with n spaces and uses the stated fillFactor.
// The map will grow as needed.
func NewUint64Map(size int, fillFactor float64) *Map {
	if fillFactor <= 0.0 || 1.0 <= fillFactor {
		panic("fillFactor must be in [0,1]")
	} else if size <= 0 {
		panic("size must be positive")
	}

	capacity := arraySize(size, fillFactor)
	return &Map{
		data:       make([]uint64, 2*capacity),
		fillFactor: fillFactor,
		threshold:  int(math.Floor(float64(capacity) * fillFactor)),
		mask:       uint64(capacity - 1),
		mask2:      uint64(2*capacity - 1),
	}
}

// Has returns id the key is found.
func (m *Map) Has(key uint64) bool {
	if key == FREE_KEY {
		return m.hasFreeKey
	}

	ptr := (phiMix(key) & m.mask) << 1
	if ptr < 0 || ptr >= uint64(len(m.data)) { // Check to help to compiler to eliminate a bounds check below.
		return false
	}
	k := m.data[ptr]
	for {
		if k == FREE_KEY {
			return false
		} else if k == key {
			return true
		}
		ptr = (ptr + 2) & m.mask2
		k = m.data[ptr]
	}
}

// Get returns the value if the key is found.
func (m *Map) Get(key uint64) (uint64, bool) {
	if key == FREE_KEY {
		if m.hasFreeKey {
			return m.freeVal, true
		}
		return 0, false
	}

	ptr := (phiMix(key) & m.mask) << 1
	if ptr < 0 || ptr >= uint64(len(m.data)) { // Check to help to compiler to eliminate a bounds check below.
		return 0, false
	}
	k := m.data[ptr]
	for {
		if k == FREE_KEY {
			return 0, false
		} else if k == key {
			return m.data[ptr+1], true
		}
		ptr = (ptr + 2) & m.mask2
		k = m.data[ptr]
	}
}

// Put adds or updates key with value val. It returns true if replacing an existing value.
func (m *Map) Put(key uint64, val uint64) {
	if key == FREE_KEY {
		m.freeVal = val
		if !m.hasFreeKey {
			m.size++
			m.hasFreeKey = true
		}
		return
	}

	ptr := (phiMix(key) & m.mask) << 1
	k := m.data[ptr]
	for {
		if k == FREE_KEY {
			m.data[ptr] = key
			m.data[ptr+1] = val
			if m.size >= m.threshold {
				m.rehash()
			} else {
				m.size++
			}
			return
		} else if k == key {
			m.data[ptr+1] = val
			return
		}
		ptr = (ptr + 2) & m.mask2
		k = m.data[ptr]
	}
}

// Del deletes a key and its value.
func (m *Map) Del(key uint64) {
	if key == FREE_KEY {
		if m.hasFreeKey {
			m.hasFreeKey = false
			m.size--
		}
		return
	}

	ptr := (phiMix(key) & m.mask) << 1
	k := m.data[ptr]
	for {
		if k == key {
			m.shiftKeys(ptr)
			m.size--
			return
		} else if k == FREE_KEY {
			return
		}
		ptr = (ptr + 2) & m.mask2
		k = m.data[ptr]
	}
}

func (m *Map) shiftKeys(pos uint64) uint64 {
	// Shift entries with the same hash.
	var k uint64
	for {
		last := pos
		pos = (last + 2) & m.mask2
		for {
			k = m.data[pos]
			if k == FREE_KEY {
				m.data[last] = FREE_KEY
				return last
			}

			slot := (phiMix(k) & m.mask) << 1
			if last <= pos {
				if last >= slot || slot > pos {
					break
				}
			} else {
				if last >= slot && slot > pos {
					break
				}
			}
			pos = (pos + 2) & m.mask2
		}
		m.data[last] = k
		m.data[last+1] = m.data[pos+1]
	}
}

func (m *Map) rehash() {
	newCapacity := len(m.data) * 2
	m.threshold = int(math.Floor(float64(len(m.data)) * m.fillFactor))
	m.mask = uint64(len(m.data) - 1)
	m.mask2 = uint64(newCapacity - 1)

	m.size = 0
	if m.hasFreeKey {
		m.size = 1
	}

	old := m.data
	m.data = make([]uint64, newCapacity)
	for i := 0; i < len(old); i += 2 {
		if k := old[i]; k != FREE_KEY {
			m.Put(k, old[i+1])
		}
	}
}

// Size returns size of the map.
func (m *Map) Size() int {
	return m.size
}

func (m *Map) Iterate(f func(k, v uint64)) {
	if m.hasFreeKey {
		f(FREE_KEY, m.freeVal)
	}
	for i := 0; i < len(m.data); i += 2 {
		if k := m.data[i]; k != FREE_KEY {
			f(k, m.data[i+1])
		}
	}
}
