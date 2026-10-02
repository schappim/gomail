// Command gomail is a command-line client for Gmail accounts using IMAP/SMTP
// with app passwords.
package main

import (
	"os"

	"gomail/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
