// Command ilinkgo is a CLI and local HTTP API for sending Weixin iLink bot messages
// (text, image, video, file, and auto-detected media), built on top of
// github.com/openilink/openilink-sdk-go.
package main

import "github.com/phpgao/ilinkgo/cmd"

func main() {
	cmd.Execute()
}
