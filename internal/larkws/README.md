# Cancellable Lark WebSocket transport

This small internal adaptation copies `ws/{client,const,error,model}.go` from
`github.com/larksuite/oapi-sdk-go/v3 v3.9.9`, under its MIT license (included).
The protobuf frame codec remains an alias to the upstream SDK. API models,
event dispatchers and outbound clients still use the upstream module.

The installed transport's `Start` blocked forever (`select {}`), reconnect and
heartbeat waits ignored cancellation, and `Close` mutated the reconnect flag
without synchronizing its readers. Dynamic event subscriptions need to replace
connections without retaining those loops. Local changes:

- `Start` waits for cancellation and closes its socket;
- dial and reconnect/heartbeat waits respect context;
- cancelled loops cannot restart or reconnect;
- reads and writes of the reconnect flag use its mutex;
- bootstrap requests have a finite timeout.

Handler maps are still built once per connection and are never mutated while
receiving events. `lifecycle_test.go` checks cancellation against a real local
WebSocket server. Prefer returning to the upstream transport when it provides
these lifecycle guarantees; keep the event relay boundary independent of it.
