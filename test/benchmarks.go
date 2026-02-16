package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/paulmach/osm/osmpbf"
	"github.com/thomersch/gosmparse"

	"github.com/tdewolff/geo/osm"
)

const (
	TypeNone osm.Type = iota
	TypeBoundary
	TypeCoastline
)

type dataHandler struct{}

func (d *dataHandler) ReadNode(n gosmparse.Node)         {}
func (d *dataHandler) ReadWay(w gosmparse.Way)           {}
func (d *dataHandler) ReadRelation(r gosmparse.Relation) {}

func printStats(name string, ts []time.Duration, ms []uint64) {
	var tMean, tStddev float64
	for _, t := range ts {
		tMean += t.Seconds()
	}
	tMean /= float64(len(ts))
	for _, t := range ts {
		tStddev += (t.Seconds() - tMean) * (t.Seconds() - tMean)
	}
	tStddev = math.Sqrt(tStddev / float64(len(ts)-1))

	var mMean, mStddev float64
	for _, m := range ms {
		mMean += float64(m) / 1024 / 1024
	}
	mMean /= float64(len(ms))
	for _, m := range ms {
		mStddev += (float64(m)/1024/1024 - tMean) * (float64(m)/1024/1024 - mMean)
	}
	mStddev = math.Sqrt(mStddev / float64(len(ms)-1))

	fmt.Printf("%v:\t t=%.2f±%.2f  m=%.2f±%.2f\n", name, tMean, tStddev, mMean, mStddev)
}

func main() {
	prof, err := os.Create("cpu")
	if err != nil {
		panic(err)
	}
	defer prof.Close()
	if err := pprof.StartCPUProfile(prof); err != nil {
		panic(err)
	}
	defer pprof.StopCPUProfile()

	defer func() {
		f, err := os.Create("mem")
		if err != nil {
			panic(err)
		}
		defer f.Close() // error handling omitted for example
		runtime.GC()    // get up-to-date statistics
		pprof.WriteHeapProfile(f)
	}()

	f, err := os.Open("groningen/groningen.osm.pbf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	N := 30
	Workers := 4
	ts := make([]time.Duration, N)
	ms := make([]uint64, N)
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	for n := 0; n < N; n++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			panic(err)
		}
		runtime.ReadMemStats(&memStats)
		t := time.Now()
		m := memStats.TotalAlloc
		scanner := osmpbf.New(context.Background(), f, Workers)
		for scanner.Scan() {
			_ = scanner.Object()
		}
		scanner.Close()
		ts[n] = time.Since(t)
		runtime.ReadMemStats(&memStats)
		ms[n] = memStats.TotalAlloc - m
	}
	printStats("paulmach", ts, ms)

	for n := 0; n < N; n++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			panic(err)
		}
		runtime.ReadMemStats(&memStats)
		t := time.Now()
		m := memStats.TotalAlloc
		scanner := osmpbf.New(context.Background(), f, Workers)
		scanner.SkipNodes = true
		scanner.SkipWays = true
		scanner.SkipRelations = true
		for scanner.Scan() {
			_ = scanner.Object()
		}
		scanner.Close()
		ts[n] = time.Since(t)
		runtime.ReadMemStats(&memStats)
		ms[n] = memStats.TotalAlloc - m
	}
	printStats("paulmach (skipping)", ts, ms)

	for n := 0; n < N; n++ {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			panic(err)
		}
		runtime.ReadMemStats(&memStats)
		t := time.Now()
		m := memStats.TotalAlloc
		dec := gosmparse.NewDecoder(f)
		dec.Workers = Workers
		if err := dec.Parse(&dataHandler{}); err != nil {
			panic(err)
		}
		ts[n] = time.Since(t)
		runtime.ReadMemStats(&memStats)
		ms[n] = memStats.TotalAlloc - m
	}
	printStats("thomersch", ts, ms)

	ctx := context.Background()
	nodeFunc := func(node osm.Node) {}
	wayFunc := func(way osm.Way) {}
	relationFunc := func(relation osm.Relation) {}

	for n := 0; n < N; n++ {
		runtime.ReadMemStats(&memStats)
		t := time.Now()
		m := memStats.TotalAlloc
		z := osm.NewParser(f)
		if err := z.Parse(ctx, nodeFunc, wayFunc, relationFunc); err != nil {
			panic(err)
		}
		ts[n] = time.Since(t)
		runtime.ReadMemStats(&memStats)
		ms[n] = memStats.TotalAlloc - m
	}
	printStats("tdewolff", ts, ms)

	for n := 0; n < N; n++ {
		runtime.ReadMemStats(&memStats)
		t := time.Now()
		m := memStats.TotalAlloc
		z := osm.NewParser(f)
		if err := z.Parse(ctx, nil, nil, nil); err != nil {
			panic(err)
		}
		ts[n] = time.Since(t)
		runtime.ReadMemStats(&memStats)
		ms[n] = memStats.TotalAlloc - m
	}
	printStats("tdewolff (skipping)", ts, ms)
}
