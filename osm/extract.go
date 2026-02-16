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
