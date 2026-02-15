package osm

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

const MaxRelationDepth = 16

type Hist struct {
	Boundaries []int
	Buckets    []int

	min, max     int
	count        uint64
	samples, all []int
	rng          *rand.Rand
}

func NewHist(maxSamples int) *Hist {
	return &Hist{
		rng:     rand.New(rand.NewPCG(34234, 923832458)),
		samples: make([]int, 0, maxSamples),
	}
}

func (h *Hist) Add(v int) {
	if len(h.samples) == 0 {
		h.min, h.max = v, v
	} else if h.max < v {
		h.max = v
	} else if v < h.min {
		h.min = v
	}
	h.count++

	h.all = append(h.all, v)
	if len(h.samples) < cap(h.samples) {
		h.samples = append(h.samples, v)
	} else if i := int(h.rng.Uint64N(h.count)); i < len(h.samples) {
		h.samples[i] = v
	}
}

func (h *Hist) Quantile(phi float64) float64 {
	return h.Quantiles([]float64{phi})[0]
}

func (h *Hist) Quantiles(phis []float64) []float64 {
	slices.Sort(h.samples)
	qs := make([]float64, len(phis))
	for i, phi := range phis {
		var q float64
		if len(h.samples) == 0 || math.IsNaN(phi) {
			q = math.NaN()
		} else if phi <= 0.0 {
			q = float64(h.min)
		} else if 1.0 <= phi {
			q = float64(h.max)
		} else {
			idx := int(phi*float64(len(h.samples)-1) + 0.5)
			if idx == len(h.samples) {
				idx--
			}
			q = float64(h.samples[idx])
		}
		qs[i] = q
	}
	return qs
}

func (h *Hist) String() string {
	var mean, stddev float64
	for _, sample := range h.samples {
		mean += float64(sample)
	}
	mean /= float64(len(h.samples))
	for _, sample := range h.samples {
		stddev += (mean - float64(sample)) * (mean - float64(sample))
	}
	stddev = math.Sqrt(stddev / (float64(len(h.samples)) - 1.0))

	qs := h.Quantiles([]float64{0.5, 0.75, 0.9, 0.99})
	return fmt.Sprintf("mean=%v±%v  q(.5,.75,.9,.99)=%v", int(mean), int(stddev), qs)
}

type Stats struct {
	NumNodes, NumWays, NumRelations          uint64    // number of entities
	NodeIDRange, WayIDRange, RelationIDRange [2]uint64 // lowest and highest ID for entity
	Bounds                                   Bounds    // bounding box for node coordinates

	WayNodes              uint64 // nodes referenced by ways
	RelationNodes         uint64 // nodes referenced by relations
	DoublyReferencedNodes uint64 // existing nodes referenced by ways and relations

	RelationWays    uint64 // ways referenced by relations
	MissingWayNodes uint64 // referenced nodes by ways or relations that are missing
	HistWayNodes    *Hist

	RelationRelations        uint64 // relations referenced by other relations
	MissingRelations         uint64 // referenced relations by other relations that are missing
	MissingRelationNodes     uint64 // referenced nodes by relations that are missing
	MissingRelationWays      uint64 // referenced ways by relations that are missing
	MissingRelationRelations uint64 // referenced relations by relations that are missing
	HistRelationNodes        *Hist
	HistRelationWays         *Hist
	HistRelationRelations    *Hist
	NumRelationDepths        []uint64 // number of relations per nested level, first entry has no relation children (leafs), second entry has leaf relation children, third entry has relation children with each having leaf relation children, etc.
	NumRecursiveRelations    uint64
}

func (s Stats) String() string {
	if s.NumNodes == 0 && s.NumWays == 0 && s.NumRelations == 0 {
		return "empty"
	}

	rootNodes := s.NumNodes - (s.WayNodes - s.MissingWayNodes) - (s.RelationNodes - s.MissingRelationNodes - s.DoublyReferencedNodes)
	rootWays := s.NumWays - (s.RelationWays - s.MissingRelationWays)
	rootRelations := s.NumRelations - (s.RelationRelations - s.MissingRelationRelations)

	sb := &strings.Builder{}
	if 0 < s.NumNodes {
		fmt.Fprintf(sb, "Nodes:        num=%v  id=[%v,%v]\n", s.NumNodes, s.NodeIDRange[0], s.NodeIDRange[1])
		fmt.Fprintf(sb, "  parents:    relation=%v (%.1f%%)  way=%v (%.1f%%)  both=%v (%.1f%%)  none=%v (%.1f%%)\n\n", s.RelationNodes-s.MissingRelationNodes-s.DoublyReferencedNodes, float64(s.RelationNodes-s.MissingRelationNodes-s.DoublyReferencedNodes)/float64(s.NumNodes)*100.0, s.WayNodes-s.MissingWayNodes-s.DoublyReferencedNodes, float64(s.WayNodes-s.MissingWayNodes-s.DoublyReferencedNodes)/float64(s.NumNodes)*100.0, s.DoublyReferencedNodes, float64(s.DoublyReferencedNodes)/float64(s.NumNodes)*100.0, rootNodes, float64(rootNodes)/float64(s.NumNodes)*100.0)
	}
	if 0 < s.NumWays {
		fmt.Fprintf(sb, "Ways:         num=%v  id=[%v,%v]\n", s.NumWays, s.WayIDRange[0], s.WayIDRange[1])
		fmt.Fprintf(sb, "  parents:    relation=%v (%.1f%%)  none=%v (%.1f%%)\n", s.RelationWays-s.MissingRelationWays, float64(s.RelationWays-s.MissingRelationWays)/float64(s.NumWays)*100.0, rootWays, float64(rootWays)/float64(s.NumWays)*100.0)
		fmt.Fprintf(sb, "  nodes:      num=%v  %v  missing=%v\n\n", s.WayNodes, s.HistWayNodes, s.MissingWayNodes)
	}
	if 0 < s.NumRelations {
		fmt.Fprintf(sb, "Relations:    num=%v  id=[%v,%v]\n", s.NumRelations, s.RelationIDRange[0], s.RelationIDRange[1])
		fmt.Fprintf(sb, "  parents:    relation=%v (%.1f%%)  none=%v (%.1f%%)\n", s.RelationRelations-s.MissingRelationRelations, float64(s.RelationRelations-s.MissingRelationRelations)/float64(s.NumRelations)*100.0, rootRelations, float64(rootRelations)/float64(s.NumRelations)*100.0)
		fmt.Fprintf(sb, "  depths:   ")
		for depth, num := range s.NumRelationDepths {
			fmt.Fprintf(sb, "  %v=%v", depth, num)
		}
		if 0 < s.NumRecursiveRelations {
			fmt.Fprintf(sb, "  RECURSIVE=%v", s.NumRecursiveRelations)
		}
		fmt.Fprintf(sb, "\n\n")
		fmt.Fprintf(sb, "  nodes:      num=%v  %v  missing=%v\n", s.RelationNodes, s.HistRelationNodes, s.MissingRelationNodes)
		fmt.Fprintf(sb, "  ways:       num=%v  %v  missing=%v\n", s.RelationWays, s.HistRelationWays, s.MissingRelationWays)
		fmt.Fprintf(sb, "  relations:  num=%v  %v  missing=%v\n", s.RelationRelations, s.HistRelationRelations, s.MissingRelationRelations)
	}
	if 0 < s.NumNodes {
		fmt.Fprintf(sb, "Bounds:       lon=[%v,%v]  lat=[%v,%v]\n", s.Bounds[0].X, s.Bounds[1].X, s.Bounds[0].Y, s.Bounds[1].Y)
	}
	return sb.String()
}

type nodeStats struct {
	Exists, InWay, InRelation bool
}

func (z *Parser) Stats(ctx context.Context) (Stats, error) {
	stats := Stats{
		HistWayNodes:          NewHist(2048),
		HistRelationNodes:     NewHist(2048),
		HistRelationWays:      NewHist(2048),
		HistRelationRelations: NewHist(2048),
	}

	var mu1, mu2, mu3, mu4, mu5, mu6 sync.Mutex
	nodeIDs := NewUint64Set(8, 0.6)
	wayIDs := NewUint64Set(8, 0.6)
	relationIDs := NewUint64Set(8, 0.6)
	//refNodeIDs := NewUint64Set(8, 0.6)
	wayNodeIDs := NewUint64Set(8, 0.6)
	relationNodeIDs := NewUint64Set(8, 0.6)
	relationWayIDs := NewUint64Set(8, 0.6)
	relationRelationIDs := NewUint64Set(8, 0.6)
	relationParents := map[uint64][]uint64{}
	nodeFunc := func(node Node) {
		atomic.AddUint64(&stats.NumNodes, 1)

		mu1.Lock()
		nodeIDs.Add(node.ID)
		mu1.Unlock()

		mu2.Lock()
		if stats.NodeIDRange[0] == 0 {
			stats.NodeIDRange[0] = node.ID
			stats.NodeIDRange[1] = node.ID
			stats.Bounds[0] = Coord{node.Lon, node.Lat}
			stats.Bounds[1] = Coord{node.Lon, node.Lat}
		} else {
			if node.ID < stats.NodeIDRange[0] {
				stats.NodeIDRange[0] = node.ID
			} else if stats.NodeIDRange[1] < node.ID {
				stats.NodeIDRange[1] = node.ID
			}
			if node.Lon < stats.Bounds[0].X {
				stats.Bounds[0].X = node.Lon
			} else if stats.Bounds[1].X < node.Lon {
				stats.Bounds[1].X = node.Lon
			}
			if node.Lat < stats.Bounds[0].Y {
				stats.Bounds[0].Y = node.Lat
			} else if stats.Bounds[1].Y < node.Lat {
				stats.Bounds[1].Y = node.Lat
			}
		}
		mu2.Unlock()
	}
	wayFunc := func(way Way) {
		atomic.AddUint64(&stats.NumWays, 1)

		mu3.Lock()
		wayIDs.Add(way.ID)
		if stats.WayIDRange[0] == 0 {
			stats.WayIDRange[0] = way.ID
			stats.WayIDRange[1] = way.ID
		} else {
			if way.ID < stats.WayIDRange[0] {
				stats.WayIDRange[0] = way.ID
			} else if stats.WayIDRange[1] < way.ID {
				stats.WayIDRange[1] = way.ID
			}
		}
		stats.HistWayNodes.Add(len(way.Refs))
		mu3.Unlock()

		mu1.Lock()
		for _, id := range way.Refs {
			wayNodeIDs.Add(id)
		}
		mu1.Unlock()
	}
	relationFunc := func(relation Relation) {
		atomic.AddUint64(&stats.NumRelations, 1)

		mu4.Lock()
		relationIDs.Add(relation.ID)
		if stats.RelationIDRange[0] == 0 {
			stats.RelationIDRange[0] = relation.ID
			stats.RelationIDRange[1] = relation.ID
		} else {
			if relation.ID < stats.RelationIDRange[0] {
				stats.RelationIDRange[0] = relation.ID
			} else if stats.RelationIDRange[1] < relation.ID {
				stats.RelationIDRange[1] = relation.ID
			}
		}
		var nodeIDs, wayIDs, relationIDs []uint64
		for _, member := range relation.Members {
			if member.Type == NodeType {
				nodeIDs = append(nodeIDs, member.ID)
			} else if member.Type == WayType {
				wayIDs = append(wayIDs, member.ID)
			} else if member.Type == RelationType {
				relationIDs = append(relationIDs, member.ID)
				relationParents[member.ID] = append(relationParents[member.ID], relation.ID)
			}
		}
		stats.HistRelationNodes.Add(len(nodeIDs))
		stats.HistRelationWays.Add(len(wayIDs))
		stats.HistRelationRelations.Add(len(relationIDs))
		mu4.Unlock()

		if 0 < len(nodeIDs) {
			mu1.Lock()
			for _, id := range nodeIDs {
				relationNodeIDs.Add(id)
			}
			mu1.Unlock()
		}
		if 0 < len(wayIDs) {
			mu5.Lock()
			for _, id := range wayIDs {
				relationWayIDs.Add(id)
			}
			mu5.Unlock()
		}
		if 0 < len(relationIDs) {
			mu6.Lock()
			for _, id := range relationIDs {
				relationRelationIDs.Add(id)
			}
			mu6.Unlock()
		}
	}
	if err := z.Parse(ctx, nodeFunc, wayFunc, relationFunc); err != nil {
		return Stats{}, err
	}

	wayNodeIDs.Iterate(func(id uint64) {
		if !nodeIDs.Has(id) {
			stats.MissingWayNodes++
		}
	})
	relationNodeIDs.Iterate(func(id uint64) {
		if !nodeIDs.Has(id) {
			stats.MissingRelationNodes++
		} else if wayNodeIDs.Has(id) {
			stats.DoublyReferencedNodes++
		}
	})
	relationWayIDs.Iterate(func(id uint64) {
		if !wayIDs.Has(id) {
			stats.MissingRelationWays++
		}
	})
	relationRelationIDs.Iterate(func(id uint64) {
		if !relationIDs.Has(id) {
			stats.MissingRelationRelations++
		} else {
			depth := relationDepth(id, relationParents, 0)
			if depth == math.MaxInt {
				stats.NumRecursiveRelations++
			} else {
				for len(stats.NumRelationDepths) <= depth {
					stats.NumRelationDepths = append(stats.NumRelationDepths, 0)
				}
				stats.NumRelationDepths[depth]++
			}
		}
	})

	stats.WayNodes = uint64(wayNodeIDs.Size())
	stats.RelationNodes = uint64(relationNodeIDs.Size())
	stats.RelationWays = uint64(relationWayIDs.Size())
	stats.RelationRelations = uint64(relationRelationIDs.Size())
	stats.NumRelationDepths[0] = stats.NumRelations - (uint64(relationRelationIDs.Size()) - stats.MissingRelationRelations)
	return stats, nil
}

func relationDepth(id uint64, parents map[uint64][]uint64, Depth int) int {
	if MaxRelationDepth <= Depth {
		return math.MaxInt
	}
	depth := Depth
	for _, p := range parents[id] {
		if d := relationDepth(p, parents, Depth+1); depth < d {
			depth = d
		}
	}
	return depth
}
