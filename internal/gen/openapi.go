package gen

import (
	"fmt"
	"strings"

	"github.com/pb33f/libopenapi"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// LoadDocument parses an OpenAPI 3.x document and returns its high-level model
// with references resolved.
//
// A 2.0 document is rejected by name rather than half-parsed: libopenapi can
// read it, but the mapping differs enough that emitting a collection from one
// would produce quietly wrong output.
func LoadDocument(data []byte) (*v3high.Document, error) {
	doc, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if v := doc.GetVersion(); strings.HasPrefix(v, "2.") {
		return nil, fmt.Errorf(
			"OpenAPI %s (Swagger 2.0) is not supported; convert it to 3.x first", v)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	return &model.Model, nil
}
