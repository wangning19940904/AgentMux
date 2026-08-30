package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSkillStatesPersist(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SetSkillEnabled(ctx, "demo", false); err != nil {
		t.Fatal(err)
	}
	states, err := st.ListSkillStates(ctx)
	if err != nil || states["demo"] {
		t.Fatalf("states = %+v, err=%v", states, err)
	}
	if err := st.DeleteSkillState(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	states, err = st.ListSkillStates(ctx)
	if err != nil || len(states) != 0 {
		t.Fatalf("states after delete = %+v, err=%v", states, err)
	}
}
