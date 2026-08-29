package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const collectionTemplate = `# Quiver collection
defaults:
  base: "https://api.example.com"

# fail_on_error: true   # exit 1 on any non-2xx / non-OK response

auth: {}
#  main:
#    type: bearer
#    token: "{{token}}"
`

const envTemplate = `# Values here are plain text and ARE committed.
# Use {{env:NAME}} for anything secret — it resolves from the process
# environment at run time and is redacted from output and history.
base: "https://api.dev.example.com"
# token: "{{env:QV_DEV_TOKEN}}"
`

const gitignoreTemplate = ".qv/\n"

func newInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init <dir>",
		Short: "Create a new collection layout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]

			// Refuse to touch a directory that is already a collection: a stray
			// init must not clobber a curated collection.yaml.
			if !force {
				if _, err := os.Stat(filepath.Join(dir, "collection.yaml")); err == nil {
					return configErr(fmt.Errorf("%s already contains collection.yaml (use --force to overwrite)", dir))
				}
			}

			for _, d := range []string{
				filepath.Join(dir, "environments"),
				filepath.Join(dir, "requests"),
			} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			files := []struct{ name, content string }{
				{"collection.yaml", collectionTemplate},
				{".gitignore", gitignoreTemplate},
				{"environments/dev.yaml", envTemplate},
			}
			for _, f := range files {
				if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.content), 0o644); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "initialized %s\n", dir); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing collection")
	return cmd
}
