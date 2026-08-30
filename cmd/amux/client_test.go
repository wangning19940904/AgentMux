package main

import (
	"testing"
)

func TestClientCommandDoesNotExposeSQLiteRuntime(t *testing.T) {
	if flag := clientCmd().Flags().Lookup("sqlite-path"); flag != nil {
		t.Fatal("client command still exposes legacy --sqlite-path runtime")
	}
}
