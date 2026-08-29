package cli

import (
	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func newGRPCCmd() *cobra.Command {
	var metadata, protoFiles []string
	var data string
	var plaintext bool

	cmd := &cobra.Command{
		Use:   "grpc <METHOD> <TARGET>",
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

			method, target := args[0], args[1]
			if err := expandAll(rc, &method, &target, &data); err != nil {
				return configErr(err)
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
				Name: "ad-hoc", Protocol: request.ProtocolGRPC, GRPC: spec,
			})
		},
	}
	cmd.Flags().BoolVar(&plaintext, "plaintext", false, "use plaintext (no TLS)") // Q9
	cmd.Flags().StringVarP(&data, "data", "d", "", "request message as JSON")
	cmd.Flags().StringArrayVarP(&metadata, "metadata", "H", nil, "metadata \"key: value\" (repeatable)")
	cmd.Flags().StringArrayVar(&protoFiles, "proto", nil, "local .proto file (repeatable; skips reflection)")
	return cmd
}
