package osm

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Class is the user-defined class of an object (such as highway or coastline). A zero class is invalid.
type Class uint32

// FilterFunc returns the class of the object, where returning zero will skip the object.
type FilterFunc func(Type, uint64, Tags) Class

type Polygon struct {
	Coords []Coord
	Fill   bool
}

type Geometry struct {
	Type        Type
	ID          uint64
	Points      []Coord
	LineStrings [][]Coord
	Polygons    []Polygon
	Tags        Tags
}

type wayNode struct {
	Coord   Coord
	Class   Class
	Outcode uint8
}

type relationWay struct {
	Coords      []Coord
	First, Last uint64 // IDs of first and last node
}

// Extract extracts a subset of the data that is within the bounds. If filter is not nil, it will also filter based on object types, IDs, or tags. It will parse and resolve all selected geometries and categorise by class. This function is optimised to limit peak memory usage but requires parsing the file three times (or five if filter is set).
// - There is no guarantee of order between geometries.
// - Nodes within or on bounds and ways that pass through the bounds are matched. Relations contain the members that matched.
// - Multiple ways in a relation are joined by their endpoints (referencing same nodes). The result is either a line string (open) or a polygon (closed). A relation may have multiple sets of ways with no matching endpoints.
// - Line strings and polygons are clipped to the bounds and any superfluous nodes are removed. Care is taken to maintain direction and closedness.
// - Filled polygons are CCW oriented and holes are CW oriented.
func (z *Parser) Extract(ctx context.Context, bounds Bounds, filter FilterFunc) (map[Class][]Geometry, error) {
	var mu1, mu2, mu3 sync.RWMutex

	selectedNodes := NewUint64Map(8, 0.6)     // matches filter
	selectedWays := NewUint64Map(8, 0.6)      // matches filter
	selectedRelations := NewUint64Map(8, 0.6) // matches filter
	if filter != nil {
		// add relation dependents and build dependent trees for super relations
		relationFunc := func(relation Relation) {
			if class := filter(RelationType, relation.ID, relation.Tags); class != 0 {
				var nodes, ways []uint64
				for _, member := range relation.Members {
					if member.Type == WayType {
						ways = append(ways, member.ID)
					} else if member.Type == NodeType {
						nodes = append(nodes, member.ID)
					}
				}
				if 0 < len(ways) || 0 < len(nodes) {
					mu1.Lock()
					selectedRelations.Put(relation.ID, uint64(class))
					mu1.Unlock()
				}
				if 0 < len(ways) {
					mu2.Lock()
					for _, id := range ways {
						selectedWays.Put(id, 0)
					}
					mu2.Unlock()
				}
				if 0 < len(nodes) {
					mu3.Lock()
					for _, id := range nodes {
						selectedNodes.Put(id, 0)
					}
					mu3.Unlock()
				}
			}
		}
		if err := z.Parse(ctx, nil, nil, relationFunc); err != nil {
			return nil, err
		}

		// add way dependents
		wayFunc := func(way Way) {
			if class := filter(WayType, way.ID, way.Tags); class != 0 || selectedWays.Has(way.ID) {
				if class != 0 {
					// overwrite class if selected by relation
					mu1.Lock()
					selectedWays.Put(way.ID, uint64(class))
					mu1.Unlock()
				}

				// add node dependents
				mu2.Lock()
				for _, ref := range way.Refs {
					selectedNodes.Put(ref, 0)
				}
				mu2.Unlock()
			}
		}
		if err := z.Parse(ctx, nil, wayFunc, nil); err != nil {
			return nil, err
		}
	}

	geometries := map[Class][]Geometry{}

	nodes := map[uint64]wayNode{}
	nodeFunc := func(node Node) {
		var class Class
		if filter != nil {
			class = filter(NodeType, node.ID, node.Tags)
		}
		outcode := cohenSutherlandOutcode(bounds, Coord{node.Lon, node.Lat})

		mu1.Lock()
		if filter == nil || selectedNodes.Has(node.ID) {
			nodes[node.ID] = wayNode{
				Coord:   Coord{node.Lon, node.Lat},
				Class:   class,
				Outcode: outcode,
			}
		}
		mu1.Unlock()
		if (filter == nil || class != 0) && outcode == 0b0000 {
			mu2.Lock()
			geometries[class] = append(geometries[class], Geometry{
				Type:   NodeType,
				ID:     node.ID,
				Points: []Coord{{node.Lon, node.Lat}},
				Tags:   node.Tags.Clone(),
			})
			mu2.Unlock()
		}
	}
	if err := z.Parse(ctx, nodeFunc, nil, nil); err != nil {
		return nil, err
	}
	selectedNodes = nil

	ways := map[uint64]relationWay{}
	if filter == nil || 0 < selectedWays.Size() {
		wayFunc := func(way Way) {
			var class Class
			selected := true
			if filter != nil {
				var wayItem uint64
				mu1.RLock()
				wayItem, selected = selectedWays.Get(way.ID)
				mu1.RUnlock()
				class = Class(wayItem)
			}
			if selected && 0 < len(way.Refs) {
				// filtered (class!=0) or dependent (class=0)
				// optimise way: remove superfluous nodes outside of the bounds
				var coords []Coord
				var prevCoord Coord
				prevOutcode := uint8(0b1111)
				for _, ref := range way.Refs {
					if node, ok := nodes[ref]; ok {
						// node exists
						coord, outcode := node.Coord, node.Outcode
						if outcode == 0b0000 {
							if prevOutcode != 0b0000 && prevOutcode != 0b1111 {
								// cross bounds to inside
								coords = append(coords, clipCoord(bounds, coord, prevCoord, prevOutcode))
							}
							coords = append(coords, coord)
						} else if prevOutcode == 0b0000 {
							// cross bounds to outside
							coords = append(coords, clipCoord(bounds, prevCoord, coord, outcode))
						} else if outcode != prevOutcode {
							// both are outside but in different outcode regions
							// put coordinates at the corners of the bounds to avoid crossing the inner region
							corner := true
							if outcode == 0b0101 {
								// left bottom
								coord = bounds[0]
							} else if outcode == 0b1001 {
								// left top
								coord = Coord{bounds[0].X, bounds[1].Y}
							} else if outcode == 0b0110 {
								// right bottom
								coord = Coord{bounds[1].X, bounds[0].Y}
							} else if outcode == 0b1010 {
								// right top
								coord = bounds[1]
							} else {
								corner = false
							}
							if corner && (len(coords) == 0 || coords[len(coords)-1] != coord) {
								if 1 < len(coords) && coords[len(coords)-2] == coord {
									// optimise and avoid overlapping segments
									coords = coords[:len(coords)-1]
								} else {
									coords = append(coords, coord)
								}
							}
						}
						prevOutcode = outcode
						prevCoord = coord
					}
				}

				closed := way.Refs[0] == way.Refs[len(way.Refs)-1]
				if (closed && len(coords) < 3) || (!closed && len(coords) < 2) {
					coords = nil
				} else if closed && coords[0] != coords[len(coords)-1] {
					coords = append(coords, coords[0])
				}

				if (filter == nil || class != 0) && coords != nil {
					// is (partially) inside or surrounds bounds
					geom := Geometry{
						Type: WayType,
						ID:   way.ID,
						Tags: way.Tags.Clone(),
					}
					if closed && way.Tags.IsArea() {
						if !isCCW(coords) {
							reverseOrientation(coords)
						}
						geom.Polygons = []Polygon{{coords, true}}
					} else {
						geom.LineStrings = [][]Coord{coords}
					}
					mu2.Lock()
					geometries[class] = append(geometries[class], geom)
					mu2.Unlock()
				}

				mu3.Lock()
				ways[way.ID] = relationWay{
					Coords: coords,
					First:  way.Refs[0],
					Last:   way.Refs[len(way.Refs)-1],
				}
				mu3.Unlock()
			}
		}
		if err := z.Parse(ctx, nil, wayFunc, nil); err != nil {
			return nil, err
		}
	}
	selectedWays = nil

	if filter == nil || 0 < selectedRelations.Size() {
		relationFunc := func(relation Relation) {
			var class Class
			if filter != nil {
				mu1.RLock()
				relationItem, _ := selectedRelations.Get(relation.ID)
				mu1.RUnlock()
				class = Class(relationItem)
			}
			if (filter == nil || class != 0) && 0 < len(relation.Members) {
				geom := Geometry{
					Type: RelationType,
					ID:   relation.ID,
					Tags: relation.Tags, // cloned later
				}

				relationWayRoles := map[string][]relationWay{}
				for _, member := range relation.Members {
					if member.Type == WayType {
						if way, ok := ways[member.ID]; ok {
							relationWayRoles[member.Role] = append(relationWayRoles[member.Role], way)
						}
					} else if member.Type == NodeType {
						if node, ok := nodes[member.ID]; ok && node.Outcode == 0b0000 {
							geom.Points = append(geom.Points, node.Coord)
						}
					}
				}

				for role, relationWays := range relationWayRoles {
					contours := connectRelationWays(relationWays)
					for _, coords := range contours {
						if 1 < len(coords) {
							if 2 < len(coords) && coords[0] == coords[len(coords)-1] {
								// closed
								fill := role != "inner"
								if fill != isCCW(coords) {
									coords = reverseOrientation(coords)
								}
								geom.Polygons = append(geom.Polygons, Polygon{
									Coords: coords,
									Fill:   fill,
								})
							} else {
								// open
								if role == "outer" || role == "inner" {
									fmt.Printf("WARNING: could not close %v ways in relation %v\n", role, relation.ID)
								}
								geom.LineStrings = append(geom.LineStrings, coords)
							}
						}
					}
				}

				if 0 < len(geom.Points) || 0 < len(geom.LineStrings) || 0 < len(geom.Polygons) {
					// sort polygons by filling first
					slices.SortFunc(geom.Polygons, func(a, b Polygon) int {
						if a.Fill == b.Fill {
							return 0
						} else if a.Fill {
							return -1
						}
						return 1
					})

					geom.Tags = geom.Tags.Clone()

					mu2.Lock()
					geometries[class] = append(geometries[class], geom)
					mu2.Unlock()
				}
			}
		}
		if err := z.Parse(ctx, nil, nil, relationFunc); err != nil {
			return nil, err
		}
	}
	return geometries, nil
}

//type NodeGeom struct {
//	ID    uint64
//	Coord Coord
//	Tags  Tags
//}

//type WayGeom struct {
//	ID     uint64
//	Coords []Coord
//	Tags   Tags
//}

//type RelationGeom struct {
//	ID          uint64
//	Points      []Coord
//	LineStrings [][]Coord
//	Polygons    []Polygon
//	Tags        Tags
//}

//type PointFunc func(Class, Point)
//type LineStringFunc func(Class, LineString)
//type PolygonFunc func(Class, Polygon)
//
//type wayGeom struct {
//	Way
//	Class  Class
//	Coords []Coord
//	IsArea bool
//}
//
//type relationWay struct {
//	Coords []Coord
//	Fill   bool
//}
//
//type relationGeom struct {
//	Relation
//	Class       Class
//	Points      []Point
//	LineStrings []LineString
//	Polygons    []Polygon
//}
//
//// Process finds all child ways and relations, adds all closed areas to Polygons and accumulates all open ways and sorts them
//func (r *relationGeom) Process(relation Relation, ways map[uint64]wayGeom, relations map[uint64]relationGeom, bounds Bounds) []Coord {
//	relationWays := []relationWay{}
//	for _, member := range relation.Members {
//		if member.Type == WayType {
//			if way, ok := ways[member.ID]; ok {
//				//if way.IsArea {
//				//	way.Coords = fixAreaCoords(bounds, way.Coords, member.Role != "inner")
//				//	r.Polygons = append(r.Polygons, Polygon{
//				//		ID:     way.ID,
//				//		Type:   WayType,
//				//		Coords: [][]Coord{way.Coords},
//				//		Tags:   way.Tags,
//				//	})
//				//} else {
//				relationWays = append(relationWays, relationWay{way.Coords, member.Role != "inner"})
//				//}
//			}
//		} else if member.Type == RelationType {
//			if relation, ok := relations[member.ID]; ok {
//				coords := r.Process(relation.Relation, ways, relations, bounds)
//				//if relation.Tags.IsArea() {
//				//	coords = fixAreaCoords(bounds, coords, member.Role != "inner")
//				//	r.Polygons = append(r.Polygons, Polygon{
//				//		ID:     relation.ID,
//				//		Type:   RelationType,
//				//		Coords: [][]Coord{coords},
//				//		Tags:   relation.Tags,
//				//	})
//				//} else {
//				relationWays = append(relationWays, relationWay{coords, member.Role != "inner"})
//				//}
//			}
//		}
//	}
//	return sortRelationWays(relationWays)
//}

// Extract extracts a subset of the data that is within the bounds. If filter is not nil, it will also filter based on object types or tags. The point, lineString, and polygon callback functions are called for each filtered object within the bounds; nodes are passed to point, for ways and relations their dependencies are resolved and call lineString (unclosed ways/boundaries) or polygon (closed ways/areas). This function is optimised to limit peak memory usage but requires parsing the file three times.
// - All callbacks are called sequentially (not concurrently), and in order of nodes, ways and then relations. There is no guarantee of order between elements.
// - The point, lineString and polygon callback functions can be nil to be skipped, this is slightly more efficient.
// - Ways are within bounds if at least one node is within bounds, relations are within bounds if at least one member is within bounds.
// - Ways and way members of relations are either a line string or a polygon determined by the tags. Polygons may be partial if a part is outside of the bounds, those polygons will be closed around the edges of the bounds in a CCW direction (or CW for holes).
// - Filled polygons are CCW oriented and holes are CW oriented.
// - If a relation consists of multiple members of both open and closed paths, those closed paths of members are returned separately. The open paths of the relation are sorted so that the endpoints connect, and if the relation itself is an area it will be returned as a polygon. This happens recursively for nested relations/ways in relations.
//func (z *Parser) Extract(ctx context.Context, bounds Bounds, filter FilterFunc, point PointFunc, lineString LineStringFunc, polygon PolygonFunc) error {
//	// PASS 1: find contained node IDs, find matching parents for ways, and relations (nodes are handled differently)
//	var mu1, mu2, mu3, mu4 sync.Mutex
//	nodeContainedIDs := NewUint64Set(8, 0.6) // matches bounds
//	relationIDs := map[uint64]Class{}        // matches filter
//	wayParents := NewUint64Map(8, 0.6)       // wayID => relationID, relation matches filter
//	relationParents := NewUint64Map(8, 0.6)  // relationID => relationID, relation matches filter
//	nodeFunc := func(node Node) {
//		if bounds.Contains(Coord{node.Lon, node.Lat}) {
//			mu1.Lock()
//			nodeContainedIDs.Add(node.ID)
//			mu1.Unlock()
//		}
//	}
//	relationFunc := func(relation Relation) {
//		var class Class
//		if filter != nil {
//			class = filter(RelationType, relation.Tags)
//		}
//		if filter == nil || class != 0 {
//			// register relation parents for ways and relations where the parent matches the filter
//			mu2.Lock()
//			relationIDs[relation.ID] = class
//			for _, member := range relation.Members {
//				if member.Type == WayType {
//					wayParents.Put(member.ID, relation.ID)
//				} else if member.Type == RelationType {
//					relationParents.Put(member.ID, relation.ID)
//				}
//			}
//			mu2.Unlock()
//		}
//	}
//	if err := z.Parse(ctx, nodeFunc, nil, relationFunc); err != nil {
//		return err
//	}
//
//	// PASS 2: find ways and relations that have a contained node
//	ways := []wayGeom{}                          // prepared ways as they are selected or referenced by selected relations
//	nodeWayIndices := map[uint64][][2]int{}      // NodeID => {index into ways, index into way.Coords}
//	wayContainedIDs := NewUint64Set(8, 0.6)      // matching bounds and filter
//	relations := []relationGeom{}                // prepared relations as they are selected or referenced by other selected relations
//	nodeRelationIndices := map[uint64][]int{}    // NodeID => index into relations
//	relationContainedIDs := NewUint64Set(8, 0.6) // matching bounds and filter
//	wayFunc := func(way Way) {
//		for first, ref := range way.Refs {
//			if nodeContainedIDs.Has(ref) {
//				fmt.Println("W", way)
//				// way is contained; prepare to fill coordinates
//				var class Class
//				if filter != nil {
//					class = filter(WayType, way.Tags)
//				}
//				if relationID, hasParent := wayParents.Get(way.ID); hasParent || filter == nil || class != 0 {
//					// add parent relations
//					if hasParent {
//						// has parent relation that matches filter
//						mu1.Lock()
//						relationContainedIDs.Add(relationID)
//						for {
//							var ok bool
//							if relationID, ok = relationParents.Get(relationID); ok {
//								relationContainedIDs.Add(relationID)
//							} else {
//								break
//							}
//						}
//						mu1.Unlock()
//					}
//
//					// either has parent relation that matches filter or the way itself matches filter
//					if 0 < first {
//						first-- // include first node outside of bounds
//					}
//					last := len(way.Refs)
//					for first < last {
//						if nodeContainedIDs.Has(way.Refs[last-1]) {
//							if last < len(way.Refs) {
//								last++ // include first node outside of bounds
//							}
//							break
//						}
//						last--
//					}
//					if filter == nil || class != 0 {
//						// clone tags if this way will be returned
//						way.Tags = way.Tags.Clone()
//						wayContainedIDs.Add(way.ID)
//					}
//					mu2.Lock()
//					for index, ref := range way.Refs[first:last] {
//						nodeWayIndices[ref] = append(nodeWayIndices[ref], [2]int{len(ways), index})
//					}
//					ways = append(ways, wayGeom{
//						Way:    way,
//						Class:  class,
//						Coords: make([]Coord, last-first),
//						IsArea: way.Tags.IsArea(),
//					})
//					mu2.Unlock()
//				}
//				break
//			}
//		}
//	}
//	relationFunc = func(relation Relation) {
//		if class, ok := relationIDs[relation.ID]; ok || filter == nil {
//			// register node IDs also selected by relations
//			for _, member := range relation.Members {
//				if member.Type == NodeType {
//					if nodeContainedIDs.Has(member.ID) {
//						mu3.Lock()
//						nodeRelationIndices[member.ID] = append(nodeRelationIndices[member.ID], len(relations))
//						mu3.Unlock()
//					}
//				}
//			}
//
//			// prepare relation to be filled
//			relation.Own()
//			mu4.Lock()
//			relations = append(relations, relationGeom{
//				Relation: relation,
//				Class:    class,
//			})
//			mu4.Unlock()
//		}
//	}
//	if err := z.Parse(ctx, nil, wayFunc, relationFunc); err != nil {
//		return err
//	}
//	relationIDs = nil
//
//	// PASS 3: fill way coordinates and return nodes
//	nodeFunc = func(node Node) {
//		if indices, ok := nodeWayIndices[node.ID]; ok {
//			// node is a member of a way, fill into way
//			mu1.Lock()
//			for _, index := range indices {
//				ways[index[0]].Coords[index[1]] = Coord{node.Lon, node.Lat}
//			}
//			mu1.Unlock()
//		}
//		if indices, ok := nodeRelationIndices[node.ID]; ok {
//			// node is a member of a relation, fill into relation
//			mu2.Lock()
//			for _, index := range indices {
//				relations[index].Points = append(relations[index].Points, Point{
//					ID:    node.ID,
//					Coord: Coord{node.Lon, node.Lat},
//					Tags:  node.Tags.Clone(),
//				})
//			}
//			mu2.Unlock()
//		}
//		if point != nil && nodeContainedIDs.Has(node.ID) {
//			var class Class
//			if filter != nil {
//				class = filter(NodeType, node.Tags)
//			}
//			if filter == nil || class != 0 {
//				mu3.Lock()
//				point(class, Point{
//					ID:    node.ID,
//					Coord: Coord{node.Lon, node.Lat},
//					Tags:  node.Tags.Clone(),
//				})
//				mu3.Unlock()
//			}
//		}
//	}
//	if err := z.Parse(ctx, nodeFunc, nil, nil); err != nil {
//		return err
//	}
//	nodeContainedIDs = nil
//	nodeRelationIndices = nil
//	nodeWayIndices = nil
//
//	// fill coordinates in ways and return them
//	if lineString != nil || polygon != nil {
//		for _, way := range ways {
//			// properly close incomplete areas
//			if way.IsArea {
//				way.Coords = fixAreaCoords(bounds, way.Coords, true)
//			}
//
//			if wayContainedIDs.Has(way.ID) {
//				if way.IsArea {
//					if polygon != nil {
//						polygon(way.Class, Polygon{
//							ID:     way.ID,
//							Type:   WayType,
//							Coords: [][]Coord{way.Coords},
//							Tags:   way.Tags,
//						})
//					}
//				} else {
//					if lineString != nil {
//						lineString(way.Class, LineString{
//							ID:     way.ID,
//							Type:   WayType,
//							Coords: [][]Coord{way.Coords},
//							Tags:   way.Tags,
//						})
//					}
//				}
//			}
//		}
//	}
//
//	// fill relations and return them
//	// TODO: sort super relations so that childs are filled before parents
//	waysMap := make(map[uint64]wayGeom, len(ways))
//	for _, way := range ways {
//		waysMap[way.ID] = way
//	}
//	ways = nil
//	relationsMap := make(map[uint64]relationGeom, len(relations))
//	for _, relation := range relations {
//		relationsMap[relation.ID] = relation
//	}
//	for _, relation := range relations {
//		if relationContainedIDs.Has(relation.ID) {
//			fmt.Println("REL", relation)
//			if _, ok := relationParents.Get(relation.ID); !ok {
//				// top-level relation for selection
//				// find all nested polygons and line strings, sort and merge line strings and return their coordinates
//				coords := relation.Process(relation.Relation, waysMap, relationsMap, bounds)
//				if relation.Tags.IsArea() {
//					coords = fixAreaCoords(bounds, coords, true)
//					relation.Polygons = append(relation.Polygons, Polygon{
//						ID:     relation.ID,
//						Type:   RelationType,
//						Coords: [][]Coord{coords},
//						Tags:   relation.Tags,
//					})
//				} else {
//					relation.LineStrings = append(relation.LineStrings, LineString{
//						ID:     relation.ID,
//						Type:   RelationType,
//						Coords: [][]Coord{coords},
//						Tags:   relation.Tags,
//					})
//				}
//
//				if point != nil && 0 < len(relation.Points) {
//					for _, o := range relation.Points {
//						point(relation.Class, o)
//					}
//				}
//				if lineString != nil && 0 < len(relation.LineStrings) {
//					for _, o := range relation.LineStrings {
//						lineString(relation.Class, o)
//					}
//				}
//				if polygon != nil && 0 < len(relation.Polygons) {
//					for _, o := range relation.Polygons {
//						polygon(relation.Class, o)
//					}
//				}
//			}
//		}
//	}
//	return nil
//}
