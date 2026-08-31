# Petstore example (network-dependent, documentation only)

Unlike `examples/local`, this collection targets a real third-party service —
the Swagger petstore at `https://petstore3.swagger.io/api/v3` — so it is **not**
part of any test or CI gate (Q35). Run it manually when you have network access:

```sh
qv run examples/petstore/requests/
```

It exists to document what a small, realistic collection looks like against a
public API: `01-find-pets.yaml` uses query parameters and asserts on response
shape, and `02-get-pet.yaml` interpolates a path-style variable that `-V
pet_id=…` can override.
