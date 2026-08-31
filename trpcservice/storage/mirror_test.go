package storage

import (
	"context"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	"trpc.group/trpc-go/trpc-agent-go/artifact"
	artifactmemory "trpc.group/trpc-go/trpc-agent-go/artifact/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	memorymemory "trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/session"
	sessionmemory "trpc.group/trpc-go/trpc-agent-go/session/inmemory"
)

func TestMirroredServicesWriteBothAndReadPrimary(t *testing.T) {
	ctx := context.Background()
	primarySession, targetSession := sessionmemory.NewSessionService(), sessionmemory.NewSessionService()
	sessions := &MirroredSession{Primary: primarySession, Target: targetSession}
	key := session.Key{AppName: "tenant/a/app/b", UserID: "user", SessionID: "session"}
	if _, err := sessions.CreateSession(ctx, key, session.StateMap{"value": []byte("primary")}); err != nil {
		t.Fatal(err)
	}
	if found, err := targetSession.GetSession(ctx, key); err != nil || found == nil {
		t.Fatalf("target session=%v err=%v", found, err)
	}

	primaryMemory, targetMemory := memorymemory.NewMemoryService(), memorymemory.NewMemoryService()
	memories := &MirroredMemory{Primary: primaryMemory, Target: targetMemory}
	user := memory.UserKey{AppName: key.AppName, UserID: key.UserID}
	if err := memories.AddMemory(ctx, user, "remember me", nil); err != nil {
		t.Fatal(err)
	}
	if found, err := targetMemory.ReadMemories(ctx, user, 10); err != nil || len(found) != 1 {
		t.Fatalf("target memories=%d err=%v", len(found), err)
	}

	primaryArtifact, targetArtifact := artifactmemory.NewService(), artifactmemory.NewService()
	artifacts := &MirroredArtifact{Primary: primaryArtifact, Target: targetArtifact}
	info := artifact.SessionInfo{AppName: key.AppName, UserID: key.UserID, SessionID: key.SessionID}
	if revision, err := artifacts.SaveArtifact(ctx, info, "answer.txt", &artifact.Artifact{MimeType: "text/plain", Data: []byte("42")}); err != nil || revision != 0 {
		t.Fatalf("revision=%d err=%v", revision, err)
	}
	if found, err := targetArtifact.LoadArtifact(ctx, info, "answer.txt", nil); err != nil || string(found.Data) != "42" {
		t.Fatalf("target artifact=%v err=%v", found, err)
	}
}

func TestValidateRoutedProfileRequiresMatchingSessionSummaryTargets(t *testing.T) {
	postgres := tenant.BackendConfig{Type: tenant.BackendPostgres}
	profile := tenant.StorageProfile{Session: postgres, Memory: postgres, Summary: postgres, Artifact: postgres, Knowledge: postgres, Audit: postgres}
	if err := ValidateRoutedProfile(profile); err != nil {
		t.Fatal(err)
	}
	target := tenant.BackendConfig{Type: tenant.BackendPostgres, Credential: tenant.SecretRef{Provider: tenant.SecretProviderEnv, Key: "TARGET_DSN"}}
	profile.Session.MigrationTarget = &target
	if err := ValidateRoutedProfile(profile); err == nil {
		t.Fatal("mismatched summary migration target must fail")
	}
	other := target.Clone()
	profile.Summary.MigrationTarget = &other
	if err := ValidateRoutedProfile(profile); err != nil {
		t.Fatal(err)
	}
}
