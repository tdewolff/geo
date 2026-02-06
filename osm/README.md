# OSM PBF parser

High-performance parser for the OSM PBF file format (no CGO). This parser uses unrolled versions of `readVarint` and `readSint` and handwritten parsing of the protobuf format, uses github.com/klauspost/compress for faster decompression, reuses memory buffers to reduce GC pressure, and allows for skipping an object type (node, way, or relation) to speed up parsing.

## Example

```go
import "github.com/tdewolff/geo/osm"

func main() {
	f, err := os.Open("planet.osm.pbf")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	nodeFunc := func(node osm.Node) {
        // process node:
        // node.ID   uint64
        // node.Lon  float64
        // node.Lat  float64
        // node.Tags osm.Tags
	}
	wayFunc := func(way osm.Way) {
        // process way:
        // way.ID   uint64
        // way.Refs []uint64
        // way.Tags osm.Tags
	}
	relationFunc := func(relation osm.Relation) {
        // process relation:
	    // relation.ID      uint64
	    // relation.Members []struct{
        //     ID   uint64
        //     Type osm.ObjectType // osm.NodeType, osm.WayType, or osm.RelationType
        //     Role string
        // }
	    // relation.Tags    osm.Tags
	}

    z := osm.NewParser(f)
    // NOTE: pass nil for a function to skip object type
    if err := z.Parse(ctx, nodeFunc, wayFunc, relationFunc); err != nil {
        panic(err)
    }
}
```

Note that all slices and `Tags` for each object are reused, so you need to call e.g. `relation.Own()` to copy that memory to be able to keep using it after the function call. This is only required if you use node.Tags, way.Refs, way.Tags, relation.Members, or relation.Tags outside and after the object function call.

## Performance
Performance measurements on my ThinkPad T460 (Intel Core i5-6300U, dual-core, four-threads) using 4 parallel workers using the BBBike's extract for province of [Groningen, The Netherlands](https://download3.bbbike.org/osm/region/europe/netherlands/groningen/).

| Library | Time (s) | Allocations (MB) |
| ------- | -------- | ---------------- |
| **tdewolff/geo/osm** | 0.56±0.01 | 48.95±3.00 |
| **tdewolff/geo/osm** (skip objects) | 0.37±0.01 | 32.09±1.31 |
| [paulmach/osm](https://github.com/paulmach/osm) | 1.22±0.04 | 1754.71±0.02 |
| [paulmach/osm](https://github.com/paulmach/osm) (skip objects) | 0.34±0.01 | 280.45±0.00 |
| [thomersch/gosmparse](https://github.com/thomersch/gosmparse) | 1.46±0.04 | 1706.43±0.00 | 

Skip objects refers to skipping all nodes, ways, and relations which is indicative for the performance gain of parsing specific object types while ignoring others. The thomersch/gosmparse library does not have this feature. Note that paulmach/osm uses a slighly faster zlib decompressor that requires CGO, resulting in a faster parsing when skipping all objects.
