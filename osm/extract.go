package osm

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/tdewolff/canvas"
)

// Class is the user-defined class of an object (such as highway or coastline). A zero class is invalid.
type Class int

// FilterFunc returns the class of the object, where returning zero will skip the object.
type FilterFunc func(Type, Tags) Class

type Projector func(float64, float64) (float64, float64)

type Point struct {
	ID    uint64
	Coord Coord
	Tags  Tags
}

func (o *Point) Own() {
	o.Tags = o.Tags.Clone()
}

func (o *Point) Point(projector Projector) canvas.Point {
	x, y := projector(o.Coord.X, o.Coord.Y)
	return canvas.Point{x, y}
}

type LineString struct {
	ID     uint64
	Coords [][]Coord
	Tags   Tags
}

func (o *LineString) Own() {
	o.Coords = slices.Clone(o.Coords)
	for i := 0; i < len(o.Coords); i++ {
		o.Coords[i] = slices.Clone(o.Coords[i])
	}
	o.Tags = o.Tags.Clone()
}

func (o *LineString) Path(projector Projector) *canvas.Path {
	p := &canvas.Path{}
	for _, coords := range o.Coords {
		if 1 < len(coords) {
			x, y := projector(coords[0].X, coords[0].Y)
			p.MoveTo(x, y)
			for _, coord := range coords[1:] {
				x, y := projector(coord.X, coord.Y)
				p.LineTo(x, y)
			}
		}
	}
	return p
}

type Polygon struct {
	ID     uint64
	Coords [][]Coord
	Tags   Tags
}

func (o *Polygon) Own() {
	o.Coords = slices.Clone(o.Coords)
	for i := 0; i < len(o.Coords); i++ {
		o.Coords[i] = slices.Clone(o.Coords[i])
	}
	o.Tags = o.Tags.Clone()
}

func (o *Polygon) Path(projector Projector) *canvas.Path {
	p := &canvas.Path{}
	for _, coords := range o.Coords {
		if 1 < len(coords) {
			x, y := projector(coords[0].X, coords[0].Y)
			p.MoveTo(x, y)
			for _, coord := range coords[1:] {
				x, y := projector(coord.X, coord.Y)
				p.LineTo(x, y)
			}
			p.Close()
		}
	}
	return p
}

type PointFunc func(Class, Point)
type LineStringFunc func(Class, LineString)
type PolygonFunc func(Class, Polygon)

type wayGeom struct {
	Way
	Class  Class
	Coords []Coord
	IsArea bool
}

type relationGeom struct {
	Relation
	Class       Class
	Points      []Point
	LineStrings []LineString
	Polygons    []Polygon
}

func (r *relationGeom) Process(relation Relation, ways map[uint64]wayGeom, relations map[uint64]relationGeom, bounds Bounds) []Coord {
	relationWays := []relationWay{}
	for _, member := range relation.Members {
		if member.Type == WayType {
			if way, ok := ways[member.ID]; ok {
				if way.IsArea {
					if way.Coords[0] == way.Coords[len(way.Coords)-1] {
						// way already closed, remove superfluous last coordinate
						way.Coords = way.Coords[:len(way.Coords)-1]
					} else {
						// add coordinates around bounding box corners to ensure it is properly filled
						way.Coords = closeAroundBounds(way.Coords, bounds)
					}
					// TODO: orient CCW/CW from member.Role
					r.Polygons = append(r.Polygons, Polygon{
						ID:     way.ID,
						Coords: [][]Coord{way.Coords},
						Tags:   way.Tags,
					})
				} else {
					relationWays = append(relationWays, relationWay{way.Coords, member.Role != "inner"})
				}
			}
		} else if member.Type == RelationType {
			if relation, ok := relations[member.ID]; ok {
				coords := r.Process(relation.Relation, ways, relations, bounds)
				relationWays = append(relationWays, relationWay{coords, member.Role != "inner"})
			}
		}
	}
	return sortRelationWays(relationWays)
}

func (z *Parser) ExtractSimple(ctx context.Context, bounds Bounds, filter FilterFunc, point PointFunc, lineString LineStringFunc, polygon PolygonFunc) error {
	var mu sync.Mutex
	nodeIDs := map[uint64]struct{}{} // matches bounds
	nodeFunc := func(node Node) {
		if bounds.Contains(Coord{node.Lon, node.Lat}) {
			mu.Lock()
			nodeIDs[node.ID] = struct{}{}
			mu.Unlock()
			if point != nil {
				var class Class
				if filter != nil {
					class = filter(NodeType, node.Tags)
				}
				if filter == nil || class != 0 {
					point(class, Point{
						ID:    node.ID,
						Coord: Coord{node.Lon, node.Lat},
						Tags:  node.Tags.Clone(),
					})
				}
			}
		}
	}
	if err := z.Parse(ctx, nodeFunc, nil, nil); err != nil {
		return err
	}

	ways := []wayGeom{}
	wayIDs := map[uint64]struct{}{} // matches bounds
	nodeWayIndices := map[uint64][2]int{}
	wayFunc := func(way Way) {
		for _, ref := range way.Refs {
			if _, ok := nodeIDs[ref]; ok {
				way.Own()
				var class Class
				if filter != nil {
					class = filter(WayType, way.Tags)
				}
				mu.Lock()
				if filter == nil || class != 0 {
					for i, ref := range way.Refs {
						nodeWayIndices[ref] = [2]int{len(ways), i}
					}
					ways = append(ways, wayGeom{
						Way:    way,
						Class:  class,
						Coords: make([]Coord, len(way.Refs)),
						IsArea: way.Tags.IsArea(),
					})
				}
				wayIDs[way.ID] = struct{}{}
				mu.Unlock()
				break
			}
		}
	}
	if err := z.Parse(ctx, nil, wayFunc, nil); err != nil {
		return err
	}

	nodeFunc = func(node Node) {
		if indices, ok := nodeWayIndices[node.ID]; ok {
			// node is a member of a way
			ways[indices[0]].Coords[indices[1]] = Coord{node.Lon, node.Lat}
		}
	}
	//relationFunc := func(relation Relation) {
	//	for _, member:=range relation.Members{
	//		if member.Type==NodeType{
	//			if _,  ok:= nodeIDs[member.ID];ok{
	//				break
	//			}
	//		} else if member.Type==WayType{
	//		}
	//	}
	//}
	if err := z.Parse(ctx, nodeFunc, nil, nil); err != nil {
		return err
	}

	for _, way := range ways {
		// properly close incomplete areas
		if way.IsArea {
			if way.Coords[0] == way.Coords[len(way.Coords)-1] {
				// way already closed, remove superfluous last coordinate
				way.Coords = way.Coords[:len(way.Coords)-1]
			} else {
				// add coordinates around bounding box corners to ensure it is properly filled
				way.Coords = closeAroundBounds(way.Coords, bounds)
			}
		}

		if way.IsArea {
			if polygon != nil {
				polygon(way.Class, Polygon{
					ID:     way.ID,
					Coords: [][]Coord{way.Coords},
					Tags:   way.Tags,
				})
			}
		} else {
			if lineString != nil {
				lineString(way.Class, LineString{
					ID:     way.ID,
					Coords: [][]Coord{way.Coords},
					Tags:   way.Tags,
				})
			}
		}
	}
	return nil
}

// Extract extracts a subset of the data that is within the bounds. If filter is not nil, it will also filter based on object types or tags. The point, lineString, and polygon callback functions are called for each filtered object within the bounds; nodes are passed to point, ways and relations their dependencies are resolved and call lineString (unclosed ways/boundaries) or polygon (closed ways/areas). This function is optimised to limit peak memory usage.
func (z *Parser) Extract(ctx context.Context, bounds Bounds, filter FilterFunc, point PointFunc, lineString LineStringFunc, polygon PolygonFunc) error {
	// TODO: use faster uint64 set https://github.com/shenwei356/uintset and maybe map[uint64]uint64

	// PASS 1: find contained node IDs, find super-relations, and return matching nodes
	var mu1, mu2, mu3, mu4 sync.Mutex
	nodeIDs := map[uint64]Class{}             // matches bounds and filter
	nodeContainedIDs := map[uint64]struct{}{} // matches bounds
	wayParents := map[uint64]uint64{}         // wayID => relationID, matches filter
	relationIDs := map[uint64]Class{}         // matches filter
	relationParents := map[uint64]uint64{}    // relationID => relationID, matches filter
	nodeFunc := func(node Node) {
		if bounds.Contains(Coord{node.Lon, node.Lat}) {
			var class Class
			if filter != nil {
				class = filter(NodeType, node.Tags)
			}
			mu1.Lock()
			nodeContainedIDs[node.ID] = struct{}{}
			if filter == nil || class != 0 {
				nodeIDs[node.ID] = class
			}
			mu1.Unlock()
		}
	}
	relationFunc := func(relation Relation) {
		var class Class
		if filter != nil {
			class = filter(RelationType, relation.Tags)
		}
		if filter == nil || class != 0 {
			// register relation parents for ways and relations where the parent matches the filter
			mu2.Lock()
			relationIDs[relation.ID] = class
			for _, member := range relation.Members {
				if member.Type == WayType {
					wayParents[member.ID] = relation.ID
				} else if member.Type == RelationType {
					relationParents[member.ID] = relation.ID
				}
			}
			mu2.Unlock()
		}
	}
	if err := z.Parse(ctx, nodeFunc, nil, relationFunc); err != nil {
		return err
	}
	fmt.Println("A")

	// PASS 2: find ways and relations that have a contained node
	ways := []wayGeom{}                           // prepared ways as they are selected or referenced by selected relations
	nodeWayIndices := map[uint64][2]int{}         // NodeID => {index into ways, index into way.Coords}
	relations := []relationGeom{}                 // prepared relations as they are selected or referenced by other selected relations
	nodeRelationIndices := map[uint64]int{}       // NodeID => index into relations
	relationContainedIDs := map[uint64]struct{}{} // matching bounds and filter
	wayFunc := func(way Way) {
		for first, ref := range way.Refs {
			if _, ok := nodeContainedIDs[ref]; ok {
				// way is contained; add parent ways and relations
				if relationID, ok := wayParents[way.ID]; ok {
					// has parent relation that matches filter
					mu1.Lock()
					relationContainedIDs[relationID] = struct{}{}
					for {
						if relationID, ok = relationParents[relationID]; ok {
							relationContainedIDs[relationID] = struct{}{}
						} else {
							break
						}
					}
					mu1.Unlock()
				}

				// prepare to fill coordinates
				var class Class
				if filter != nil {
					class = filter(WayType, way.Tags)
				}
				if _, ok := wayParents[way.ID]; ok || filter == nil || class != 0 {
					// has parent relation that matches filter, or the way is matches filter
					if 0 < first {
						first-- // include first node outside of bounds
					}
					last := len(way.Refs)
					for first < last {
						if _, ok := nodeContainedIDs[way.Refs[last-1]]; ok {
							if last < len(way.Refs) {
								last++ // include first node outside of bounds
							}
							break
						}
						last--
					}
					if !ok {
						// clone tags if this way will be returned
						way.Tags = way.Tags.Clone()
					}
					mu2.Lock()
					for index, ref := range way.Refs[first:last] {
						nodeWayIndices[ref] = [2]int{len(ways), index}
					}
					ways = append(ways, wayGeom{
						Way:    way,
						Class:  class,
						Coords: make([]Coord, last-first),
						IsArea: way.Tags.IsArea(),
					})
					mu2.Unlock()
				}
				break
			}
		}
	}
	relationFunc = func(relation Relation) {
		if class, ok := relationIDs[relation.ID]; ok || filter == nil {
			// register node IDs also selected by relations
			for _, member := range relation.Members {
				if member.Type == NodeType {
					if _, ok := nodeContainedIDs[member.ID]; ok {
						mu3.Lock()
						nodeRelationIndices[member.ID] = len(relations)
						mu3.Unlock()
					}
				}
			}

			// prepare relation to be filled
			relation.Own()
			mu4.Lock()
			relations = append(relations, relationGeom{
				Relation: relation,
				Class:    class,
			})
			mu4.Unlock()
		}
	}
	if err := z.Parse(ctx, nil, wayFunc, relationFunc); err != nil {
		return err
	}
	clear(relationIDs)
	clear(nodeContainedIDs)
	fmt.Println("B")

	// PASS 3: fill way coordinates and return nodes
	nodeFunc = func(node Node) {
		if indices, ok := nodeWayIndices[node.ID]; ok {
			// node is a member of a way
			mu1.Lock()
			ways[indices[0]].Coords[indices[1]] = Coord{node.Lon, node.Lat}
			mu1.Unlock()
		} else if index, ok := nodeRelationIndices[node.ID]; ok {
			// node is a member of a relation, fill into relation
			mu2.Lock()
			relations[index].Points = append(relations[index].Points, Point{
				ID:    node.ID,
				Coord: Coord{node.Lon, node.Lat},
				Tags:  node.Tags.Clone(),
			})
			mu2.Unlock()
		} else if class, ok := nodeIDs[node.ID]; ok && point != nil {
			point(class, Point{
				ID:    node.ID,
				Coord: Coord{node.Lon, node.Lat},
				Tags:  node.Tags.Clone(),
			})
		}
	}
	if err := z.Parse(ctx, nodeFunc, nil, nil); err != nil {
		return err
	}
	clear(nodeRelationIndices)
	clear(nodeWayIndices)
	clear(nodeIDs)
	fmt.Println("C")

	// fill coordinates in ways and return them
	for _, way := range ways {
		// properly close incomplete areas
		if way.IsArea {
			if way.Coords[0] == way.Coords[len(way.Coords)-1] {
				// way already closed, remove superfluous last coordinate
				way.Coords = way.Coords[:len(way.Coords)-1]
			} else {
				// add coordinates around bounding box corners to ensure it is properly filled
				way.Coords = closeAroundBounds(way.Coords, bounds)
			}
		}

		if _, ok := wayParents[way.ID]; !ok {
			// not used by selected relations
			if way.IsArea {
				if polygon != nil {
					polygon(way.Class, Polygon{
						ID:     way.ID,
						Coords: [][]Coord{way.Coords},
						Tags:   way.Tags,
					})
				}
			} else {
				if lineString != nil {
					lineString(way.Class, LineString{
						ID:     way.ID,
						Coords: [][]Coord{way.Coords},
						Tags:   way.Tags,
					})
				}
			}
		}
	}

	// fill relations and return them
	waysMap := make(map[uint64]wayGeom, len(ways))
	for _, way := range ways {
		waysMap[way.ID] = way
	}
	clear(ways)
	relationsMap := make(map[uint64]relationGeom, len(relations))
	for _, relation := range relations {
		relationsMap[relation.ID] = relation
	}
	clear(relations)
	for _, relation := range relations {
		if _, ok := relationContainedIDs[relation.ID]; ok {
			if _, ok := relationParents[relation.ID]; !ok {
				// is top-level relation for selection
				coords := relation.Process(relation.Relation, waysMap, relationsMap, bounds)
				if relation.Tags.IsArea() {
					if coords[0] == coords[len(coords)-1] {
						// way already closed, remove superfluous last coordinate
						coords = coords[:len(coords)-1]
					} else {
						// add coordinates around bounding box corners to ensure it is properly filled
						coords = closeAroundBounds(coords, bounds)
					}
					relation.Polygons = append(relation.Polygons, Polygon{
						ID:     relation.ID,
						Coords: [][]Coord{coords},
						Tags:   relation.Tags,
					})
				} else {
					relation.LineStrings = append(relation.LineStrings, LineString{
						ID:     relation.ID,
						Coords: [][]Coord{coords},
						Tags:   relation.Tags,
					})
				}

				if point != nil && 0 < len(relation.Points) {
					for _, o := range relation.Points {
						point(relation.Class, o)
					}
				}
				if lineString != nil && 0 < len(relation.LineStrings) {
					for _, o := range relation.LineStrings {
						lineString(relation.Class, o)
					}
				}
				if polygon != nil && 0 < len(relation.Polygons) {
					for _, o := range relation.Polygons {
						polygon(relation.Class, o)
					}
				}
			}
		}
	}
	return nil
}
