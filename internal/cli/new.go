package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const httpRequestTemplate = `# Run with: qv run <this file>
name: example
protocol: http

http:
  method: GET
  url: "{{base}}/path"
  # headers:
  #   Accept: application/json
  # query:
  #   page: "1"
  # body: |
  #   {"key": "value"}

# Captures extract response values into variables for later requests.
# captures:
#   - var: token
#     from: body
#     path: data.token

# assertions:
#   - name: status is 200
#     from: status
#     op: eq
#     value: "200"
`

const grpcRequestTemplate = `# Run with: qv run <this file>
name: example
protocol: grpc

grpc:
  target: "localhost:50051"
  method: "pkg.Service/Method"
  # message: |
  #   {"id": "1"}
  # metadata:
  #   authorization: "Bearer {{token}}"
  # proto_files:            # skip server reflection, resolve locally
  #   - protos/service.proto
  # plaintext: true

# captures:
#   - var: token
#     from: body
#     path: data.token

# assertions:
#   - name: status is OK
#     from: status
#     op: eq
#     value: "0"
`

const graphqlRequestTemplate = `# Run with: qv run <this file>
name: example
protocol: graphql

graphql:
  url: "{{base}}/graphql"
  query: |
    query Example {
      field
    }
  # variables: |
  #   {"id": "1"}
  # headers:
  #   Accept: application/json

# assertions:
#   - name: no errors
#     from: body
#     path: errors
#     op: not_exists
`

// templateFor returns the scaffold for `qv new`. It defaults to HTTP when no
// protocol flag is set. The multi-flag error is defence in depth: cobra's
// MarkFlagsMutuallyExclusive already rejects the combination.
func templateFor(asHTTP, asGRPC, asGraphQL bool) (string, error) {
	set := 0
	for _, b := range []bool{asHTTP, asGRPC, asGraphQL} {
		if b {
			set++
		}
	}
	if set > 1 {
		return "", fmt.Errorf("choose at most one of --http, --grpc, --graphql")
	}
	switch {
	case asGRPC:
		return grpcRequestTemplate, nil
	case asGraphQL:
		return graphqlRequestTemplate, nil
	default:
		return httpRequestTemplate, nil
	}
}

func newNewCmd() *cobra.Command {
	var asHTTP, asGRPC, asGraphQL, force bool

	cmd := &cobra.Command{
		Use:   "new <path>",
		Short: "Scaffold a new request file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Real flags bound to real variables. The previous revision wrote
			// `BoolVar(new(bool), "http", ...)`, so --http did nothing and only
			// the default made it appear to work.
			tmpl, err := templateFor(asHTTP, asGRPC, asGraphQL)
			if err != nil {
				return configErr(err)
			}
			path := args[0]
			if _, err := os.Stat(path); err == nil && !force {
				return configErr(fmt.Errorf("%s already exists (use --force to overwrite)", path))
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(tmpl), 0o644); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", path); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asHTTP, "http", false, "scaffold an HTTP request (default)")
	cmd.Flags().BoolVar(&asGRPC, "grpc", false, "scaffold a gRPC request")
	cmd.Flags().BoolVar(&asGraphQL, "graphql", false, "scaffold a GraphQL request")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.MarkFlagsMutuallyExclusive("http", "grpc", "graphql")
	return cmd
}
