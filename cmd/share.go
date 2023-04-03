package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/topisenpai/gobin/gobin"
	"github.com/topisenpai/gobin/internal/ezhttp"
)

func NewShareCmd(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Share a document with someone",
		Example: `gobin share -p=rds jis74978

Will return a link to the document with the id of jis74978 and the permission of write, delete, and share.`,
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			tokensMap := viper.GetStringMap("tokens.")
			tokens := make([]string, 0, len(tokensMap))
			for document := range tokensMap {
				tokens = append(tokens, document)
			}
			return tokens, cobra.ShellCompDirectiveNoFileComp
		},
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := viper.BindPFlag("server", cmd.Flags().Lookup("server")); err != nil {
				return err
			}
			return viper.BindPFlag("permissions", cmd.Flags().Lookup("permissions"))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("document id is required")
			}
			server := viper.GetString("server")
			documentID := args[0]
			var permissions []gobin.Permission
			for _, p := range viper.GetString("permissions") {
				switch p {
				case 'w':
					permissions = append(permissions, gobin.PermissionWrite)
				case 'd':
					permissions = append(permissions, gobin.PermissionDelete)
				case 's':
					permissions = append(permissions, gobin.PermissionShare)
				default:
					return fmt.Errorf("invalid permission %c", p)
				}
			}

			if len(permissions) == 0 {
				cmd.Printf("Share link: %s/%s", server, documentID)
				cmd.Printf("Share command: gobin get -s %s %s", server, documentID)
				return nil
			}

			buff := bytes.NewBuffer(nil)
			if err := json.NewEncoder(buff).Encode(gobin.ShareRequest{
				Permissions: permissions,
			}); err != nil {
				return fmt.Errorf("failed to marshal request body: %w", err)
			}

			rs, err := ezhttp.Post(fmt.Sprintf("%s/documents/%s/share", server, documentID), buff)
			if err != nil {
				return fmt.Errorf("failed to share document: %w", err)
			}
			defer rs.Body.Close()

			var response gobin.ShareResponse
			if err = json.NewDecoder(rs.Body).Decode(&response); err != nil {
				return fmt.Errorf("failed to decode response body: %w", err)
			}

			cmd.Printf("Share link: %s/%s?token=%s", server, documentID, response.Token)
			cmd.Printf("Share command: gobin add -s %s -t %s %s", server, response.Token)

			return nil
		},
	}

	parent.AddCommand(cmd)

	cmd.Flags().StringP("server", "s", "", "Gobin server address")
	cmd.Flags().StringP("permissions", "p", "", "The permissions to share the document with. (w)rite, (d)elete, (s)hare")
}
