package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func newGRPCCmd() *cobra.Command {
	var metadata, protoFiles []string
	var data, bearer, user string
	var plaintext bool

	cmd := &cobra.Command{
		// Target first, matching grpcurl, evans and spec §6. The analogy with
		// `qv http <METHOD> <URL>` is misleading: a gRPC method name is not a verb,
		// it is the thing being addressed *on* the target.
		Use:   "grpc <TARGET> <pkg.Service/Method>",
		Short: "Send an ad-hoc gRPC unary request",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			md, err := splitPairs(metadata, ":", "--metadata")
			if err != nil {
				return configErr(err)
			}
			auth, err := adhocAuth(bearer, user)
			if err != nil {
				return configErr(err)
			}

			target, method := args[0], args[1]
			// Be forgiving about a plausible mistake rather than erroring on it:
			// only one of the two can contain a "/".
			if strings.Contains(target, "/") && !strings.Contains(method, "/") {
				target, method = method, target
			}
			if err := expandAll(rc, &target, &method, &data); err != nil {
				return classify(err)
			}
			if err := expandMaps(rc, &md); err != nil {
				return classify(err)
			}

			spec := &request.GRPCSpec{
				Target:     target,
				Method:     method,
				Metadata:   md,
				Message:    data,
				ProtoFiles: protoFiles,
				Plaintext:  plaintext,
			}
			return executeAdHoc(cmd, rc, core.ResolvedRequest{
				Name: "ad-hoc", Protocol: request.ProtocolGRPC, GRPC: spec, Auth: auth,
			})
		},
	}
	cmd.Flags().BoolVar(&plaintext, "plaintext", false, "use plaintext (no TLS)") // Q9
	cmd.Flags().StringVarP(&data, "data", "d", "", "request message as JSON")
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "H", nil, "metadata \"key: value\" (repeatable)")
	cmd.Flags().StringArrayVar(&protoFiles, "proto", nil, "local .proto file (repeatable; skips reflection)")
	// Parity with `qv http`: an auth profile is a first-class gRPC feature, so the
	// ad-hoc form must be able to express one too.
	cmd.Flags().StringVar(&bearer, "bearer", "", "bearer token")
	cmd.Flags().StringVarP(&user, "user", "u", "", "basic auth as user:password")
	return cmd
}
