package main

import (
	"context"
	"fmt"
	"os"

	"github.com/liuzengh/trpc-agent-service/trpcservice/config"
	"github.com/liuzengh/trpc-agent-service/trpcservice/policy"
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
	bundle, err := serviceRuntime.NewTestBundle(snapshot)
	if err != nil {
		fatal(err)
	}
	defer bundle.Close()
	engine := &policy.Engine{Identity: policy.AuthenticatedIdentityAuthorizer{}}
	policyRequest := policy.Request{TenantID: snapshot.TenantID(), AppID: snapshot.AppID(), UserID: "user/quickstart", RequestID: "quickstart-1", Policy: snapshot.App().Tools}
	controls, err := engine.Evaluate(context.Background(), policyRequest)
	if err != nil {
		fatal(err)
	}
	ctx := policy.WithRequest(context.Background(), engine, policyRequest)
	result, err := bundle.Run(ctx, serviceRuntime.RunInput{RequestID: "quickstart-1", UserID: "user/quickstart", SessionID: "dm/demo-http/quickstart", Text: "calculate 6*7", ToolFilter: controls.Visibility, ToolExecutionFilter: controls.Execution, ToolPermissionPolicy: controls.Permission})
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
