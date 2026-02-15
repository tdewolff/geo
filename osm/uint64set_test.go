package osm

import (
	"testing"
)

func TestSetSimple(t *testing.T) {
	m := NewUint64Set(10, 0.99)
	var i uint64

	// --------------------------------------------------------------------
	// Add() and Has()

	for i = 0; i < 20000; i += 2 {
		m.Add(i)
	}
	for i = 0; i < 20000; i += 2 {
		if !m.Has(i) {
			t.Errorf("%d should exist", i)
		}
		if m.Has(i + 1) {
			t.Errorf("%d should not exist", i)
		}
	}

	if m.Size() != int(20000/2) {
		t.Errorf("size (%d) is not right, should be %d", m.Size(), int(20000/2))
	}

	// --------------------------------------------------------------------
	// Del()

	for i = 0; i < 20000; i += 2 {
		m.Del(i)
	}
	for i = 0; i < 20000; i += 2 {
		if m.Has(i) {
			t.Errorf("%d should not exist", i)
		}
		if m.Has(i + 1) {
			t.Errorf("%d should not exist", i)
		}
	}

}

func fillUint64Set(m *Set) {
	var j, k uint64
	for j = 0; j < MAX; j += STEP {
		m.Add(j)
		for k = j; k < j+16; k++ {
			m.Add(k)
		}
	}
}

func fillStdSet(m map[uint64]struct{}) {
	var j, k uint64
	for j = 0; j < MAX; j += STEP {
		m[j] = struct{}{}
		for k = j; k < j+16; k++ {
			m[k] = struct{}{}
		}
	}
}

func BenchmarkUint64SetFill(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := NewUint64Set(2048, 0.80)
		fillUint64Set(m)
	}
}

func BenchmarkStdSetFill(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := make(map[uint64]struct{}, 2048)
		fillStdSet(m)
	}
}

func BenchmarkUint64SetTest10PercentHitRate(b *testing.B) {
	var j, k, sum uint64
	m := NewUint64Set(2048, 0.80)
	fillUint64Set(m)
	for i := 0; i < b.N; i++ {
		sum = 0
		for j = 0; j < MAX; j += STEP {
			for k = j; k < 10; k++ {
				if m.Has(k) {
					sum += k
				}
			}
		}
		//log.Println("int int sum:", sum)
	}
}

func BenchmarkStdSetTest10PercentHitRate(b *testing.B) {
	var j, k, sum uint64
	var ok bool
	m := make(map[uint64]struct{}, 2048)
	fillStdSet(m)
	for i := 0; i < b.N; i++ {
		sum = 0
		for j = 0; j < MAX; j += STEP {
			for k = j; k < 10; k++ {
				if _, ok = m[k]; ok {
					sum += k
				}
			}
		}
		//log.Println("map sum:", sum)
	}
}

func BenchmarkUint64SetTest100PercentHitRate(b *testing.B) {
	var j, sum uint64
	m := NewUint64Set(2048, 0.80)
	fillUint64Set(m)
	for i := 0; i < b.N; i++ {
		sum = 0
		for j = 0; j < MAX; j += STEP {
			if m.Has(j) {
				sum += j
			}
		}
		//log.Println("int int sum:", sum)
	}
}

func BenchmarkStdSetTest100PercentHitRate(b *testing.B) {
	var j, sum uint64
	var ok bool
	m := make(map[uint64]struct{}, 2048)
	fillStdSet(m)
	for i := 0; i < b.N; i++ {
		sum = 0
		for j = 0; j < MAX; j += STEP {
			if _, ok = m[j]; ok {
				sum += j
			}
		}
		//log.Println("map sum:", sum)
	}
}
