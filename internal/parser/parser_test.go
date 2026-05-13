package parser

import (
	"testing"
)

func TestParseString(t *testing.T) {
	dsl := `model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin
    define can_read: member
    define can_edit: admin
`
	model, err := ParseString(dsl)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	types := model.GetTypeDefinitions()
	if len(types) != 2 {
		t.Fatalf("expected 2 type definitions, got %d", len(types))
	}

	if types[0].GetType() != "user" {
		t.Errorf("expected first type to be 'user', got %q", types[0].GetType())
	}

	org := types[1]
	if org.GetType() != "organization" {
		t.Errorf("expected second type to be 'organization', got %q", org.GetType())
	}

	relations := org.GetRelations()
	if len(relations) != 5 {
		t.Errorf("expected 5 relations, got %d", len(relations))
	}
}

func TestParseString_TupleToUserset(t *testing.T) {
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
	model, err := ParseString(dsl)
	if err != nil {
		t.Fatalf("ParseString: %v", err)
	}

	types := model.GetTypeDefinitions()
	if len(types) != 3 {
		t.Fatalf("expected 3 type definitions, got %d", len(types))
	}
}

func TestValidateFile(t *testing.T) {
	err := ValidateFile("../../schemas/v000/schema.fga")
	if err != nil {
		t.Fatalf("ValidateFile: %v", err)
	}
}
