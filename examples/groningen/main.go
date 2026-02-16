package main

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/tdewolff/canvas"
	"github.com/tdewolff/canvas/renderers"
	"github.com/tdewolff/geo"
	"github.com/tdewolff/geo/osm"
)

const (
	Unknown osm.Class = iota + 1
	Land
	Water
	Wetland
	Grass
	Forest
	Residential
	Beach
)

var Bounds = osm.Bounds{
	{6.5651050153515484, 53.16260493850089},
	{6.574056630521028, 53.1677857404529},
}

func progress(ctx context.Context, z *osm.Parser, total int64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pos := z.Pos()
			fmt.Printf("%d/%d  %.1f%%\n", pos, total, float64(pos)/float64(total))
		}
	}
}

func boundsPath(bounds osm.Bounds, projector geo.Projector) *canvas.Path {
	p := &canvas.Path{}
	x, y := projector(bounds[0].X, bounds[0].Y)
	p.MoveTo(x, y)
	x, y = projector(bounds[1].X, bounds[0].Y)
	p.LineTo(x, y)
	x, y = projector(bounds[1].X, bounds[1].Y)
	p.LineTo(x, y)
	x, y = projector(bounds[0].X, bounds[1].Y)
	p.LineTo(x, y)
	p.Close()
	return p
}

func polygonPath(polygons []osm.Polygon, projector geo.Projector) *canvas.Path {
	p := &canvas.Path{}
	for _, polygon := range polygons {
		x, y := projector(polygon.Coords[0].X, polygon.Coords[0].Y)
		p.MoveTo(x, y)
		for _, coord := range polygon.Coords[1:] {
			x, y := projector(coord.X, coord.Y)
			p.LineTo(x, y)
		}
		p.Close()
	}
	return p
}

func colorOpacity(col color.RGBA, a float64) color.RGBA {
	R, G, B, A := col.RGBA()
	newA := uint32(a * 0xffff)
	return color.RGBA{uint8(newA * R / A), uint8(newA * G / A), uint8(newA * B / A), uint8(newA)}
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

	r, err := os.Open("paterswoldsemeer.osm.pbf")
	if err != nil {
		panic(err)
	}

	ctx0, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var t time.Time
	z := osm.NewParser(r)

	//t = time.Now()
	//stats, err := z.Stats(ctx0)
	//if err != nil {
	//	panic(err)
	//}
	//fmt.Println(stats)
	//fmt.Println("Time:", time.Since(t))

	t = time.Now()
	Bounds = Bounds.ExpandByFactor(8.0)
	margin := 0.01 // relative to width or height
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
	geometries, err := z.Extract(ctx0, Bounds.ExpandByFactor(margin), filter)
	if err != nil {
		panic(err)
	}
	fmt.Println("Time:", time.Since(t))

	proj := geo.TransverseMercatorLambert(Bounds.Centre().X, 0.9996)
	projBounds := Bounds.Project(proj.Forward)

	width := 900.0
	f := width / projBounds.W()
	height := f * projBounds.H()

	projector := func(lon float64, lat float64) (float64, float64) {
		x, y := proj.Forward(lon, lat)
		return f * (x - projBounds[0].X), f * (y - projBounds[0].Y)
	}

	c := canvas.New(width, height)
	ctx := canvas.NewContext(c)
	ctx.SetStrokeWidth(1.0) // avoid black borders

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

	ctx.SetFillColor(canvas.Transparent)
	ctx.SetStrokeColor(canvas.Red)
	ctx.SetStrokeWidth(1.5)
	ctx.SetDashes(0.0, canvas.Dashed...)
	ctx.DrawPath(0.0, 0.0, boundsPath(Bounds, projector))

	c.Fit(1.0)

	if err := renderers.Write("out.png", c, canvas.Resolution(1.0)); err != nil {
		panic(err)
	}
}
