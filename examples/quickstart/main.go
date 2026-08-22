package main

import (
	"context"
	"fmt"
	"os"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	serviceRuntime "github.com/liuzengh/trpc-agent-service/trpcservice/runtime"
)

func main() {
	path := "configs/example.yaml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	file, err := config.LoadFile(path)
	if err != nil {
		fatal(err)
	}
	snapshot, err := file.Snapshot("demo", "assistant")
	if err != nil {
		fatal(err)
	}
	bundle, err := serviceRuntime.NewBundle(snapshot)
	if err != nil {
		fatal(err)
	}
	defer bundle.Close()
	result, err := bundle.Run(context.Background(), serviceRuntime.RunInput{RequestID: "quickstart-1", UserID: "user/quickstart", SessionID: "dm/demo-http/quickstart", Text: "calculate 6*7"})
	if err != nil {
		fatal(err)
	}
	for _, item := range result.Events {
		if item.Response == nil {
			continue
		}
		for _, choice := range item.Choices {
			if choice.Message.Content != "" {
				fmt.Println(choice.Message.Content)
			}
		}
	}
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
