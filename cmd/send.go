package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message (text/image/video/file/media/typing)",
	Long: `Send an ilink message to a user.

The bot can only push to users that have messaged it before: the context token from
their last inbound message is cached while 'ilinkgo serve' runs, and reused afterwards
from the state directory. Pass --context-token to override.

Voice (item type 3) is receive-only in the iLink protocol and has no send API.`,
}

func init() {
	sendCmd.PersistentFlags().String("to", "", "recipient ilink user ID (default: the user captured by 'ilinkgo serve')")
	sendCmd.PersistentFlags().String("context-token", "", "explicit context token, skipping the cached one")

	sendCmd.AddCommand(
		sendTextCmd(),
		sendMediaCmd("image", "Send an image", "image"),
		sendMediaCmd("video", "Send a video", "video"),
		sendMediaCmd("file", "Send a file attachment", "file"),
		sendMediaCmd("media", "Send a file, auto-detecting its type by extension", "media"),
		sendTypingCmd(),
	)
	rootCmd.AddCommand(sendCmd)
}

// parentFlag reads a flag defined on the send parent command.
func parentFlag(cmd *cobra.Command, name string) string {
	v, err := cmd.Flags().GetString(name)
	if err != nil {
		// Only reachable if the flag name changes without updating the callers.
		panic(fmt.Sprintf("ilinkgo: unknown flag %q", name))
	}
	return v
}

// requireFlags validates flags shared with the parent command. Cobra's
// MarkFlagRequired can't be used here: at init time the subcommand hasn't been added
// to its parent yet, so the parent's persistent flags aren't visible.
func requireFlags(cmd *cobra.Command, names ...string) error {
	var missing []string
	for _, name := range names {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed || f.Value.String() == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required flag(s) %q not set", strings.Join(missing, ", "))
	}
	return nil
}

func sendTextCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "text --to USER --text TEXT",
		Short: "Send a text message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "text"); err != nil {
				return err
			}
			text, _ := cmd.Flags().GetString("text")
			sd, err := newSender()
			if err != nil {
				return err
			}
			clientID, err := sd.SendText(cmd.Context(), parentFlag(cmd, "to"), text, parentFlag(cmd, "context-token"))
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), clientID)
			return nil
		},
	}
	c.Flags().String("text", "", "message text")
	return c
}

// sendMediaCmd builds the image/video/file/media commands, which share --file/--caption
// and differ only in how the uploaded media is sent.
func sendMediaCmd(use, short, kind string) *cobra.Command {
	c := &cobra.Command{
		Use:   use + " --to USER --file PATH",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "file"); err != nil {
				return err
			}
			file, _ := cmd.Flags().GetString("file")
			caption, _ := cmd.Flags().GetString("caption")
			to, token := parentFlag(cmd, "to"), parentFlag(cmd, "context-token")

			sd, err := newSender()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			switch kind {
			case "image":
				_, err = sd.SendImage(ctx, to, file, caption, token)
			case "video":
				_, err = sd.SendVideo(ctx, to, file, caption, token)
			case "file":
				_, err = sd.SendFile(ctx, to, file, caption, token)
			default:
				err = sd.SendMedia(ctx, to, file, caption, token)
			}
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sent")
			return nil
		},
	}
	c.Flags().String("file", "", "local path of the file to send")
	c.Flags().String("caption", "", "optional caption, sent as a separate text message")
	return c
}

func sendTypingCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "typing --to USER --ticket TICKET [--off]",
		Short: "Show or cancel the typing indicator",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "ticket"); err != nil {
				return err
			}
			ticket, _ := cmd.Flags().GetString("ticket")
			off, _ := cmd.Flags().GetBool("off")
			sd, err := newSender()
			if err != nil {
				return err
			}
			if err := sd.SendTyping(cmd.Context(), parentFlag(cmd, "to"), ticket, !off); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "sent")
			return nil
		},
	}
	c.Flags().String("ticket", "", "typing ticket for the recipient")
	c.Flags().Bool("off", false, "cancel the typing indicator instead of showing it")
	return c
}
