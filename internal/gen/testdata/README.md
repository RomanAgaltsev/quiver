# gen test fixtures

The hand-written `*.yaml` specs here each isolate one mapping rule, and their
expected output trees live under `golden/`.

`petstore-3.1.json` is different: it is the canonical **Swagger Petstore 3.1**
document, fetched verbatim from <https://petstore31.swagger.io/api/v31/openapi.json>
and licensed Apache 2.0 by SmartBear. It is not pinned to a golden tree — it is
there so the smoke test can prove the mapping survives contact with a real spec
(shared `$ref`s, mostly missing `operationId`s, a relative server URL) rather
than only with fixtures written to suit it.
