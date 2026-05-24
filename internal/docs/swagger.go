package docs

import _ "embed"

//go:embed query_openapi.json
var queryOpenAPI []byte

func QueryOpenAPI() []byte {
	return queryOpenAPI
}
