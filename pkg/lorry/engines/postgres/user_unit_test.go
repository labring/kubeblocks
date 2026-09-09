/*
Copyright (C) 2022-2023 ApeCloud Co., Ltd

This file is part of KubeBlocks project.
*/

package postgres

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgreSQLUserQuoting(t *testing.T) {
	if got, want := quoteIdentifier(`user"name`), `"user""name"`; got != want {
		t.Fatalf("quoteIdentifier() = %q, want %q", got, want)
	}
	if got, want := quoteLiteral(`pa'ssword`), `'pa''ssword'`; got != want {
		t.Fatalf("quoteLiteral() = %q, want %q", got, want)
	}
}

func TestIsDuplicateRoleError(t *testing.T) {
	if !isDuplicateRoleError(&pgconn.PgError{Code: "42710"}) {
		t.Fatal("duplicate role error was not detected")
	}
	if isDuplicateRoleError(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("non-role conflict was detected as duplicate role")
	}
}
