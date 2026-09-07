package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	ilink "github.com/openilink/openilink-sdk-go"
	"github.com/spf13/cobra"

	"github.com/phpgao/ilinkgo/internal/apiserver"
	"github.com/phpgao/ilinkgo/internal/sender"
	"github.com/phpgao/ilinkgo/internal/store"
)

var (
	serveListen string
	serveToken  string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the message monitor and the local HTTP API",
	Long: `Long-poll for inbound messages (caching each user's context token so the bot can
push to them later) and serve the local HTTP API for sending messages.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		// One client instance for the whole process: an empty token means "not bound yet",
		// and /api/login fills it in later without restarting (container-friendly).
		client := ilink.NewClient("")
		if cred, err := st.LoadCredential(); err == nil && cred != nil {
			client.SetToken(cred.Token)
			if cred.BaseURL != "" {
				client.SetBaseURL(cred.BaseURL)
			}
		} else {
			cmd.PrintErrln("not logged in: POST /api/login to get the WeChat bind URL")
		}

		// Reuse the previously generated token; generate+persist one on first run so
		// the API is never accidentally left unauthenticated.
		apiToken := serveToken
		if apiToken == "" {
			if apiToken, _ = st.LoadAPIToken(); apiToken == "" {
				apiToken = randomToken()
				if err := st.SaveAPIToken(apiToken); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Generated API token: %s\n", apiToken)
			}
		}

		logger := log.New(cmd.ErrOrStderr(), "", log.LstdFlags)
		sd := sender.New(client, st)

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		apiCtx, cancelAPI := context.WithCancel(ctx)
		defer cancelAPI()

		// Wakes the monitor loop as soon as /api/login completes a bind, so no restart
		// is needed after scanning.
		loggedIn := make(chan struct{}, 1)

		go monitorLoop(apiCtx, client, st, loggedIn, logger)

		err = apiserver.Serve(apiCtx, apiserver.Config{
			Listen: serveListen,
			Token:  apiToken,
			OnLoggedIn: func() {
				select {
				case loggedIn <- struct{}{}:
				default:
				}
			},
		}, sd, logger)
		cancelAPI()
		if err != nil && ctx.Err() == nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Stopped.")
		return nil
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveListen, "listen", "127.0.0.1:9100", "HTTP API listen address")
	serveCmd.Flags().StringVar(&serveToken, "token", os.Getenv("ILINKGO_API_TOKEN"),
		"HTTP API bearer token (default: reuse or generate one under the state dir)")
	rootCmd.AddCommand(serveCmd)
}

// monitorLoop keeps the long poll running: it waits for a login session, then monitors
// until the context ends or the session expires, and waits again afterwards.
// It caches each user's context token (also persisted to disk) and tracks the default
// recipient, so sends can omit --to.
func monitorLoop(ctx context.Context, client *ilink.Client, st *store.Store, loggedIn <-chan struct{}, logger *log.Logger) {
	const retryDelay = 5 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		if client.Token() == "" {
			select {
			case <-ctx.Done():
				return
			case <-loggedIn:
			case <-time.After(retryDelay):
			}
			continue
		}

		err := client.Monitor(ctx, func(msg ilink.WeixinMessage) {
			if msg.ContextToken != "" {
				if err := st.SaveContextToken(msg.FromUserID, msg.ContextToken); err != nil {
					logger.Printf("[store] save context token: %v", err)
				}
			}
			// Default recipient follows the latest user that messaged the bot, so --to
			// can be omitted and still reach whoever is currently talking to it.
			if msg.FromUserID != "" && msg.FromUserID != st.LoadDefaultUser() {
				if err := st.SaveDefaultUser(msg.FromUserID); err != nil {
					logger.Printf("[store] save default user: %v", err)
				} else {
					logger.Printf("[store] default recipient: %s", msg.FromUserID)
				}
			}
			logger.Printf("[in] %s: %s", msg.FromUserID, ilink.ExtractText(&msg))
		}, &ilink.MonitorOptions{
			InitialBuf:  st.LoadSyncBuf(),
			OnBufUpdate: func(buf string) { _ = st.SaveSyncBuf(buf) },
			OnError:     func(err error) { logger.Printf("[monitor] %v", err) },
			OnSessionExpired: func() {
				logger.Printf("[monitor] session expired: POST /api/login (or run 'ilinkgo login') to rebind")
			},
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Printf("[monitor] %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
		}
	}
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
