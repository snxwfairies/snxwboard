package main

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/snxwfairies/snxwboard/internal/config"
)

// runOpen opens the board with the token in the URL fragment. A fragment never
// leaves the browser: it is not sent to the server, logged, or put in a
// Referer, and the page removes it from the address bar once read. It does sit
// briefly in the opener's process arguments, readable by other local users on
// a shared machine; there, paste the token from `snxw token` instead.
func runOpen() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	url := "http://" + cfg.Addr + "/#t=" + cfg.Token
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("open this in your browser:", url)
		return nil
	}
	fmt.Println("opened the board at http://" + cfg.Addr + "/")
	return nil
}
