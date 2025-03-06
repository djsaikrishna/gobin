package cmd

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/topi314/gobin/v3/internal/ezhttp"
	"github.com/topi314/gobin/v3/internal/ver"
)

func NewVersionCmd(parent *cobra.Command, version ver.Version) {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Prints the version of the gobin cli",
		Example: `gobin version

Go Version: go1.18.3
Version: dev
Commit: b1fd421
Build Time: Thu Jan  1 00:00:00 1970
OS/Arch: windows/amd64

Go Version: go1.19
Version: dev
Commit: b1fd421
Build Time: Thu Jan  1 00:00:00 1970
OS/Arch: windows/amd64`,
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			return viper.BindPFlag("server", cmd.Flags().Lookup("server"))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			gobinServer := viper.GetString("server")
			cmd.Println(version.Format())

			if gobinServer == "" {
				return nil
			}
			rs, err := ezhttp.Get("/version")
			if err != nil {
				return fmt.Errorf("failed to get server version: %w", err)
			}
			defer func() {
				_ = rs.Body.Close()
			}()

			if rs.StatusCode != http.StatusOK {
				return fmt.Errorf("failed to get server version: %s", rs.Status)
			}

			data, err := io.ReadAll(rs.Body)
			if err != nil {
				return fmt.Errorf("failed to read server version: %w", err)
			}
			cmd.Printf("Server: %s\n%s\n", gobinServer, data)
			return nil
		},
	}

	parent.AddCommand(cmd)

	cmd.Flags().StringP("server", "s", "", "Gobin server address")
}
