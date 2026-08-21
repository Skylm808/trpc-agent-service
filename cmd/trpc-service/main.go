package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuzengh/trpc-agent-service/trpcservice"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("trpc-service", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *showVersion {
		fmt.Printf("trpc-agent-service %s\n", trpcservice.Version)
		return 0
	}

	app, err := trpcservice.NewApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize service: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	fmt.Printf("trpc-agent-service %s\n", trpcservice.Version)
	if err := app.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "service stopped with error: %v\n", err)
		return 1
	}
	return 0
}
