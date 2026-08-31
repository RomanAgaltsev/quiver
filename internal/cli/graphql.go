package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func newGraphQLCmd() *cobra.Command {
	var headers []string
	var query, variables, bearer, user string

	cmd := &cobra.Command{
		Use:   "graphql <URL>",
		Short: "Send an ad-hoc GraphQL query",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			// request.Validate rejects an empty graphql.query in a saved file, so the
			// ad-hoc path must too rather than POSTing {"query":""}.
			if query == "" {
				return configErr(fmt.Errorf("--query is required (graphql.query is required for saved requests too)"))
			}
			hdr, err := splitPairs(headers, ":", "--header")
			if err != nil {
				return configErr(err)
			}
			auth, err := adhocAuth(bearer, user)
			if err != nil {
				return configErr(err)
			}

			url := args[0]
			if err := expandAll(rc, &url, &query, &variables); err != nil {
				return classify(err)
			}
			if err := expandMaps(rc, &hdr); err != nil {
				return classify(err)
			}

			spec := &request.GraphQLSpec{URL: url, Headers: hdr, Query: query, Variables: variables}
			return executeAdHoc(cmd, rc, core.ResolvedRequest{
				Name: "ad-hoc", Protocol: request.ProtocolGraphQL, GraphQL: spec, Auth: auth,
			})
		},
	}
	cmd.Flags().StringVarP(&query, "query", "q", "", "GraphQL query")
	cmd.Flags().StringVar(&variables, "variables", "", "variables as JSON")
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "header \"Key: Value\" (repeatable)")
	cmd.Flags().StringVar(&bearer, "bearer", "", "bearer token")
	// Parity with `qv http`: --bearer alone was an arbitrary subset of the auth
	// profiles the saved form supports.
	cmd.Flags().StringVarP(&user, "user", "u", "", "basic auth as user:password")
	return cmd
}
