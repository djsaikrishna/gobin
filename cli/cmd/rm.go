package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/topi314/gobin/v3/internal/cfg"
	"github.com/topi314/gobin/v3/internal/ezhttp"
	"github.com/topi314/gobin/v3/server"
)

func NewRmCmd(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:     "rm",
		GroupID: "actions",
		Short:   "Removes a document from the gobin server",
		Example: `gobin rm jis74978

Will delete the jis74978 from the server.`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: documentCompletion,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := viper.BindPFlag("server", cmd.Flags().Lookup("server")); err != nil {
				return err
			}
			if err := viper.BindPFlag("version", cmd.Flags().Lookup("version")); err != nil {
				return err
			}
			return viper.BindPFlag("token", cmd.Flags().Lookup("token"))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("document id is required")
			}
			documentID := args[0]
			version := viper.GetString("version")
			token := viper.GetString("token")

			path := "/documents/" + documentID
			if version != "" {
				path += "/versions/" + version
			}

			if token == "" {
				token = viper.GetString("tokens_" + documentID)
			}
			if token == "" {
				return fmt.Errorf("no token found or provided for document: %s", documentID)
			}

			rs, err := ezhttp.Delete(path, token)
			if err != nil {
				return fmt.Errorf("failed to create document: %w", err)
			}
			defer func() {
				_ = rs.Body.Close()
			}()

			var deleteRs server.DeleteResponse
			if err = ezhttp.ProcessBody("delete document", rs, &deleteRs); err != nil {
				return fmt.Errorf("failed to process response: %w", err)
			}

			if version != "" {
				cmd.Printf("Removed version: %s from document: %s\n", version, documentID)
			} else {
				cmd.Printf("Removed document: %s\n", documentID)

			}
			if deleteRs.Versions > 0 {
				return nil
			}

			path, err = cfg.Update(func(m map[string]string) {
				delete(m, "TOKENS_"+documentID)
			})
			if err != nil {
				return fmt.Errorf("failed to update config: %w", err)
			}
			cmd.Printf("Removed document: %s from config: %s\n", documentID, path)
			return nil
		},
	}

	parent.AddCommand(cmd)

	cmd.Flags().StringP("server", "s", "", "Gobin server address")
	cmd.Flags().StringP("version", "v", "", "The version to update")
	cmd.Flags().StringP("token", "t", "", "The token for the document to update")
}
