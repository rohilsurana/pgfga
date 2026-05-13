package transform

import (
	"testing"

	"github.com/rohilsurana/pgfga/parser"
)

func TestGenerateAuthzModel(t *testing.T) {
	dsl := `model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member
`
	model, err := parser.ParseString(dsl)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	rows, err := GenerateAuthzModel(1, model)
	if err != nil {
		t.Fatalf("GenerateAuthzModel: %v", err)
	}

	if len(rows) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(rows))
	}

	byRelation := make(map[string]AuthzModelRow)
	for _, r := range rows {
		byRelation[r.Relation] = r
	}

	owner := byRelation["owner"]
	if owner.SubjectType == nil || *owner.SubjectType != "user" {
		t.Errorf("owner should have subject_type=user")
	}
	if owner.ImpliedBy != nil {
		t.Errorf("owner should not have implied_by")
	}

	admin := byRelation["admin"]
	if admin.SubjectType == nil || *admin.SubjectType != "user" {
		t.Errorf("admin should have subject_type=user")
	}
	if admin.ImpliedBy == nil || *admin.ImpliedBy != "owner" {
		t.Errorf("admin should have implied_by=owner")
	}

	member := byRelation["member"]
	if member.SubjectType == nil || *member.SubjectType != "user" {
		t.Errorf("member should have subject_type=user")
	}
	if member.ImpliedBy == nil || *member.ImpliedBy != "admin" {
		t.Errorf("member should have implied_by=admin")
	}

	canRead := byRelation["can_read"]
	if canRead.SubjectType != nil {
		t.Errorf("can_read should not have subject_type")
	}
	if canRead.ImpliedBy == nil || *canRead.ImpliedBy != "member" {
		t.Errorf("can_read should have implied_by=member")
	}
}

func TestGenerateAuthzModel_TupleToUserset(t *testing.T) {
	dsl := `model
  schema 1.1

type user

type organization
  relations
    define member: [user]

type repository
  relations
    define organization: [organization]
    define can_read: member from organization
`
	model, err := parser.ParseString(dsl)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	rows, err := GenerateAuthzModel(1, model)
	if err != nil {
		t.Fatalf("GenerateAuthzModel: %v", err)
	}

	byKey := make(map[string]AuthzModelRow)
	for _, r := range rows {
		byKey[r.EntityType+"."+r.Relation] = r
	}

	canRead := byKey["repository.can_read"]
	if canRead.ImpliedBy == nil || *canRead.ImpliedBy != "member" {
		t.Errorf("can_read should have implied_by=member")
	}
	if canRead.ParentRelation == nil || *canRead.ParentRelation != "organization" {
		t.Errorf("can_read should have parent_relation=organization")
	}
}
