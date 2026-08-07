package command

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func commandReservationMutationJSON(replay bool) string {
	return fmt.Sprintf(`{"reservation":{"reservation_id":"%s","client_reservation_uuid":"%s","lot_id":"%s","roast_uuid":"%s","client_instance_uuid":"%s","state":"reserved","planned_grams":1250,"actual_grams":null,"reserved_at":"%s","completed_at":null,"created_at":"%s","updated_at":"%s","open_conflict_id":null},"balance":{"lot_id":"%s","on_hand_grams":5000,"reserved_grams":1250,"available_grams":3750,"unresolved_conflict_count":0},"conflict":null,"idempotent_replay":%t}`,
		commandReservationID, commandEntryID, commandLotID, commandRoastID, commandClientID, commandTimestamp, commandTimestamp, commandTimestamp, commandLotID, replay)
}

func TestInventoryReservationCommandsSendEveryExactFieldWithoutPrompt(t *testing.T) {
	var paths, bodies, keys []string
	statuses := []int{http.StatusCreated, http.StatusOK, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(contents))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(statuses[len(paths)-1])
		_, _ = fmt.Fprint(w, commandReservationMutationJSON(len(paths) == 2))
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("reservation command prompted"); return false }
	create := runAuthCommand(t, runtime, "--json", "inventory", "reservation", "create",
		"--client-reservation-uuid", "33333333-3333-4333-8333-333333333333",
		"--client-instance-uuid", "77777777-7777-4777-8777-777777777777",
		"--roast-uuid", "66666666-6666-4666-8666-666666666666",
		"--lot-id", "11111111-1111-4111-8111-111111111111",
		"--planned-grams", "1250", "--occurred-at", commandTimestamp, "--idempotency-key", "create-key")
	finalize := runAuthCommand(t, runtime, "inventory", "reservation", "finalize", "33333333-3333-4333-8333-333333333333",
		"--actual-grams", "1200", "--occurred-at", commandTimestamp, "--idempotency-key", "finalize-key")
	release := runAuthCommand(t, runtime, "inventory", "reservation", "release", commandEntryID,
		"--occurred-at", commandTimestamp, "--idempotency-key", "release-key")
	if create.code != 0 || finalize.code != 0 || release.code != 0 || create.stderr != "" || finalize.stderr != "" || release.stderr != "" {
		t.Fatalf("create=%#v finalize=%#v release=%#v", create, finalize, release)
	}
	wantPaths := []string{"/api/v1/inventory/reservations", "/api/v1/inventory/reservations/" + commandEntryID + "/finalize", "/api/v1/inventory/reservations/" + commandEntryID + "/release"}
	wantBodies := []string{
		`{"client_reservation_uuid":"33333333333343338333333333333333","client_instance_uuid":"77777777777747778777777777777777","roast_uuid":"66666666666646668666666666666666","lot_id":"11111111111141118111111111111111","planned_grams":1250,"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
		`{"actual_grams":1200,"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
		`{"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
	}
	if !reflect.DeepEqual(paths, wantPaths) || !reflect.DeepEqual(bodies, wantBodies) || !reflect.DeepEqual(keys, []string{"create-key", "finalize-key", "release-key"}) {
		t.Fatalf("paths=%#v bodies=%q keys=%#v", paths, bodies, keys)
	}
	for _, exact := range []string{`"idempotent_replay":false`, `"planned_grams":1250`, `"available_grams":3750`, `"conflict":null`} {
		if !strings.Contains(create.stdout, exact) {
			t.Fatalf("create JSON %q missing %q", create.stdout, exact)
		}
	}
	for _, exact := range []string{"Reservation ID", commandReservationID, "Idempotent replay", "true"} {
		if !strings.Contains(finalize.stdout, exact) {
			t.Fatalf("finalize output %q missing %q", finalize.stdout, exact)
		}
	}
}

func TestInventoryReservationFinalizeDoesNotInferActualGrams(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = fmt.Fprint(w, commandReservationMutationJSON(false))
	}))
	defer server.Close()
	runtime := inventoryRuntime(t, server.URL)
	runtime.IsTerminal = func(int) bool { t.Fatal("finalize prompted"); return false }
	result := runAuthCommand(t, runtime, "inventory", "reservation", "finalize", commandEntryID, "--occurred-at", commandTimestamp)
	if result.code != 0 || body != `{"actual_grams":null,"occurred_at":"2026-08-04T12:00:00.000000Z"}` {
		t.Fatalf("result=%#v body=%q", result, body)
	}
}

func TestInventoryReservationRejectsMissingInvalidAndNonintegerFieldsLocally(t *testing.T) {
	tests := [][]string{
		{"inventory", "reservation", "create", "--client-reservation-uuid", commandEntryID, "--client-instance-uuid", commandClientID, "--roast-uuid", commandRoastID, "--lot-id", commandLotID, "--planned-grams", "1"},
		{"inventory", "reservation", "create", "--client-reservation-uuid", "bad", "--client-instance-uuid", commandClientID, "--roast-uuid", commandRoastID, "--lot-id", commandLotID, "--planned-grams", "1", "--occurred-at", commandTimestamp},
		{"inventory", "reservation", "create", "--client-reservation-uuid", commandEntryID, "--client-instance-uuid", commandClientID, "--roast-uuid", commandRoastID, "--lot-id", commandLotID, "--planned-grams", "1.5", "--occurred-at", commandTimestamp},
		{"inventory", "reservation", "finalize", commandEntryID, "--actual-grams", "0", "--occurred-at", commandTimestamp},
		{"inventory", "reservation", "finalize", commandEntryID, "--actual-grams", "1.5", "--occurred-at", commandTimestamp},
		{"inventory", "reservation", "release", commandEntryID, "--occurred-at", "2026-08-04T12:00:00Z"},
		{"inventory", "reservation", "release", commandEntryID, "--occurred-at", commandTimestamp, "--idempotency-key", ""},
	}
	for _, args := range tests {
		result := runAuthCommand(t, Runtime{ConfigDir: t.TempDir()}, args...)
		if result.code != 2 {
			t.Errorf("args=%q result=%#v", args, result)
		}
	}
}

func TestInventoryReservationPreservesServerConflictResponseWithoutAdjustment(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_inventory_transition","message":"Reservation cannot transition","details":null}}`)
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "reservation", "release", commandEntryID, "--occurred-at", commandTimestamp)
	if result.code != 7 || !strings.Contains(result.stdout, `"code":"invalid_inventory_transition"`) || !strings.Contains(result.stdout, `"message":"Reservation cannot transition"`) {
		t.Fatalf("result=%#v", result)
	}
	if !reflect.DeepEqual(paths, []string{"/api/v1/inventory/reservations/" + commandEntryID + "/release"}) {
		t.Fatalf("paths=%#v", paths)
	}
}
