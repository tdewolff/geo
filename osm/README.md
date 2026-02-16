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
        //     Type osm.ObjectType // osm.NodeType, osm.WayType, or osm.RelationType
        //     ID   uint64
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
| **tdewolff/geo/osm** (CGO, skip objects) | 0.27±0.04 | 57.79±1.52 |
| [paulmach/osm](https://github.com/paulmach/osm) (CGO, skip objects) | 0.34±0.01 | 280.45±0.00 |
| **tdewolff/geo/osm** (skip objects) | 0.37±0.01 | 32.09±1.31 |
| **tdewolff/geo/osm** (CGO) | 0.47±0.05 | 74.44±2.33 |
| **tdewolff/geo/osm** | 0.56±0.01 | 48.95±3.00 |
| [paulmach/osm](https://github.com/paulmach/osm) (CGO) | 1.22±0.04 | 1754.71±0.02 |
| [thomersch/gosmparse](https://github.com/thomersch/gosmparse) | 1.46±0.04 | 1706.43±0.00 | 

Skip objects refers to skipping all nodes, ways, and relations which is indicative for the performance gain of parsing specific object types while ignoring others. The thomersch/gosmparse library does not have this feature.

## Statistics
Gather various OSM statistics.
```go
r, err := os.Open("groningen.osm.pbf")
if err != nil {
    panic(err)
}

ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

stats, err := osm.NewParser(r).Stats(ctx)
if err != nil {
    panic(err)
}
fmt.Println(stats)
```

Output:
```
Nodes:        num=5588975  id=[150862,13525140901]
  parents:    relation=2629 (0.0%)  way=5206109 (93.1%)  both=2933 (0.1%)  none=377304 (6.8%)

Ways:         num=709253  id=[4424482,1474261027]
  parents:    relation=43683 (6.2%)  none=665570 (93.8%)
  nodes:      num=5209042  mean=10±14  q(.5,.75,.9,.99)=[6 11 19 65]  missing=0

Relations:    num=9073  id=[1883,20124374]
  parents:    relation=3799 (41.9%)  none=5274 (58.1%)
  depths:     0=5274  1=3407  2=134  3=258

  nodes:      num=14330  mean=1±21  q(.5,.75,.9,.99)=[0 0 1 44]  missing=8768
  ways:       num=80187  mean=18±106  q(.5,.75,.9,.99)=[3 6 17 249]  missing=36504
  relations:  num=16981  mean=0±32  q(.5,.75,.9,.99)=[0 0 0 3]  missing=13182
Bounds:       lon=[0.06715,8.573573300000001]  lat=[51.2541943,58.2701214]
```

## Extract and render
Extract water, grass and forest features and render.
```go
r, err := os.Open("groningen.osm.pbf")
if err != nil {
    panic(err)
}

ctx0, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

margin := 0.01 // relative to width or height
bounds := osm.Bounds{
	{6.5651050153515484, 53.16260493850089},
	{6.574056630521028, 53.1677857404529},
}

filter := func(typ osm.Type, id uint64, tags osm.Tags) osm.Class {
    if tags.Find("natural") == "water" {
        return Water
    } else if tags.Find("landuse") == "grass" {
        return Grass
    } else if tags.Find("landuse") == "forest" {
        return Forest
    } else if tags.Find("landuse") == "residential" {
        return Residential
    } else if tags.Find("natural") == "wetland" {
        return Wetland
    } else if tags.Find("natural") == "beach" {
        return Beach
    } else if tags.Find("type") == "coastline" {
        return Land
    }
    return 0
}

geometries, err := z.Extract(ctx0, bounds.ExpandByFactor(margin), filter)
if err != nil {
    panic(err)
}


// use transverse mercator projection
proj := geo.TransverseMercatorLambert(bounds.Centre().X, 0.9996)
projBounds := bounds.Project(proj.Forward)

// find image width and height
width := 900.0
f := width / projBounds.W()
height := f * projBounds.H()

// projector for coordinates
projector := func(lon float64, lat float64) (float64, float64) {
    x, y := proj.Forward(lon, lat)
    return f * (x - projBounds[0].X), f * (y - projBounds[0].Y)
}

c := canvas.New(width, height)
ctx := canvas.NewContext(c)
ctx.SetStrokeWidth(1.0) // avoid black borders

// range over classes one by one, add stroke to avoid black borders
colors := map[osm.Class]color.RGBA{
    Land:        canvas.Hex("fbeedb"),
    Water:       canvas.Hex("30aee1"),
    Wetland:     canvas.Hex("7fbd9b"),
    Grass:       canvas.Hex("4d8a44"),
    Forest:      canvas.Hex("256316"),
    Beach:       canvas.Hex("e1ea8d"),
    Residential: canvas.Hex("d0d0d0"),
}

ctx.SetFillColor(colors[Land])
ctx.SetStrokeColor(canvas.Transparent)
ctx.DrawPath(0.0, 0.0, boundsPath(Bounds.ExpandByFactor(margin), projector))

classes := []osm.Class{Water, Residential, Wetland, Forest, Grass, Beach}
for _, class := range classes {
    color := colors[class]
    ctx.SetFillColor(color)
    ctx.SetStrokeColor(color)
    if geoms := geometries[class]; 0 < len(geoms) {
        for _, geom := range geoms {
            ctx.DrawPath(0.0, 0.0, polygonPath(geom.Polygons, projector))
        }
    }
}

// render image
if err := renderers.Write("groningen.png", c, canvas.Resolution(1.0)); err != nil {
    panic(err)
}
```

![Paterswoldsemeer, Groningen](https://github.com/tdewolff/geo/blob/master/examples/groningen/out.png)
