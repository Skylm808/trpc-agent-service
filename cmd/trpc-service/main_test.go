package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const signalHelperEnv = "TRPC_SERVICE_SIGNAL_TEST_HELPER"

func TestRunNonBlockingFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"--help"}, want: 0},
		{name: "version", args: []string{"--version"}, want: 0},
		{name: "unknown flag", args: []string{"--unknown"}, want: 2},
		{name: "unexpected argument", args: []string{"unexpected"}, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := run(test.args); got != test.want {
				t.Fatalf("run(%v) = %d, want %d", test.args, got, test.want)
			}
		})
	}
}

func TestGatewayTokenEnv(t *testing.T) {
	if got := gatewayTokenEnv("demo-http.v2"); got != "TRPC_AGENT_GATEWAY_TOKEN_DEMO_HTTP_V2" {
		t.Fatalf("gatewayTokenEnv=%q", got)
	}
}

func TestRunExitsCleanlyOnInterrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSignalHelperProcess")
	command.Env = append(os.Environ(), signalHelperEnv+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() error = %v", err)
	}
	if err := command.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read startup output: %v", err)
	}
	if !strings.HasPrefix(line, "trpc-agent-service ") {
		t.Fatalf("startup output = %q, want service version", line)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("Signal() error = %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("process exit error = %v", err)
	}
}

func TestSignalHelperProcess(t *testing.T) {
	if os.Getenv(signalHelperEnv) != "1" {
		return
	}
	os.Exit(run(nil))
}
