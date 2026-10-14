// Command emberplus-cli browses the tree of an Ember+ provider in the
// terminal.
//
// Usage:
//
//	emberplus-cli [flags] host port
//	emberplus-cli [flags] host:port
//
// Navigate with the arrow keys or h, j, k, l. Values of visible parameters
// are updated live; stream parameters can be subscribed with s.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	emberplus "github.com/lukirs95/goemberplusclient"
)

// version is set at build time by the Makefile.
var version = "dev"

func main() {
	timeout := flag.Duration("timeout", 5*time.Second, "timeout for connecting and for requests")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] host port\n       %s [flags] host:port\n\nFlags:\n", os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("emberplus-cli", version)
		return
	}

	addr, err := address(flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	c, err := emberplus.Dial(ctx, addr, emberplus.WithRequestTimeout(*timeout))
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()

	a := &app{client: c, addr: addr, timeout: *timeout}
	p := tea.NewProgram(newModel(a), tea.WithAltScreen())
	a.send = p.Send
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// address accepts "host port" or "host:port".
func address(args []string) (string, error) {
	switch len(args) {
	case 1:
		if _, _, err := net.SplitHostPort(args[0]); err != nil {
			return "", fmt.Errorf("invalid address %q: %w", args[0], err)
		}
		return args[0], nil
	case 2:
		return net.JoinHostPort(args[0], args[1]), nil
	}
	return "", fmt.Errorf("expected host and port")
}
