package osm

import (
	"testing"
)

func TestMapSimple(t *testing.T) {
	m := NewUint64Map(10, 0.99)
	var i uint64
	var v uint64
	var ok bool

	// --------------------------------------------------------------------
	// Put() and Get()

	for i = 0; i < 20000; i += 2 {
		m.Put(i, i)
	}
	for i = 0; i < 20000; i += 2 {
		if v, ok = m.Get(i); !ok || v != i {
			t.Errorf("didn't get expected value")
		}
		if _, ok = m.Get(i + 1); ok {
			t.Errorf("didn't get expected 'not found' flag")
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
		if _, ok = m.Get(i); ok {
			t.Errorf("didn't get expected 'not found' flag")
		}
		if _, ok = m.Get(i + 1); ok {
			t.Errorf("didn't get expected 'not found' flag")
		}
	}

	// --------------------------------------------------------------------
	// Put() and Get()

	for i = 0; i < 20000; i += 2 {
		m.Put(i, i*2)
	}
	for i = 0; i < 20000; i += 2 {
		if v, ok = m.Get(i); !ok || v != i*2 {
			t.Errorf("didn't get expected value")
		}
		if _, ok = m.Get(i + 1); ok {
			t.Errorf("didn't get expected 'not found' flag")
		}
	}

}

func TestMap(t *testing.T) {
	m := NewUint64Map(10, 0.6)
	var ok bool
	var v uint64

	step := uint64(61)

	var i uint64
	m.Put(0, 12345)
	for i = 1; i < 1000000; i += step {
		m.Put(i, i+7)
		m.Put(-i, i-7)

		if v, ok = m.Get(i); !ok || v != i+7 {
			t.Errorf("expected %d as value for key %d, got %d", i+7, i, v)
		}
		if v, ok = m.Get(-i); !ok || v != i-7 {
			t.Errorf("expected %d as value for key %d, got %d", i-7, -i, v)
		}
	}
	for i = 1; i < 1000000; i += step {
		if v, ok = m.Get(i); !ok || v != i+7 {
			t.Errorf("expected %d as value for key %d, got %d", i+7, i, v)
		}
		if v, ok = m.Get(-i); !ok || v != i-7 {
			t.Errorf("expected %d as value for key %d, got %d", i-7, -i, v)
		}

		for j := i + 1; j < i+step; j++ {
			if v, ok = m.Get(j); ok {
				t.Errorf("expected 'not found' flag for %d, found %d", j, v)
			}
		}
	}

	if v, ok = m.Get(0); !ok || v != 12345 {
		t.Errorf("expected 12345 for key 0")
	}
}

const MAX = 999999999
const STEP = 9534

func fillUint64Map(m *Map) {
	var j uint64
	for j = 0; j < MAX; j += STEP {
		m.Put(j, -j)
		for k := j; k < j+16; k++ {
			m.Put(k, -k)
		}

	}
}

func fillStdMap(m map[uint64]uint64) {
	var j uint64
	for j = 0; j < MAX; j += STEP {
		m[j] = -j
		for k := j; k < j+16; k++ {
			m[k] = -k
		}
	}
}

func BenchmarkUint64MapFill(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := NewUint64Map(2048, 0.60)
		fillUint64Map(m)
	}
}

func BenchmarkStdMapFill(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := make(map[uint64]uint64, 2048)
		fillStdMap(m)
	}
}

func BenchmarkUint64MapGet10PercentHitRate(b *testing.B) {
	var j, k, v, sum uint64
	var ok bool
	m := NewUint64Map(2048, 0.60)
	fillUint64Map(m)
	for i := 0; i < b.N; i++ {
		sum = uint64(0)
		for j = 0; j < MAX; j += STEP {
			for k = j; k < 10; k++ {
				if v, ok = m.Get(k); ok {
					sum += v
				}
			}
		}
		//log.Println("int int sum:", sum)
	}
}

func BenchmarkStdMapGet10PercentHitRate(b *testing.B) {
	var j, k, v, sum uint64
	var ok bool
	m := make(map[uint64]uint64, 2048)
	fillStdMap(m)
	for i := 0; i < b.N; i++ {
		sum = uint64(0)
		for j = 0; j < MAX; j += STEP {
			for k = j; k < 10; k++ {
				if v, ok = m[k]; ok {
					sum += v
				}
			}
		}
		//log.Println("map sum:", sum)
	}
}

func BenchmarkUint64MapGet100PercentHitRate(b *testing.B) {
	var j, v, sum uint64
	var ok bool
	m := NewUint64Map(2048, 0.60)
	fillUint64Map(m)
	for i := 0; i < b.N; i++ {
		sum = uint64(0)
		for j = 0; j < MAX; j += STEP {
			if v, ok = m.Get(j); ok {
				sum += v
			}
		}
		//log.Println("int int sum:", sum)
	}
}

func BenchmarkStdMapGet100PercentHitRate(b *testing.B) {
	var j, v, sum uint64
	var ok bool
	m := make(map[uint64]uint64, 2048)
	fillStdMap(m)
	for i := 0; i < b.N; i++ {
		sum = uint64(0)
		for j = 0; j < MAX; j += STEP {
			if v, ok = m[j]; ok {
				sum += v
			}
		}
		//log.Println("map sum:", sum)
	}
}

func BenchmarkStdMapRange(b *testing.B) {
	var j, v, sum uint64
	m := make(map[uint64]uint64, 2048)
	fillStdMap(m)
	for i := 0; i < b.N; i++ {
		sum = uint64(0)
		for j, v = range m {
			sum += j
			sum += v
		}
		//log.Println("map sum:", sum)
	}
}

func BenchmarkUint64MapEach(b *testing.B) {
	var sum uint64
	m := NewUint64Map(2048, 0.60)
	fillUint64Map(m)
	for i := 0; i < b.N; i++ {
		//sum = int64(0)
		m.Iterate(func(k, v uint64) {
			sum += k
			sum += v
		})

	}
	//log.Println("int int sum:", sum)
}
