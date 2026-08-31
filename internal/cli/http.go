package cli

import (
	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func newHTTPCmd() *cobra.Command {
	var headers, query []string
	var body, bearer, user string

	cmd := &cobra.Command{
		Use:   "http <METHOD> <URL>",
		Short: "Send an ad-hoc HTTP request",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			hdr, err := splitPairs(headers, ":", "--header")
			if err != nil {
				return configErr(err)
			}
			qry, err := splitPairs(query, "=", "--query")
			if err != nil {
				return configErr(err)
			}
			auth, err := adhocAuth(bearer, user)
			if err != nil {
				return configErr(err)
			}

			method, url := args[0], args[1]
			if err := expandAll(rc, &method, &url, &body); err != nil {
				return classify(err)
			}
			// Headers and query params are arguments too. The single most valuable
			// ad-hoc use of a variable is a token in a header, and it was the one
			// case that silently did not expand.
			if err := expandMaps(rc, &hdr, &qry); err != nil {
				return classify(err)
			}

			spec := &request.HTTPSpec{Method: method, URL: url, Headers: hdr, Query: qry, Body: body}
			return executeAdHoc(cmd, rc, core.ResolvedRequest{
				Name: "ad-hoc", Protocol: request.ProtocolHTTP, HTTP: spec, Auth: auth,
			})
		},
	}
	cmd.Flags().StringArrayVarP(&headers, "header", "H", nil, "header \"Key: Value\" (repeatable)")
	cmd.Flags().StringArrayVarP(&query, "query", "q", nil, "query param key=value (repeatable)")
	cmd.Flags().StringVarP(&body, "data", "d", "", "request body")
	cmd.Flags().StringVar(&bearer, "bearer", "", "bearer token")
	cmd.Flags().StringVarP(&user, "user", "u", "", "basic auth as user:password")
	return cmd
}
