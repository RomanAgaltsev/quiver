// Package graphqlx implements the GraphQL executor on top of HTTP.
package graphqlx

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

type executor struct {
	http core.Executor
}

// New returns a GraphQL executor that delegates the actual transport to the
// given HTTP executor. Injecting it is what lets GraphQL inherit --insecure,
// --timeout and any custom transport.
func New(httpExec core.Executor) core.Executor {
	return &executor{http: httpExec}
}

func (e *executor) Execute(ctx context.Context, rr core.ResolvedRequest) (*core.Response, error) {
	spec := rr.GraphQL
	if spec == nil {
		return nil, fmt.Errorf("graphqlx: missing graphql spec")
	}

	payload := map[string]any{"query": spec.Query}
	if spec.Variables != "" {
		var vars any
		if err := json.Unmarshal([]byte(spec.Variables), &vars); err != nil {
			return nil, fmt.Errorf("graphqlx: variables must be JSON: %w", err)
		}
		payload["variables"] = vars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("graphqlx: marshal payload: %w", err)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range spec.Headers {
		headers[k] = v
	}

	httpReq := core.ResolvedRequest{
		Name:     rr.Name,
		Protocol: request.ProtocolGraphQL, // keep protocol tag for the normalized Response
		HTTP:     &request.HTTPSpec{Method: "POST", URL: spec.URL, Headers: headers, Body: string(body)},
		Auth:     rr.Auth,
		Timeout:  rr.Timeout,
		Insecure: rr.Insecure,
	}
	resp, err := e.http.Execute(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	resp.Protocol = request.ProtocolGraphQL

	// A GraphQL failure is an HTTP 200 carrying a non-empty `errors` array. The
	// HTTP layer therefore reports success; only this layer can tell (Q11).
	if n := gjson.GetBytes(resp.Body, "errors.#").Int(); n > 0 {
		resp.OK = false
		resp.StatusText = "graphql error"
	}
	return resp, nil
}
