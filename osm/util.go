package osm

import "math"

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

func (b Bounds) Contains(c Coord) bool {
	return b[0].X <= c.X && c.X <= b[1].X && b[0].Y <= c.Y && c.Y <= b[1].Y
}

func (b Bounds) Center() Coord {
	return Coord{(b[0].X + b[1].X) / 2.0, (b[0].Y + b[1].Y) / 2.0}
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

func cohenSutherlandOutcode(bounds Bounds, c Coord) int {
	code := 0b0000
	if c.X < bounds[0].X {
		code |= 0b0001 // left
	} else if bounds[1].X < c.X {
		code |= 0b0010 // right
	}
	if c.Y < bounds[0].Y {
		code |= 0b0100 // bottom
	} else if bounds[1].Y < c.Y {
		code |= 0b1000 // top
	}
	return code
}

// closeAroundBounds closes a polygon with start and end points outside of the bounds in a CCW direction.
func closeAroundBounds(coords []Coord, bounds Bounds) []Coord {
	if len(coords) < 2 {
		return coords
	}
	dx, dy := 0.1*(bounds[1].X-bounds[0].X), 0.1*(bounds[1].Y-bounds[0].Y)
	start, end := coords[0], coords[len(coords)-1]
	startCode, endCode := cohenSutherlandOutcode(bounds, start), cohenSutherlandOutcode(bounds, end)
	if startCode == 0 || endCode == 0 {
		// no-op
		return coords
	} else if startCode&endCode != 0 {
		// check if endpoints are on the same side and can simply be closed
		center := bounds.Center()
		angle := end.Sub(center).AngleBetween(start.Sub(center))
		if 0.0 < angle {
			return coords
		}
	}
	for {
		// go around the clipping area to close the coastline
		if endCode&0b0001 != 0 && endCode&0b0100 == 0 {
			// left or top-left
			coords = append(coords, Coord{bounds[0].X - dx, bounds[0].Y - dy})
			endCode = 0b0101
		} else if endCode&0b0100 != 0 && endCode&0b0010 == 0 {
			// bottom or bottom-left
			coords = append(coords, Coord{bounds[1].X + dx, bounds[0].Y - dy})
			endCode = 0b0110
		} else if endCode&0b0010 != 0 && endCode&0b1000 == 0 {
			// right or bottom-right
			coords = append(coords, Coord{bounds[1].X + dx, bounds[1].Y + dy})
			endCode = 0b1010
		} else if endCode&0b1000 != 0 && endCode&0b0001 == 0 {
			// top or top-right
			coords = append(coords, Coord{bounds[0].X - dx, bounds[1].Y + dy})
			endCode = 0b0110
		}

		if startCode&endCode != 0 {
			return coords
		}
	}
}

type relationWay struct {
	Coords []Coord
	Fill   bool
}

// sortRelationWays finds and connects all ways in a relation
func sortRelationWays(ways []relationWay) []Coord {
	if len(ways) == 0 {
		return []Coord{}
	}

	endpoints := make([][2]Coord, len(ways))
	for j := 0; j < len(ways); j++ {
		k := j
		start, end := ways[j].Coords[0], ways[j].Coords[len(ways[j].Coords)-1]
		for i := 0; i < j; i++ {
			if ways[i].Coords != nil {
				if endpoints[i][0] == end {
					ways[i].Coords = append(ways[k].Coords, ways[i].Coords...)
					ways[k].Coords = nil
					if math.IsNaN(start.X) {
						break
					} else {
						end = Coord{math.NaN(), math.NaN()}
						k = i
						i = 0
						continue
					}
				} else if endpoints[i][1] == start {
					ways[i].Coords = append(ways[i].Coords, ways[k].Coords...)
					ways[k].Coords = nil
					if math.IsNaN(end.X) {
						break
					} else {
						start = Coord{math.NaN(), math.NaN()}
						k = i
						i = 0
						continue
					}
				}
			}
		}
		endpoints[j] = [2]Coord{start, end}
	}

	var polygon []Coord
	for _, way := range ways {
		if way.Coords != nil {
			if len(polygon) == 0 {
				polygon = way.Coords
			} else {
				polygon = append(polygon, way.Coords...)
			}
		}
	}
	// TODO: use Fill to orient CCW/CW
	return polygon

	// TODO: test
	//ok := true
	//coastline := &canvas.Path{}
	//for _, way := range ways {
	//	if way != nil {
	//		coastline2, ok2 := closeExteriorContour(way, clip)
	//		if !ok2 {
	//			ok = false
	//		}
	//		coastline = coastline.Append(coastline2)
	//	}
	//}
	//return coastline, ok
}
