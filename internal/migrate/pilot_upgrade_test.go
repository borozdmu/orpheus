//go:build integration

package migrate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/orpheus-agents/orpheus/internal/migrate"
	"github.com/orpheus-agents/orpheus/internal/testutil"
)

// Pilot v23 created credential tables. Upstream v23 added operation deadlines,
// and v24 backfilled old provisioning attempts. Exercise each historical schema
// with Goose's real journal, including the data which must survive migration.
func TestUpgradeFromDivergentProvisioningHistories(t *testing.T) {
	for _, source := range []string{"pilot23", "upstream23", "upstream24"} {
		t.Run(source, func(t *testing.T) {
			ctx := t.Context()
			pool := testutil.Database(t)
			db := stdlib.OpenDB(*pool.Config().ConnConfig)
			defer func() { _ = db.Close() }()
			current, err := migrate.Provider(db, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := current.DownTo(ctx, 22); err != nil {
				t.Fatal(err)
			}
			history := t.TempDir()
			files, err := filepath.Glob("../../migrations/*.sql")
			if err != nil {
				t.Fatal(err)
			}
			var inputs []string
			for _, path := range files {
				if filepath.Base(path) < "00023" {
					inputs = append(inputs, path)
				}
			}
			if source == "pilot23" {
				inputs = append(inputs, "testdata/pilot/00023_service_credentials.sql")
			} else {
				inputs = append(inputs, "../../migrations/00023_sandbox_provisioning.sql")
				if source == "upstream24" {
					inputs = append(inputs, "testdata/upstream/00024_sandbox_provisioning_data.sql")
				}
			}
			for _, file := range inputs {
				content, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(history, filepath.Base(file)), content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			old, err := migrate.Provider(db, history)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := old.Up(ctx); err != nil {
				t.Fatal(err)
			}
			sid, rid, oid, mid, invocation := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := pool.Exec(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO sessions(id, configuration, slot_reserved) VALUES ($1,'{}',false)", sid)
			exec("INSERT INTO runs(id,session_id,number) VALUES ($1,$2,1)", rid, sid)
			exec("INSERT INTO operations(id,session_id,run_id,kind,status) VALUES ($1,$2,$3,'sandbox_create','uncertain')", oid, sid, rid)
			if source == "pilot23" {
				exec("INSERT INTO messages(id,session_id,run_id,role,text,delivery_status,registered_sequence) VALUES ($1,$2,$3,'user','synthetic','pending',1)", mid, sid, rid)
				exec("INSERT INTO run_input_subjects VALUES ($1,'{\"id\":\"synthetic\"}')", mid)
				exec("INSERT INTO run_service_credentials VALUES ($1,$2,'{\"id\":1}','{\"read\":true}','synthetic-ciphertext')", rid, invocation)
			} else if source == "upstream24" {
				exec("UPDATE operations SET deadline_at=created_at+interval '777 seconds' WHERE id=$1", oid)
			}
			for range 2 {
				if _, err := current.Up(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var deadline int
			var reserved bool
			if err := pool.QueryRow(ctx, "SELECT EXTRACT(EPOCH FROM(o.deadline_at-o.created_at))::integer,s.slot_reserved FROM operations o JOIN sessions s ON s.id=o.session_id WHERE o.id=$1", oid).Scan(&deadline, &reserved); err != nil {
				t.Fatal(err)
			}
			if source == "upstream24" {
				if deadline != 777 || reserved {
					t.Fatalf("repeated already applied upstream backfill: deadline=%d reserved=%t", deadline, reserved)
				}
			} else if deadline != 120 || !reserved {
				t.Fatalf("missed historical backfill: deadline=%d reserved=%t", deadline, reserved)
			}
			if source == "pilot23" {
				var token, actor string
				err := pool.QueryRow(ctx, "SELECT c.ciphertext,i.subject->>'id' FROM run_service_credentials c JOIN messages m USING (run_id) JOIN run_input_subjects i ON i.message_id=m.id WHERE c.invocation_id=$1 AND c.scopes='{\"read\":true}' AND c.access_context='{\"id\":1}'", invocation).Scan(&token, &actor)
				if err != nil || actor != "synthetic" || token != "synthetic-ciphertext" {
					t.Fatalf("lost pilot grant or author: %v", err)
				}
			}
		})
	}
}
