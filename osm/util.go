package osm

import (
	"math"
)

var WorldBounds = Bounds{{math.Inf(-1), math.Inf(-1)}, {math.Inf(1), math.Inf(1)}}

type Coord struct {
	X, Y float64
}

func (p Coord) Sub(q Coord) Coord {
	return Coord{p.X - q.X, p.Y - q.Y}
}

func (p Coord) AngleBetween(q Coord) float64 {
	dot := p.X*q.X + p.Y*q.Y
	perpdot := p.X*q.Y - p.Y*q.X
	return math.Atan2(perpdot, dot)
}

// Bounds is the [min,max] coordinate of a bounding box.
type Bounds [2]Coord

func (b Bounds) W() float64 {
	return b[1].X - b[0].X
}

func (b Bounds) H() float64 {
	return b[1].Y - b[0].Y
}

func (b Bounds) Centre() Coord {
	return Coord{(b[0].X + b[1].X) / 2.0, (b[0].Y + b[1].Y) / 2.0}
}

func (b Bounds) Contains(c Coord) bool {
	return b[0].X <= c.X && c.X <= b[1].X && b[0].Y <= c.Y && c.Y <= b[1].Y
}

func (b Bounds) Expand(d float64) Bounds {
	return Bounds{
		{b[0].X - d, b[0].Y - d},
		{b[1].X + d, b[1].Y + d},
	}
}

func (b Bounds) Project(proj func(float64, float64) (float64, float64)) Bounds {
	ax, ay := proj(b[0].X, b[0].Y)
	bx, by := proj(b[1].X, b[0].Y)
	cx, cy := proj(b[1].X, b[1].Y)
	dx, dy := proj(b[0].X, b[1].Y)
	return Bounds{
		{math.Min(math.Min(ax, bx), math.Min(cx, dx)), math.Min(math.Min(ay, by), math.Min(cy, dy))},
		{math.Max(math.Max(ax, bx), math.Max(cx, dx)), math.Max(math.Max(ay, by), math.Max(cy, dy))},
	}
}

// IsArea returns true if the way or relation is considered an enclosed area (in contrast to a open path).
func (tags Tags) IsArea() bool {
	area := false
	for _, tag := range tags {
		if tag.Key == "area" && tag.Val == "yes" {
			return true
		} else if tag.Key == "area" && tag.Val == "no" {
			return false
		} else if tag.Key == "building" || tag.Key == "landuse" || tag.Key == "amenity" || tag.Key == "shop" || tag.Key == "building:part" || tag.Key == "boundary" || tag.Key == "historic" || tag.Key == "place" || tag.Key == "area:highway" || tag.Key == "waterway" && tag.Val == "riverbank" || tag.Key == "highway" && (tag.Val == "rest_area" || tag.Val == "services" || tag.Val == "platform") || tag.Key == "railway" && tag.Val == "platform" || tag.Key == "natural" && (tag.Val == "water" || tag.Val == "wood" || tag.Val == "scrub" || tag.Val == "wetland" || tag.Val == "grassland" || tag.Val == "heath" || tag.Val == "rock" || tag.Val == "bare_rock" || tag.Val == "sand" || tag.Val == "beach" || tag.Val == "scree" || tag.Val == "bay" || tag.Val == "glacier" || tag.Val == "shingle" || tag.Val == "fell" || tag.Val == "reef" || tag.Val == "stone" || tag.Val == "mud" || tag.Val == "landslide" || tag.Val == "sinkhole" || tag.Val == "crevasse" || tag.Val == "desert") || tag.Key == "leisure" && (tag.Val != "picnic_table" && tag.Val != "slipway" && tag.Val != "firepit") || tag.Key == "aeroway" && tag.Val == "aerodrome" {
			area = true
		} else if tag.Key == "natural" && tag.Val == "coastline" {
			area = true
		}
	}
	return area
}

func isCCW(coords []Coord) bool {
	// Shoelace formula
	a := 0.0
	for i := 0; i < len(coords); i++ {
		p, q := coords[i], coords[(i+1)%len(coords)]
		a += p.X*q.Y - p.Y*q.X
	}
	return 0.0 <= a
}

func reverseOrientation(coords []Coord) []Coord {
	n := len(coords)
	coords2 := make([]Coord, n)
	for i := 0; i < n/2; i++ {
		coords2[i], coords2[n-i-1] = coords[n-i-1], coords[i]
	}
	if n%2 == 1 {
		coords2[n/2] = coords[n/2]
	}
	return coords2
}

//func fixAreaCoords(bounds Bounds, coords []Coord, filling bool) []Coord {
//	if len(coords) < 3 {
//		return coords
//	}
//	ccw := isCCW(coords) // TODO: this may be wrong for partial areas
//	if coords[0] == coords[len(coords)-1] {
//		// way already closed, remove superfluous last coordinate
//		coords = coords[:len(coords)-1]
//	} else {
//		// add coordinates around bounding box corners to ensure it is properly filled
//		coords = closeAroundBounds(bounds, coords, ccw)
//	}
//	if filling != ccw {
//		reverseOrientation(coords)
//	}
//	return coords
//}

func cohenSutherlandOutcode(bounds Bounds, c Coord) uint8 {
	code := uint8(0b0000)
	if c.X <= bounds[0].X {
		code |= 0b0001 // left
	} else if bounds[1].X <= c.X {
		code |= 0b0010 // right
	}
	if c.Y <= bounds[0].Y {
		code |= 0b0100 // bottom
	} else if bounds[1].Y <= c.Y {
		code |= 0b1000 // top
	}
	return code
}

func clipCoord(bounds Bounds, inner, outer Coord, outerOutcode uint8) Coord {
	if outerOutcode == 0b0001 {
		// left
		t := (bounds[0].X - inner.X) / (outer.X - inner.X)
		return Coord{bounds[0].X, inner.Y + t*(outer.Y-inner.Y)}
	} else if outerOutcode == 0b0010 {
		// right
		t := (bounds[1].X - inner.X) / (outer.X - inner.X)
		return Coord{bounds[1].X, inner.Y + t*(outer.Y-inner.Y)}
	} else if outerOutcode == 0b0100 {
		// bottom
		t := (bounds[0].Y - inner.Y) / (outer.Y - inner.Y)
		return Coord{inner.X + t*(outer.X-inner.X), bounds[0].Y}
	} else if outerOutcode == 0b1000 {
		// top
		t := (bounds[1].Y - inner.Y) / (outer.Y - inner.Y)
		return Coord{inner.X + t*(outer.X-inner.X), bounds[1].Y}
	} else {
		var anchor Coord
		if outerOutcode == 0b0101 {
			// left bottom
			anchor = bounds[0]
		} else if outerOutcode == 0b1001 {
			// left top
			anchor.X, anchor.Y = bounds[0].X, bounds[1].Y
		} else if outerOutcode == 0b0110 {
			// right bottom
			anchor.X, anchor.Y = bounds[1].X, bounds[0].Y
		} else if outerOutcode == 0b1010 {
			// right top
			anchor = bounds[1]
		} else {
			return outer
		}
		tx := (anchor.X - inner.X) / (outer.X - inner.X)
		ty := (anchor.Y - inner.Y) / (outer.Y - inner.Y)
		if tx < ty {
			return Coord{inner.X + tx*(outer.X-inner.X), anchor.Y}
		} else {
			return Coord{anchor.X, inner.Y + ty*(outer.Y-inner.Y)}
		}
	}
}

// closeAroundBounds closes a polygon with start and end points outside of the bounds in a CCW direction.
//func closeAroundBounds(bounds Bounds, coords []Coord, ccw bool) []Coord {
//	if len(coords) < 2 {
//		return coords
//	}
//	dx, dy := 0.1*(bounds[1].X-bounds[0].X), 0.1*(bounds[1].Y-bounds[0].Y)
//	start, end := coords[0], coords[len(coords)-1]
//	startCode, endCode := cohenSutherlandOutcode(bounds, start), cohenSutherlandOutcode(bounds, end)
//	if startCode == 0 || endCode == 0 {
//		// no-op
//		return coords
//	} else if startCode&endCode != 0 {
//		// check if endpoints are on the same side and can simply be closed
//		centre := bounds.Centre()
//		angle := end.Sub(centre).AngleBetween(start.Sub(centre))
//		if ccw && 0.0 < angle || !ccw && angle < 0.0 {
//			return coords
//		}
//	}
//	for {
//		if !ccw {
//			// swap left-top and right-bottom
//			endCode = (endCode & 0b0001 << 3) | (endCode & 0b0010 << 1) | (endCode & 0b0100 >> 1) | (endCode & 0b1000 >> 3)
//		}
//		// go around the clipping area to close the coastline
//		if endCode&0b0001 != 0 && endCode&0b0100 == 0 {
//			// left or top-left
//			coords = append(coords, Coord{bounds[0].X - dx, bounds[0].Y - dy})
//			endCode = 0b0101
//		} else if endCode&0b0100 != 0 && endCode&0b0010 == 0 {
//			// bottom or bottom-left
//			coords = append(coords, Coord{bounds[1].X + dx, bounds[0].Y - dy})
//			endCode = 0b0110
//		} else if endCode&0b0010 != 0 && endCode&0b1000 == 0 {
//			// right or bottom-right
//			coords = append(coords, Coord{bounds[1].X + dx, bounds[1].Y + dy})
//			endCode = 0b1010
//		} else if endCode&0b1000 != 0 && endCode&0b0001 == 0 {
//			// top or top-right
//			coords = append(coords, Coord{bounds[0].X - dx, bounds[1].Y + dy})
//			endCode = 0b0110
//		}
//
//		if startCode&endCode != 0 {
//			return coords
//		}
//	}
//}

// sortRelationWays finds and connects all ways in a relation
//func sortRelationWays(ways []relationWay) []Coord {
//	if len(ways) == 0 {
//		return []Coord{}
//	}
//
//	endpoints := make([][2]Coord, len(ways))
//	for j := 0; j < len(ways); j++ {
//		k := j
//		start, end := ways[j].Coords[0], ways[j].Coords[len(ways[j].Coords)-1]
//		for i := 0; i < j; i++ {
//			if ways[i].Coords != nil {
//				if endpoints[i][0] == end {
//					ways[i].Coords = append(ways[k].Coords, ways[i].Coords...)
//					ways[k].Coords = nil
//					if math.IsNaN(start.X) {
//						break
//					} else {
//						end = Coord{math.NaN(), math.NaN()}
//						k = i
//						i = 0
//						continue
//					}
//				} else if endpoints[i][1] == start {
//					ways[i].Coords = append(ways[i].Coords, ways[k].Coords...)
//					ways[k].Coords = nil
//					if math.IsNaN(end.X) {
//						break
//					} else {
//						start = Coord{math.NaN(), math.NaN()}
//						k = i
//						i = 0
//						continue
//					}
//				}
//			}
//		}
//		endpoints[j] = [2]Coord{start, end}
//	}
//
//	var polygon []Coord
//	for _, way := range ways {
//		if way.Coords != nil {
//			if len(polygon) == 0 {
//				polygon = way.Coords
//			} else {
//				polygon = append(polygon, way.Coords...)
//			}
//		}
//	}
//	return polygon
//}

func appendCoords(a, b []Coord) []Coord {
	for 0 < len(a) && 1 < len(b) {
		if b[0] == a[len(a)-1] {
			a = a[:len(a)-1]
		} else if b[1] == a[len(a)-1] {
			b = b[2:]
		}
	}
	return append(a, b...)
}

// connectRelationWays finds and connects all ways in a relation
func connectRelationWays(ways []relationWay) [][]Coord {
	var polygons [][]Coord
	handled := make([]bool, len(ways))
	wayCoords := make([][]Coord, len(ways))
	endpoints := make([][2]uint64, len(ways))
	for j, way := range ways {
		k := j
		start, end := way.First, way.Last
		if start == end {
			// way closes itself, add directly
			if 2 < len(way.Coords) {
				polygons = append(polygons, way.Coords)
			}
			handled[j] = true
			continue
		}

		wayCoords[j] = way.Coords[:len(way.Coords):len(way.Coords)]

		// loop over remaining ways
		var startConnected, endConnected bool
		for i := 0; i < k; i++ {
			if !handled[i] {
				if !endConnected && endpoints[i][0] == end {
					// current comes before other
					wayCoords[i] = appendCoords(wayCoords[k], wayCoords[i])
					handled[k] = true
					if startConnected {
						break
					} else {
						endConnected = true
						k = i
						i = 0
						continue
					}
				} else if !startConnected && endpoints[i][1] == start {
					// current comes after other
					wayCoords[i] = appendCoords(wayCoords[i], wayCoords[k])
					handled[k] = true
					if endConnected {
						break
					} else {
						startConnected = true
						k = i
						i = 0
						continue
					}
				}
			}
		}
		endpoints[j] = [2]uint64{start, end}
	}
	for i, coords := range wayCoords {
		if !handled[i] && 2 < len(coords) {
			polygons = append(polygons, coords)
		}
	}
	return polygons
}
