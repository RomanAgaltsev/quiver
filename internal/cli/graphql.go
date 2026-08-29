package cli

import (
	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func newGraphQLCmd() *cobra.Command {
	var headers []string
	var query, variables, bearer string

	cmd := &cobra.Command{
		Use:   "graphql <URL>",
		Short: "Send an ad-hoc GraphQL query",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			hdr, err := splitPairs(headers, ":", "--header")
			if err != nil {
				return configErr(err)
			}

			url := args[0]
			if err := expandAll(rc, &url, &query, &variables); err != nil {
				return configErr(err)
			}

			var auth *request.AuthProfile
			if bearer != "" {
				auth = &request.AuthProfile{Type: "bearer", Token: bearer}
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
	return cmd
}
