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

func commandReservationMutationJSON(state string, replay bool) string {
	actual, completed := "null", "null"
	onHand, reserved := int64(5000), int64(1250)
	if state == "finalized" {
		actual, completed = "1200", `"`+commandTimestamp+`"`
		onHand, reserved = 3800, 0
	} else if state == "released" {
		completed = `"` + commandTimestamp + `"`
		reserved = 0
	}
	return fmt.Sprintf(`{"reservation":{"reservation_id":"%s","client_reservation_uuid":"%s","lot_id":"%s","roast_uuid":"%s","client_instance_uuid":"%s","state":"%s","planned_grams":1250,"actual_grams":%s,"reserved_at":"%s","completed_at":%s,"created_at":"%s","updated_at":"%s","open_conflict_id":null},"balance":{"lot_id":"%s","on_hand_grams":%d,"reserved_grams":%d,"available_grams":%d,"unresolved_conflict_count":0},"conflict":null,"idempotent_replay":%t}`,
		commandReservationID, commandEntryID, commandLotID, commandRoastID, commandClientID, state, actual, commandTimestamp, completed, commandTimestamp, commandTimestamp, commandLotID, onHand, reserved, onHand-reserved, replay)
}

func TestInventoryReservationCommandsSendEveryExactFieldWithoutPrompt(t *testing.T) {
	var paths, bodies, keys []string
	statuses := []int{http.StatusCreated, http.StatusOK, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(contents))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(statuses[len(paths)-1])
		_, _ = fmt.Fprint(w, commandReservationMutationJSON([]string{"reserved", "finalized", "released"}[len(paths)-1], len(paths) == 2))
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
	wantCreate := `{"ok":true,"data":` + commandReservationMutationJSON("reserved", false) + "}\n"
	if create.stdout != wantCreate {
		t.Fatalf("create JSON=%q want=%q", create.stdout, wantCreate)
	}
	if finalize.stdout != commandReservationHumanOutput("finalized", true) {
		t.Fatalf("finalize output=%q want=%q", finalize.stdout, commandReservationHumanOutput("finalized", true))
	}
	if release.stdout != commandReservationHumanOutput("released", false) {
		t.Fatalf("release output=%q want=%q", release.stdout, commandReservationHumanOutput("released", false))
	}
}

func TestInventoryReservationFinalizeAcceptsFlagsAfterUUID(t *testing.T) {
	var body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body, key = string(contents), r.Header.Get("Idempotency-Key")
		_, _ = fmt.Fprint(w, commandReservationMutationJSONForActualGrams(900))
	}))
	defer server.Close()

	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "--json", "inventory", "reservation", "finalize", commandEntryID, "--actual-grams", "900", "--occurred-at", commandTimestamp)
	if result.code != 0 || result.stderr != "" || key == "" {
		t.Fatalf("result=%#v key=%q", result, key)
	}
	if body != `{"actual_grams":900,"occurred_at":"`+commandTimestamp+`"}` {
		t.Fatalf("body=%q", body)
	}
	if result.stdout != `{"ok":true,"data":`+commandReservationMutationJSONForActualGrams(900)+"}\n" {
		t.Fatalf("stdout=%q", result.stdout)
	}
}

func commandReservationMutationJSONForActualGrams(actual int64) string {
	return fmt.Sprintf(`{"reservation":{"reservation_id":"%s","client_reservation_uuid":"%s","lot_id":"%s","roast_uuid":"%s","client_instance_uuid":"%s","state":"finalized","planned_grams":1250,"actual_grams":%d,"reserved_at":"%s","completed_at":"%s","created_at":"%s","updated_at":"%s","open_conflict_id":null},"balance":{"lot_id":"%s","on_hand_grams":%d,"reserved_grams":0,"available_grams":%d,"unresolved_conflict_count":0},"conflict":null,"idempotent_replay":false}`,
		commandReservationID, commandEntryID, commandLotID, commandRoastID, commandClientID, actual, commandTimestamp, commandTimestamp, commandTimestamp, commandTimestamp, commandLotID, 5000-actual, 5000-actual)
}

func commandReservationHumanOutput(state string, replay bool) string {
	actual, completed := "-", "-"
	onHand, reserved := "5000", "1250"
	if state == "finalized" {
		actual, completed, onHand, reserved = "1200", commandTimestamp, "3800", "0"
	} else if state == "released" {
		completed, reserved = commandTimestamp, "0"
	}
	available := onHand
	if state == "reserved" {
		available = "3750"
	}
	return "Reservation ID: " + commandReservationID + "\n" +
		"Client reservation UUID: " + commandEntryID + "\n" +
		"Lot ID: " + commandLotID + "\n" +
		"Roast UUID: " + commandRoastID + "\n" +
		"Client instance UUID: " + commandClientID + "\n" +
		"State: " + state + "\n" +
		"Planned grams: 1250\n" +
		"Actual grams: " + actual + "\n" +
		"Reserved at: " + commandTimestamp + "\n" +
		"Completed at: " + completed + "\n" +
		"Created at: " + commandTimestamp + "\n" +
		"Updated at: " + commandTimestamp + "\n" +
		"Open conflict ID: -\n" +
		"On hand grams: " + onHand + "\n" +
		"Reserved grams: " + reserved + "\n" +
		"Available grams: " + available + "\n" +
		"Unresolved conflicts: 0\n" +
		fmt.Sprintf("Idempotent replay: %t\n", replay)
}

func TestInventoryReservationFinalizeDoesNotInferActualGrams(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = fmt.Fprint(w, commandReservationMutationJSON("finalized", false))
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
		w.Header().Set("Content-Type", "application/json")
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

func TestInventoryReservationMutationJSONEnvelopesAreExact(t *testing.T) {
	tests := []struct {
		name, state string
		status      int
		args        []string
	}{
		{name: "create", state: "reserved", status: http.StatusCreated, args: []string{"inventory", "reservation", "create", "--client-reservation-uuid", commandEntryID, "--client-instance-uuid", commandClientID, "--roast-uuid", commandRoastID, "--lot-id", commandLotID, "--planned-grams", "1250", "--occurred-at", commandTimestamp}},
		{name: "finalize", state: "finalized", status: http.StatusOK, args: []string{"inventory", "reservation", "finalize", commandEntryID, "--actual-grams", "1200", "--occurred-at", commandTimestamp}},
		{name: "release", state: "released", status: http.StatusOK, args: []string{"inventory", "reservation", "release", commandEntryID, "--occurred-at", commandTimestamp}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, commandReservationMutationJSON(test.state, false))
			}))
			defer server.Close()
			args := append([]string{"--json"}, test.args...)
			result := runAuthCommand(t, inventoryRuntime(t, server.URL), args...)
			want := `{"ok":true,"data":` + commandReservationMutationJSON(test.state, false) + "}\n"
			if result.code != 0 || result.stderr != "" || result.stdout != want {
				t.Fatalf("result=%#v want=%q", result, want)
			}
		})
	}
}

func TestInventoryReservationCreateHumanOutputIsExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, commandReservationMutationJSON("reserved", false))
	}))
	defer server.Close()
	result := runAuthCommand(t, inventoryRuntime(t, server.URL), "inventory", "reservation", "create", "--client-reservation-uuid", commandEntryID, "--client-instance-uuid", commandClientID, "--roast-uuid", commandRoastID, "--lot-id", commandLotID, "--planned-grams", "1250", "--occurred-at", commandTimestamp)
	if result.code != 0 || result.stderr != "" || result.stdout != commandReservationHumanOutput("reserved", false) {
		t.Fatalf("result=%#v", result)
	}
}
