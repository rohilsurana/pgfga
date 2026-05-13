package parser

import (
	"fmt"
	"os"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
)

func ParseFile(path string) (*openfgav1.AuthorizationModel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseString(string(data))
}

func ParseString(dsl string) (*openfgav1.AuthorizationModel, error) {
	model, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		return nil, fmt.Errorf("parse dsl: %w", err)
	}
	return model, nil
}

func ValidateFile(path string) error {
	_, err := ParseFile(path)
	return err
}
