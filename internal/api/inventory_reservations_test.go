package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const reservationClientInstanceID = "77777777777747778777777777777777"
const reservationRoastID = "66666666666646668666666666666666"

func reservationMutationJSON(replay bool) string {
	return fmt.Sprintf(`{"reservation":{"reservation_id":"%s","client_reservation_uuid":"%s","lot_id":"%s","roast_uuid":"%s","client_instance_uuid":"%s","state":"reserved","planned_grams":1250,"actual_grams":null,"reserved_at":"%s","completed_at":null,"created_at":"%s","updated_at":"%s","open_conflict_id":"%s"},"balance":{"lot_id":"%s","on_hand_grams":5000,"reserved_grams":1250,"available_grams":3750,"unresolved_conflict_count":1},"conflict":{"conflict_id":"%s","lot_id":"%s","source_ledger_entry_id":"%s","roast_uuid":"%s","reservation_id":"%s","trigger_operation":"reservation","available_grams_snapshot":-25,"state":"open","resolution_note":null,"resolved_by_user_id":null,"resolved_at":null,"created_at":"%s"},"idempotent_replay":%t}`,
		inventoryReservationID, inventoryEntryID, inventoryLotID, reservationRoastID, reservationClientInstanceID, inventoryTimestamp, inventoryTimestamp, inventoryTimestamp, inventoryConflictID,
		inventoryLotID, inventoryConflictID, inventoryLotID, inventoryEntryID, reservationRoastID, inventoryReservationID, inventoryTimestamp, replay)
}

func TestReservationMethodsUseMemberCompatibleRoutesExactBodiesStatusesAndOneKey(t *testing.T) {
	var methods, paths, keys, bodies []string
	statuses := []int{http.StatusCreated, http.StatusOK, http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, _ := io.ReadAll(r.Body)
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		bodies = append(bodies, string(contents))
		w.WriteHeader(statuses[len(paths)-1])
		_, _ = io.WriteString(w, reservationMutationJSON(false))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "member-token", time.Second)
	create := ReservationCreate{
		ClientReservationUUID: "33333333-3333-4333-8333-333333333333",
		ClientInstanceUUID:    "77777777-7777-4777-8777-777777777777",
		RoastUUID:             "66666666-6666-4666-8666-666666666666",
		LotID:                 "11111111-1111-4111-8111-111111111111",
		PlannedGrams:          1250,
		OccurredAt:            inventoryTimestamp,
	}
	if _, failure := client.CreateInventoryReservation(context.Background(), create, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	actual := int64(1200)
	if _, failure := client.FinalizeInventoryReservation(context.Background(), "33333333-3333-4333-8333-333333333333", ReservationFinalize{ActualGrams: &actual, OccurredAt: inventoryTimestamp}, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	if _, failure := client.ReleaseInventoryReservation(context.Background(), inventoryEntryID, ReservationRelease{OccurredAt: inventoryTimestamp}, "same-key"); failure != nil {
		t.Fatal(failure)
	}
	if !reflect.DeepEqual(methods, []string{"POST", "POST", "POST"}) {
		t.Fatalf("methods = %#v", methods)
	}
	wantPaths := []string{
		"/api/v1/inventory/reservations",
		"/api/v1/inventory/reservations/" + inventoryEntryID + "/finalize",
		"/api/v1/inventory/reservations/" + inventoryEntryID + "/release",
	}
	if !reflect.DeepEqual(paths, wantPaths) || !reflect.DeepEqual(keys, []string{"same-key", "same-key", "same-key"}) {
		t.Fatalf("paths=%#v keys=%#v", paths, keys)
	}
	wantBodies := []string{
		`{"client_reservation_uuid":"33333333333343338333333333333333","client_instance_uuid":"77777777777747778777777777777777","roast_uuid":"66666666666646668666666666666666","lot_id":"11111111111141118111111111111111","planned_grams":1250,"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
		`{"actual_grams":1200,"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
		`{"occurred_at":"2026-08-04T12:00:00.000000Z"}`,
	}
	if !reflect.DeepEqual(bodies, wantBodies) {
		t.Fatalf("bodies=%q want=%q", bodies, wantBodies)
	}
}

func TestReservationFinalizePreservesOmittedActualWeightAsNull(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = io.WriteString(w, reservationMutationJSON(false))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "token", time.Second)
	if _, failure := client.FinalizeInventoryReservation(context.Background(), inventoryEntryID, ReservationFinalize{OccurredAt: inventoryTimestamp}, "key"); failure != nil {
		t.Fatal(failure)
	}
	if body != `{"actual_grams":null,"occurred_at":"2026-08-04T12:00:00.000000Z"}` {
		t.Fatalf("body = %q", body)
	}
}

func TestReservationMutationProjectionRequiresAndValidatesEveryConsumedField(t *testing.T) {
	var response ReservationMutationResponse
	if err := decodeOneJSON([]byte(reservationMutationJSON(true)), &response); err != nil {
		t.Fatal(err)
	}
	if !response.IdempotentReplay || response.Reservation.PlannedGrams != 1250 || response.Balance.AvailableGrams != 3750 || response.Conflict == nil || response.Conflict.ConflictID != inventoryConflictID {
		t.Fatalf("response = %#v", response)
	}
	for _, replacement := range []struct{ old, new string }{
		{`"balance":{`, `"other":{`},
		{`"idempotent_replay":true`, `"other_replay":true`},
		{`"available_grams":3750`, `"available_grams":3750.5`},
		{`"planned_grams":1250`, `"planned_grams":0`},
		{inventoryReservationID, "not-a-uuid"},
		{inventoryTimestamp, "2026-08-04T12:00:00Z"},
	} {
		mutated := strings.Replace(reservationMutationJSON(true), replacement.old, replacement.new, 1)
		if err := decodeOneJSON([]byte(mutated), &ReservationMutationResponse{}); err == nil {
			t.Errorf("accepted mutation %q => %q", replacement.old, replacement.new)
		}
	}
}

func TestReservationRequestsRejectStrictLocalFieldsBeforeNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client, _ := NewClient(server.URL, "token", time.Second)
	validCreate := ReservationCreate{ClientReservationUUID: inventoryEntryID, ClientInstanceUUID: reservationClientInstanceID, RoastUUID: reservationRoastID, LotID: inventoryLotID, PlannedGrams: 1, OccurredAt: inventoryTimestamp}
	invalidCreates := []ReservationCreate{
		func() ReservationCreate { value := validCreate; value.LotID = "bad"; return value }(),
		func() ReservationCreate { value := validCreate; value.PlannedGrams = 0; return value }(),
		func() ReservationCreate {
			value := validCreate
			value.OccurredAt = "2026-08-04T12:00:00Z"
			return value
		}(),
	}
	for _, request := range invalidCreates {
		if _, failure := client.CreateInventoryReservation(context.Background(), request, "key"); failure == nil || failure.ExitCode != 2 {
			t.Errorf("request=%#v failure=%#v", request, failure)
		}
	}
	zero := int64(0)
	if _, failure := client.FinalizeInventoryReservation(context.Background(), inventoryEntryID, ReservationFinalize{ActualGrams: &zero, OccurredAt: inventoryTimestamp}, "key"); failure == nil {
		t.Fatal("accepted zero actual grams")
	}
	if _, failure := client.ReleaseInventoryReservation(context.Background(), inventoryEntryID, ReservationRelease{OccurredAt: "0000-01-01T00:00:00.000000Z"}, "key"); failure == nil {
		t.Fatal("accepted year-zero release")
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
}

func TestReservationMethodsRequireExactStatusesAndReplayIdentically(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		call   func(*Client) *output.Error
	}{
		{name: "create requires 201", status: http.StatusOK, call: func(client *Client) *output.Error {
			_, failure := client.CreateInventoryReservation(context.Background(), ReservationCreate{ClientReservationUUID: inventoryEntryID, ClientInstanceUUID: reservationClientInstanceID, RoastUUID: reservationRoastID, LotID: inventoryLotID, PlannedGrams: 1, OccurredAt: inventoryTimestamp}, "key")
			return failure
		}},
		{name: "finalize requires 200", status: http.StatusCreated, call: func(client *Client) *output.Error {
			_, failure := client.FinalizeInventoryReservation(context.Background(), inventoryEntryID, ReservationFinalize{OccurredAt: inventoryTimestamp}, "key")
			return failure
		}},
		{name: "release requires 200", status: http.StatusNoContent, call: func(client *Client) *output.Error {
			_, failure := client.ReleaseInventoryReservation(context.Background(), inventoryEntryID, ReservationRelease{OccurredAt: inventoryTimestamp}, "key")
			return failure
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				if test.status != http.StatusNoContent {
					_, _ = io.WriteString(w, reservationMutationJSON(false))
				}
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, "token", time.Second)
			failure := test.call(client)
			if failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}

	var bodies, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contents, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(contents))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(bodies) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":"busy","message":"Busy","details":null}}`)
			return
		}
		_, _ = io.WriteString(w, reservationMutationJSON(true))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "token", time.Second)
	if _, failure := client.ReleaseInventoryReservation(context.Background(), inventoryEntryID, ReservationRelease{OccurredAt: inventoryTimestamp}, "replay-key"); failure != nil {
		t.Fatal(failure)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] || !reflect.DeepEqual(keys, []string{"replay-key", "replay-key", "replay-key"}) {
		t.Fatalf("bodies=%q keys=%q", bodies, keys)
	}
}

func TestConflictResolutionUsesAdminRouteExactNormalizedNoteAndStatus(t *testing.T) {
	var path, body, key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		key = r.Header.Get("Idempotency-Key")
		contents, _ := io.ReadAll(r.Body)
		body = string(contents)
		_, _ = io.WriteString(w, validConflictJSON())
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "admin-token", time.Second)
	conflict, failure := client.ResolveInventoryConflict(context.Background(), "55555555-5555-4555-8555-555555555555", InventoryConflictResolutionWrite{ResolutionNote: " counted\r\nagain "}, "resolve-key")
	if failure != nil || conflict.ConflictID != inventoryConflictID {
		t.Fatalf("conflict=%#v failure=%#v", conflict, failure)
	}
	if path != "/api/v1/inventory/admin/conflicts/"+inventoryConflictID+"/resolve" || body != `{"resolution_note":"counted\nagain"}` || key != "resolve-key" {
		t.Fatalf("path=%q body=%q key=%q", path, body, key)
	}
}

func TestConflictResolutionRejectsInvalidNoteBeforeNetworkAndRequires200(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, validConflictJSON())
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "token", time.Second)
	for _, note := range []string{"", " \t ", strings.Repeat("x", 2001), "unsafe\x00note"} {
		if _, failure := client.ResolveInventoryConflict(context.Background(), inventoryConflictID, InventoryConflictResolutionWrite{ResolutionNote: note}, "key"); failure == nil || failure.ExitCode != 2 {
			t.Errorf("note length=%d failure=%#v", len(note), failure)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid note requests=%d", requests.Load())
	}
	if _, failure := client.ResolveInventoryConflict(context.Background(), inventoryConflictID, InventoryConflictResolutionWrite{ResolutionNote: "counted"}, "key"); failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("status failure=%#v", failure)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}
}
