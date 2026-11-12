package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGatewayTokenCreationIsAtomicAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	var stores []*Store
	for range 2 {
		st, err := OpenLegacySQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		stores = append(stores, st)
	}
	checkConcurrentGatewayTokens(t, stores)
}

func checkConcurrentGatewayTokens(t *testing.T, stores []*Store) {
	t.Helper()
	const callers = 24
	tokens := make(chan string, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			token, err := stores[index%len(stores)].GetOrCreateGatewayToken(context.Background())
			if err != nil {
				errs <- err
				return
			}
			tokens <- token
		}()
	}
	close(start)
	group.Wait()
	close(tokens)
	close(errs)
	for err := range errs {
		t.Errorf("create gateway token: %v", err)
	}
	saved, ok, err := stores[0].GetSetting(context.Background(), gatewayTokenKey)
	if err != nil || !ok || !strings.HasPrefix(saved, "amux-") {
		t.Fatalf("saved gateway token is invalid: found=%v, err=%v", ok, err)
	}
	for token := range tokens {
		if token != saved {
			t.Fatal("concurrent caller received a token that differs from the stored token")
		}
	}
}

func TestGatewayTokenRepairsEmptySetting(t *testing.T) {
	st, err := OpenLegacySQLite(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SetSetting(ctx, gatewayTokenKey, ""); err != nil {
		t.Fatal(err)
	}
	token, err := st.GetOrCreateGatewayToken(ctx)
	if err != nil || !strings.HasPrefix(token, "amux-") {
		t.Fatalf("gateway token was not created: %v", err)
	}
	again, err := st.GetOrCreateGatewayToken(ctx)
	if err != nil || again != token {
		t.Fatalf("gateway token changed on the next read: %v", err)
	}
}
