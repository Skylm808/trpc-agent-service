// Package migrations embeds the PostgreSQL control-plane schema.
package migrations

import _ "embed"

//go:embed 000001_control_plane.up.sql
var up string

//go:embed 000001_control_plane.down.sql
var down string

//go:embed 000002_message_runtime.up.sql
var messageRuntimeUp string

//go:embed 000002_message_runtime.down.sql
var messageRuntimeDown string

//go:embed 000003_persistent_runtime.up.sql
var persistentRuntimeUp string

//go:embed 000003_persistent_runtime.down.sql
var persistentRuntimeDown string

//go:embed 000004_inbox_recovery.up.sql
var inboxRecoveryUp string

//go:embed 000004_inbox_recovery.down.sql
var inboxRecoveryDown string

// Up returns SQL that creates or verifies the schema.
func Up() string {
	return up + "\n" + messageRuntimeUp + "\n" + persistentRuntimeUp + "\n" + inboxRecoveryUp
}

// Down returns destructive SQL intended only for tests and disaster recovery.
func Down() string {
	return inboxRecoveryDown + "\n" + persistentRuntimeDown + "\n" + messageRuntimeDown + "\n" + down
}
