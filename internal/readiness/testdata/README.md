非 Backoffice 的公開測資，用來證明 readiness 的規則不靠任何一個系統：

- `petstore.json`：Swagger Petstore。
- `keycloak-26.5.7-admin-rest.yaml`：Keycloak Admin REST API 26.5.7，原樣取自 https://www.keycloak.org/docs-api/26.5.7/rest-api/openapi.yaml 。用來守兩件事：`generate-example-*` 這類唯讀 GET 不能被當成會改資料，`first`+`max` 要被認成分頁。
