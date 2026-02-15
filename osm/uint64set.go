// This package is a fork of intintSet, which is copied nearly verbatim from http://java-performance.info/implementing-world-fasHas-java-int-to-int-hash-Set/
package osm

import (
	"math"
)

// Set is a Set-like data-structure for uint64s
type Set struct {
	data       []uint64 // only keys
	fillFactor float64
	threshold  int // we will resize a Set once it reaches this size
	size       int

	mask  uint64 // mask to calculate the original position
	mask2 uint64

	hasFreeKey bool // do we have 'free' key in the Set?
}

// NewUint64Set returns a Set initialized with n spaces and uses the stated fillFactor.
// The Set will grow as needed.
func NewUint64Set(size int, fillFactor float64) *Set {
	if fillFactor <= 0 || fillFactor >= 1 {
		panic("FillFactor must be in (0, 1)")
	}
	if size <= 0 {
		panic("Size must be positive")
	}

	capacity := arraySize(size, fillFactor)
	return &Set{
		data:       make([]uint64, capacity),
		fillFactor: fillFactor,
		threshold:  int(math.Floor(float64(capacity) * fillFactor)),
		mask:       uint64(capacity - 1),
		mask2:      uint64(capacity - 1),
	}
}

// Has checks if an element exists
func (m *Set) Has(key uint64) bool {
	if key == FREE_KEY {
		if m.hasFreeKey {
			return true
		}
		return false
	}

	ptr := phiMix(key) & m.mask
	k := m.data[ptr]

	if key == FREE_KEY { // end of chain already
		return false
	}
	if k == key { // we check FREE prior to this call
		return true
	}

	for {
		ptr = (ptr + 1) & m.mask2
		k = m.data[ptr]
		if k == FREE_KEY {
			return false
		}
		if k == key {
			return true
		}
	}
}

// Add adds an element
func (m *Set) Add(key uint64) {
	if key == FREE_KEY {
		if !m.hasFreeKey {
			m.size++
		}
		m.hasFreeKey = true
		return
	}

	ptr := (phiMix(key) & m.mask)
	k := m.data[ptr]

	if k == FREE_KEY { // end of chain already
		m.data[ptr] = key
		if m.size >= m.threshold {
			m.rehash()
		} else {
			m.size++
		}
		return
	} else if k == key { // existed key
		return
	}

	for {
		ptr = (ptr + 1) & m.mask2
		k = m.data[ptr]

		if k == FREE_KEY {
			m.data[ptr] = key
			if m.size >= m.threshold {
				m.rehash()
			} else {
				m.size++
			}
			return
		} else if k == key {
			return
		}
	}
}

// Del deletes an element.
func (m *Set) Del(key uint64) {
	if key == FREE_KEY {
		m.hasFreeKey = false
		m.size--
		return
	}

	ptr := phiMix(key) & m.mask
	k := m.data[ptr]

	if k == key {
		m.shiftKeys(int64(ptr))
		m.size--
		return
	} else if k == FREE_KEY { // end of chain already
		return
	}

	for {
		ptr = (ptr + 1) & m.mask2
		k = m.data[ptr]

		if k == key {
			m.shiftKeys(int64(ptr))
			m.size--
			return
		} else if k == FREE_KEY {
			return
		}
	}
}

func (m *Set) shiftKeys(pos int64) int64 {
	// Shift entries with the same hash.
	var last, slot int64
	var k uint64
	var data = m.data
	for {
		last = pos
		pos = int64(uint64(last+1) & m.mask2)
		for {
			k = data[pos]
			if k == FREE_KEY {
				data[last] = FREE_KEY
				return last
			}

			slot = int64(phiMix(k) & m.mask)
			if last <= pos {
				if last >= slot || slot > pos {
					break
				}
			} else {
				if last >= slot && slot > pos {
					break
				}
			}
			pos = int64(uint64(pos+1) & m.mask2)
		}
		data[last] = k
	}
}

func (m *Set) rehash() {
	newCapacity := len(m.data) * 2
	m.threshold = int(math.Floor(float64(newCapacity/2) * m.fillFactor))
	m.mask = uint64(newCapacity - 1)
	m.mask2 = uint64(newCapacity - 1)

	data := m.data
	m.data = make([]uint64, newCapacity)
	if m.hasFreeKey { // reset size
		m.size = 1
	} else {
		m.size = 0
	}

	var o uint64
	for i := 0; i < len(data); i++ {
		o = data[i]
		if o != FREE_KEY {
			m.Add(o)
		}
	}
}

// Size returns size of the Set.
func (m *Set) Size() int {
	return m.size
}

func (m *Set) Iterate(fn func(uint64)) {
	if m.hasFreeKey {
		fn(FREE_KEY)
	}
	for i := 0; i < len(m.data); i++ {
		k := m.data[i]
		if k == FREE_KEY {
			continue
		}
		fn(k)
	}
}
