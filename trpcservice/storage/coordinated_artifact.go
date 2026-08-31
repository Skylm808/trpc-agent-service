package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-agent-go/artifact"
)

// CoordinatedArtifact serializes S3 revision allocation through the shared
// platform PostgreSQL database. The upstream S3 implementation intentionally
// cannot allocate concurrent revisions on its own.
type CoordinatedArtifact struct {
	DB       *sql.DB
	Delegate artifact.Service
}

func (service *CoordinatedArtifact) SaveArtifact(ctx context.Context, info artifact.SessionInfo, filename string, value *artifact.Artifact) (int, error) {
	if service == nil || service.DB == nil || service.Delegate == nil {
		return 0, errors.New("storage: coordinated artifact service is incomplete")
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(info.AppName + "\x00" + info.UserID + "\x00" + info.SessionID + "\x00" + filename))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%x", digest)); err != nil {
		return 0, err
	}
	revision, err := service.Delegate.SaveArtifact(ctx, info, filename, value)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return revision, nil
}

func (service *CoordinatedArtifact) LoadArtifact(ctx context.Context, info artifact.SessionInfo, filename string, version *int) (*artifact.Artifact, error) {
	return service.Delegate.LoadArtifact(ctx, info, filename, version)
}
func (service *CoordinatedArtifact) ListArtifactKeys(ctx context.Context, info artifact.SessionInfo) ([]string, error) {
	return service.Delegate.ListArtifactKeys(ctx, info)
}
func (service *CoordinatedArtifact) DeleteArtifact(ctx context.Context, info artifact.SessionInfo, filename string) error {
	if service == nil || service.DB == nil || service.Delegate == nil {
		return errors.New("storage: coordinated artifact service is incomplete")
	}
	tx, err := service.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(info.AppName + "\x00" + info.UserID + "\x00" + info.SessionID + "\x00" + filename))
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%x", digest)); err != nil {
		return err
	}
	if err := service.Delegate.DeleteArtifact(ctx, info, filename); err != nil {
		return err
	}
	return tx.Commit()
}
func (service *CoordinatedArtifact) ListVersions(ctx context.Context, info artifact.SessionInfo, filename string) ([]int, error) {
	return service.Delegate.ListVersions(ctx, info, filename)
}
func (service *CoordinatedArtifact) Close() error {
	if closer, ok := service.Delegate.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

var _ artifact.Service = (*CoordinatedArtifact)(nil)
