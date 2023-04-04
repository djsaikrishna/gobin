package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"net/url"
	"os"
)

func NewImportCmd(parent *cobra.Command) {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a document token from a share link",
		Example: `gobin import https://xgob.in/jis74978?token=sucrytsueirysuirysu

Will import the token for the server at https://xgob.in and the document with id jis74978`,
		Args: cobra.ExactArgs(1),
		//ValidArgsFunction: cobra.NoFileCompletions,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return viper.BindPFlag("server", cmd.Flags().Lookup("server"))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			token := args[0]
			server := viper.GetString("server")

			if tokenURL, err := url.Parse(args[0]); err == nil {
				token = tokenURL.Query().Get("token")
				server = tokenURL.Scheme + "://" + tokenURL.Host
			}

			if server == "" {
				return fmt.Errorf("no server specified")
			}
			if token == "" {
				return fmt.Errorf("no token specified")
			}

			var servers map[string][]string
			if err := viper.UnmarshalKey("servers", &servers); err != nil {
				return err
			}

			if servers == nil {
				servers = map[string][]string{}
			}

			tokens, _ := servers[server]
			for _, t := range tokens {
				if t == token {
					cmd.PrintErr("Token already imported")
					return nil
				}
			}
			servers[server] = append(tokens, token)

			viper.Set("servers", servers)

			if _, err := os.Stat(viper.ConfigFileUsed()); err != nil {
				if file, err := os.Create(viper.ConfigFileUsed()); err != nil {
					file.
					file.Close()
				}
			}

			if err := viper.WriteConfig(); err != nil {
				return err
			}

			cmd.PrintErr("Token imported")
			return nil
		},
	}

	parent.AddCommand(cmd)

	cmd.Flags().StringP("server", "s", "", "Gobin server address")
}
