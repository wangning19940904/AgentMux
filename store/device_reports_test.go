package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func pairTestDevice(t *testing.T, st *Store) *core.Device {
	t.Helper()
	ctx := context.Background()
	code, err := st.CreateDevicePairingCode(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	device, _, err := st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo{Hostname: "box"})
	if err != nil || device == nil {
		t.Fatalf("pair: %+v, %v", device, err)
	}
	return device
}

func doc(id, data string) core.DeviceReportDoc {
	return core.DeviceReportDoc{ID: id, Data: json.RawMessage(data)}
}

func TestDeviceReportIsStoredAndRebuilt(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()
	device := pairTestDevice(t, st)
	other := pairTestDevice(t, st)
	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	const path = "/api/v1/agent-instances"

	if _, _, ok, err := st.DeviceReportedResponse(ctx, device.ID, path); ok || err != nil {
		t.Fatalf("a device that never reported has an answer: %v, %v", ok, err)
	}
	if err := st.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{
		Collection: "agents", Path: path, Shape: core.DeviceReportList, Order: []string{"b", "a"},
		Upserts: []core.DeviceReportDoc{doc("a", `{"id":"a"}`), doc("b", `{"id":"b"}`)},
	}, at); err != nil {
		t.Fatal(err)
	}
	payload, reportedAt, ok, err := st.DeviceReportedResponse(ctx, device.ID, path)
	if err != nil || !ok || string(payload) != `[{"id":"b"},{"id":"a"}]` || !reportedAt.Equal(at) {
		t.Fatalf("rebuilt = %s at %v ok=%v err=%v", payload, reportedAt, ok, err)
	}
	// The path is matched whole: another query is another answer.
	if _, _, ok, _ := st.DeviceReportedResponse(ctx, device.ID, path+"?x=1"); ok {
		t.Fatal("a different query was answered from the copy")
	}
	if _, _, ok, _ := st.DeviceReportedResponse(ctx, other.ID, path); ok {
		t.Fatal("one device's copy answered for another")
	}

	state, err := st.DeviceReportState(ctx, device.ID, "agents")
	if err != nil || len(state) != 2 || state["a"] != core.DeviceReportHash([]byte(`{"id":"a"}`)) {
		t.Fatalf("state = %v, %v", state, err)
	}

	// A change without an order keeps the order; a document outside it is
	// still part of the list.
	later := at.Add(time.Minute)
	if err := st.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{
		Collection: "agents", Path: path, Shape: core.DeviceReportList,
		Upserts: []core.DeviceReportDoc{doc("a", `{"id":"a","v":2}`), doc("c", `{"id":"c"}`)}, Deletes: []string{"b"},
	}, later); err != nil {
		t.Fatal(err)
	}
	payload, reportedAt, _, _ = st.DeviceReportedResponse(ctx, device.ID, path)
	if string(payload) != `[{"id":"a","v":2},{"id":"c"}]` || !reportedAt.Equal(later) {
		t.Fatalf("after a change = %s at %v", payload, reportedAt)
	}
	// An empty list is an answer, not a missing one.
	if err := st.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{
		Collection: "agents", Path: path, Shape: core.DeviceReportList, Order: []string{}, Deletes: []string{"a", "c"},
	}, later); err != nil {
		t.Fatal(err)
	}
	if payload, _, ok, _ := st.DeviceReportedResponse(ctx, device.ID, path); !ok || string(payload) != `[]` {
		t.Fatalf("emptied list = %s ok=%v", payload, ok)
	}

	if err := st.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{
		Collection: "status", Path: "/api/v1/status", Shape: core.DeviceReportValue,
		Upserts: []core.DeviceReportDoc{doc("_", `{"version":"1"}`)},
	}, later); err != nil {
		t.Fatal(err)
	}
	if payload, _, ok, _ := st.DeviceReportedResponse(ctx, device.ID, "/api/v1/status"); !ok || string(payload) != `{"version":"1"}` {
		t.Fatalf("value = %s ok=%v", payload, ok)
	}

	collections, err := st.ListDeviceCollections(ctx, "")
	if err != nil || len(collections) != 2 || collections[0].Collection != "agents" || collections[0].Items != 0 || collections[1].Items != 1 {
		t.Fatalf("collections = %+v, %v", collections, err)
	}

	// Unpairing a device removes the hub's copy of its data.
	if err := st.DeleteDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := st.DeviceReportedResponse(ctx, device.ID, "/api/v1/status"); ok {
		t.Fatal("an unpaired device's data is still answered")
	}
	if collections, _ := st.ListDeviceCollections(ctx, ""); len(collections) != 0 {
		t.Fatalf("collections after unpairing = %+v", collections)
	}
	// A report still in flight for it is refused rather than left behind.
	err = st.ApplyDeviceReport(ctx, device.ID, core.DeviceReport{Collection: "status", Path: "/api/v1/status", Shape: core.DeviceReportValue}, later)
	if err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("report for an unpaired device: %v", err)
	}
}

func TestDeviceReportIsBounded(t *testing.T) {
	st := newDeviceStore(t)
	ctx := context.Background()
	device := pairTestDevice(t, st)
	report := core.DeviceReport{Collection: "agents", Path: "/api/v1/agent-instances", Shape: core.DeviceReportList}
	for index := range maxDeviceCollectionItems + 1 {
		report.Upserts = append(report.Upserts, doc("id"+string(rune('a'+index%26))+strings.Repeat("x", index/26), `{}`))
	}
	if err := st.ApplyDeviceReport(ctx, device.ID, report, time.Now()); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an oversized collection was stored: %v", err)
	}
	// All or nothing: the refused report left no rows.
	if state, _ := st.DeviceReportState(ctx, device.ID, "agents"); len(state) != 0 {
		t.Fatalf("a refused report left %d documents", len(state))
	}
}
