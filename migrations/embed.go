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

// Up returns SQL that creates or verifies the schema.
func Up() string { return up + "\n" + messageRuntimeUp + "\n" + persistentRuntimeUp }

// Down returns destructive SQL intended only for tests and disaster recovery.
func Down() string { return persistentRuntimeDown + "\n" + messageRuntimeDown + "\n" + down }
