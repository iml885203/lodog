# testdata

Recorded API descriptions the spec reader is tested against. No network.

| File | Source |
| --- | --- |
| `petstore-swagger2.json` | Swagger 2.0 JSON, downloaded from https://petstore.swagger.io/v2/swagger.json |
| `petstore-openapi3.yaml` | OpenAPI 3.0 YAML, OAI/learn.openapis.org `examples/v3.0/petstore.yaml` |
| `petstore3-openapi3.json` | OpenAPI 3.0 JSON with a header parameter, downloaded from https://petstore3.swagger.io/api/v3/openapi.json |
| `petstore-expanded-openapi3.yaml` | OpenAPI 3.0 YAML, OAI/learn.openapis.org `examples/v3.0/petstore-expanded.yaml` |
| `swagger2-features.yaml` | Hand-written Swagger 2.0 YAML covering what petstore lacks: path-level and `#/parameters` references, `formData`, per-operation `consumes`, `x-nullable`, oauth2, unquoted response codes |
