package transform

import (
	"fmt"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
)

type AuthzModelRow struct {
	SchemaVersion  int64
	EntityType     string
	Relation       string
	SubjectType    *string
	ImpliedBy      *string
	ParentRelation *string
}

func GenerateAuthzModel(schemaVersion int64, model *openfgav1.AuthorizationModel) ([]AuthzModelRow, error) {
	var rows []AuthzModelRow

	for _, typeDef := range model.GetTypeDefinitions() {
		relations := typeDef.GetRelations()
		metadata := typeDef.GetMetadata()

		if len(relations) == 0 {
			continue
		}

		for relName, relDef := range relations {
			var subjectType *string
			if metadata != nil {
				if relMeta, ok := metadata.GetRelations()[relName]; ok {
					types := relMeta.GetDirectlyRelatedUserTypes()
					if len(types) > 0 {
						st := types[0].GetType()
						subjectType = &st
					}
				}
			}

			row, err := processUserset(schemaVersion, typeDef.GetType(), relName, subjectType, relDef)
			if err != nil {
				return nil, fmt.Errorf("relation %s on %s: %w", relName, typeDef.GetType(), err)
			}
			rows = append(rows, row...)
		}
	}

	return rows, nil
}

func processUserset(schemaVersion int64, entityType, relName string, subjectType *string, userset *openfgav1.Userset) ([]AuthzModelRow, error) {
	if userset == nil {
		return nil, fmt.Errorf("nil userset")
	}

	switch u := userset.GetUserset().(type) {
	case *openfgav1.Userset_This:
		if subjectType == nil {
			return nil, fmt.Errorf("'this' relation without subject type")
		}
		return []AuthzModelRow{{
			SchemaVersion: schemaVersion,
			EntityType:    entityType,
			Relation:      relName,
			SubjectType:   subjectType,
		}}, nil

	case *openfgav1.Userset_ComputedUserset:
		impliedBy := u.ComputedUserset.GetRelation()
		return []AuthzModelRow{{
			SchemaVersion: schemaVersion,
			EntityType:    entityType,
			Relation:      relName,
			ImpliedBy:     &impliedBy,
		}}, nil

	case *openfgav1.Userset_TupleToUserset:
		impliedBy := u.TupleToUserset.GetComputedUserset().GetRelation()
		parentRel := u.TupleToUserset.GetTupleset().GetRelation()
		return []AuthzModelRow{{
			SchemaVersion:  schemaVersion,
			EntityType:     entityType,
			Relation:       relName,
			ImpliedBy:      &impliedBy,
			ParentRelation: &parentRel,
		}}, nil

	case *openfgav1.Userset_Union:
		children := u.Union.GetChild()
		if len(children) != 2 {
			return nil, fmt.Errorf("union with %d children (expected 2)", len(children))
		}

		_, isThis := children[0].GetUserset().(*openfgav1.Userset_This)
		computed, isComputed := children[1].GetUserset().(*openfgav1.Userset_ComputedUserset)

		if !isThis || !isComputed {
			return nil, fmt.Errorf("unsupported union pattern")
		}

		if subjectType == nil {
			return nil, fmt.Errorf("union relation without subject type")
		}

		impliedBy := computed.ComputedUserset.GetRelation()
		return []AuthzModelRow{{
			SchemaVersion: schemaVersion,
			EntityType:    entityType,
			Relation:      relName,
			SubjectType:   subjectType,
			ImpliedBy:     &impliedBy,
		}}, nil

	default:
		return nil, fmt.Errorf("unsupported userset type: %T", u)
	}
}
