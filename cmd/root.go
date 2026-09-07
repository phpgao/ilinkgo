// Package cmd wires the ilinkgo subcommands (login/logout/serve/send) with cobra.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	ilink "github.com/openilink/openilink-sdk-go"
	"github.com/spf13/cobra"

	"github.com/phpgao/ilinkgo/internal/sender"
	"github.com/phpgao/ilinkgo/internal/store"
)

// dataDir is shared by every subcommand via the root's persistent --data-dir flag.
var dataDir string

// version is stamped into release binaries via
// -ldflags "-X github.com/phpgao/ilinkgo/cmd.version=vX.Y.Z".
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "ilinkgo",
	Short: "Weixin iLink bot CLI + local HTTP API",
	Long: `ilinkgo sends Weixin iLink bot messages from the command line or a local HTTP API.

Start with 'ilinkgo login', then either send directly:

  ilinkgo send text --to <user> --text "hello"

or run 'ilinkgo serve' to keep context tokens fresh and expose the HTTP API.

Environment variables:
  ILINKGO_DATA_DIR    state directory (overridden by --data-dir, default ~/.ilinkgo)
  ILINKGO_API_TOKEN   HTTP API bearer token (overridden by serve --token)`,
	SilenceUsage: true, // runtime errors shouldn't dump the whole usage text
	Version:      version,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dataDir, "data-dir", defaultDataDir(),
		"state directory (env: ILINKGO_DATA_DIR)")
}

// Execute runs the command tree and exits non-zero on failure.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func defaultDataDir() string {
	if d := os.Getenv("ILINKGO_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ilinkgo"
	}
	return filepath.Join(home, ".ilinkgo")
}

// openStore opens the state directory shared by all subcommands.
func openStore() (*store.Store, error) {
	return store.New(dataDir)
}

// loadClient loads the saved session and returns a ready-to-use ilink client.
func loadClient(st *store.Store) (*ilink.Client, error) {
	cred, err := st.LoadCredential()
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, fmt.Errorf("not logged in: run 'ilinkgo login' first")
	}
	opts := []ilink.Option{}
	if cred.BaseURL != "" {
		opts = append(opts, ilink.WithBaseURL(cred.BaseURL))
	}
	return ilink.NewClient(cred.Token, opts...), nil
}

// newSender builds the shared send path used by both the CLI and the HTTP API.
func newSender() (*sender.Sender, error) {
	st, err := openStore()
	if err != nil {
		return nil, err
	}
	client, err := loadClient(st)
	if err != nil {
		return nil, err
	}
	return sender.New(client, st), nil
}
