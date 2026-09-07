package cmd

import (
	"context"
	"fmt"

	"github.com/mdp/qrterminal"
	ilink "github.com/openilink/openilink-sdk-go"
	"github.com/spf13/cobra"

	"github.com/phpgao/ilinkgo/internal/store"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Scan a QR code to log the bot in",
	Long:  "Fetch a login QR code, wait for the Weixin scan, and save the session to the state directory.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}

		client := ilink.NewClient("")
		cmd.PrintErrln("Fetching QR code...")
		result, err := client.LoginWithQR(context.Background(), &ilink.LoginCallbacks{
			OnQRCode: func(imgContent string) {
				// qrcode_img_content is the login URL, not an image: render it as a QR
				// so it can be scanned straight from the terminal.
				cmd.PrintErrln("\nScan with WeChat:")
				if imgContent != "" {
					qrterminal.GenerateHalfBlock(imgContent, qrterminal.L, cmd.OutOrStdout())
				}
				cmd.PrintErrf("扫不出来时用浏览器打开: %s\n\n", imgContent)
			},
			OnScanned: func() {
				cmd.PrintErrln("Scanned, confirm on WeChat...")
			},
			OnExpired: func(attempt, max int) {
				cmd.PrintErrf("QR expired, refreshing (%d/%d)...\n", attempt, max)
			},
		})
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		if !result.Connected {
			return fmt.Errorf("login incomplete: %s", result.Message)
		}

		if err := st.SaveCredential(&store.Credential{
			Token:   client.Token(),
			BaseURL: client.BaseURL(),
			BotID:   result.BotID,
			UserID:  result.UserID,
		}); err != nil {
			return err
		}

		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Logged in. BotID=%s UserID=%s\nSession saved to %s\n",
			result.BotID, result.UserID, st.Dir())
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove the saved login session",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		if err := st.ClearCredential(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Logged out.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(loginCmd, logoutCmd)
}
