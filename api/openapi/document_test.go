package openapi_test

import (
	"context"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jhzhfree/ai-governance-gateway/api/openapi"
	"testing"
)

func TestOpenAPIIsValid(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(openapi.Document)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
