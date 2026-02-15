package osm

import (
	"context"
	"sync"
)

func (z *Parser) FindSuperRelations(ctx context.Context) ([]Relation, error) {
	var mu sync.Mutex
	relations := []Relation{}
	relationFunc := func(relation Relation) {
		for _, member := range relation.Members {
			if member.Type == RelationType {
				relation.Own()
				mu.Lock()
				relations = append(relations, relation)
				mu.Unlock()
			}
		}
	}
	if err := z.Parse(ctx, nil, nil, relationFunc); err != nil {
		return nil, err
	}
	return relations, nil
}
