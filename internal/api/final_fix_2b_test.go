package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const otherInventoryLotID = "99999999999949998999999999999999"
const otherInventoryImageID = "88888888888848888888888888888888"

func imageJSONFor(lotID, imageID string, position int, cover bool, caption, alt string) string {
	return fmt.Sprintf(`{"image_id":"%s","caption":%s,"alt_text":%s,"position":%d,"is_cover":%t,"display_width":1600,"display_height":1200,"thumbnail_width":480,"thumbnail_height":360,"display_url":"/api/v1/inventory/admin/bean-lots/%s/images/%s/display","thumbnail_url":"/api/v1/inventory/admin/bean-lots/%s/images/%s/thumbnail"}`,
		imageID, caption, alt, position, cover, lotID, imageID, lotID, imageID)
}

func detailWithImages(images ...string) string {
	payload := mutationLotJSON(0)
	cover := "null"
	for _, image := range images {
		if strings.Contains(image, `"is_cover":true`) {
			cover = image
			break
		}
	}
	payload = strings.Replace(payload, `"cover_image":null`, `"cover_image":`+cover, 1)
	return strings.Replace(payload, `"images":[]`, `"images":[`+strings.Join(images, ",")+`]`, 1)
}

func TestFinalFix2BRouteScopedResponsesAreBoundToRequest(t *testing.T) {
	otherLotDetail := strings.ReplaceAll(mutationLotJSON(0), mutationLotID, otherInventoryLotID)
	wrongConflict := strings.Replace(validConflictJSON(), inventoryConflictID, otherInventoryImageID, 1)
	wrongLotLedger := strings.Replace(validLedgerJSON(), inventoryLotID, otherInventoryLotID, 1)
	wrongLotReservation := strings.Replace(validReservationJSON(), inventoryLotID, otherInventoryLotID, 1)
	wrongLotConflict := strings.Replace(validConflictJSON(), inventoryLotID, otherInventoryLotID, 1)
	patch, _ := NewBeanLotPatch(map[string]any{"notes": nil})
	adjustment := InventoryAdjustmentWrite{QuantityGrams: 1, Reason: "count", OccurredAt: mutationStamp}
	tests := []struct {
		name, body string
		status     int
		call       func(*Client) *outputErrorAlias
	}{
		{name: "lot detail", body: otherLotDetail, status: http.StatusOK, call: func(c *Client) *outputErrorAlias { _, f := c.BeanLot(context.Background(), mutationLotID); return f }},
		{name: "ledger page", body: `{"items":[` + wrongLotLedger + `],"next_cursor":null}`, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.BeanLotLedger(context.Background(), mutationLotID, PageOptions{})
			return f
		}},
		{name: "reservation page", body: `{"items":[` + wrongLotReservation + `],"next_cursor":null}`, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.BeanLotReservations(context.Background(), mutationLotID, PageOptions{})
			return f
		}},
		{name: "conflict page", body: `{"items":[` + wrongLotConflict + `],"next_cursor":null}`, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.BeanLotConflicts(context.Background(), mutationLotID, PageOptions{})
			return f
		}},
		{name: "conflict detail", body: wrongConflict, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.InventoryConflict(context.Background(), inventoryConflictID)
			return f
		}},
		{name: "patch", body: otherLotDetail, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.PatchBeanLot(context.Background(), mutationLotID, patch, "key")
			return f
		}},
		{name: "adjust", body: otherLotDetail, status: http.StatusOK, call: func(c *Client) *outputErrorAlias {
			_, f := c.AdjustBeanLot(context.Background(), mutationLotID, adjustment, "key")
			return f
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			failure := test.call(client)
			if failure == nil || failure.Code != "invalid_server_response" || failure.ExitCode != 9 {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

// Alias keeps the route table readable while retaining the public failure type.
type outputErrorAlias = output.Error

func TestFinalFix2BImageMutationResponsesAreBoundToTargetAndOperation(t *testing.T) {
	first := imageJSONFor(mutationLotID, commandAPIImageID, 0, false, `"old"`, `null`)
	second := imageJSONFor(mutationLotID, otherInventoryImageID, 1, false, `null`, `null`)
	imagePath := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, _ := NewInventoryImagePatch(map[string]any{"caption": "new"})
	tests := []struct {
		name, body string
		call       func(*Client) *output.Error
	}{
		{name: "add wrong lot", body: strings.ReplaceAll(mutationLotJSON(0), mutationLotID, otherInventoryLotID), call: func(c *Client) *output.Error {
			_, f := c.AddInventoryImages(context.Background(), mutationLotID, []ImageUploadManifest{{UploadIndex: 0}}, []string{imagePath}, "key")
			return f
		}},
		{name: "update target metadata", body: detailWithImages(first), call: func(c *Client) *output.Error {
			_, f := c.PatchInventoryImage(context.Background(), mutationLotID, commandAPIImageID, patch, "key")
			return f
		}},
		{name: "reorder exact order", body: detailWithImages(second, strings.Replace(first, `"position":0`, `"position":1`, 1)), call: func(c *Client) *output.Error {
			_, f := c.ReorderInventoryImages(context.Background(), mutationLotID, []string{commandAPIImageID, otherInventoryImageID}, "key")
			return f
		}},
		{name: "delete absent", body: detailWithImages(first), call: func(c *Client) *output.Error {
			_, f := c.DeleteInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "key")
			return f
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := inventoryAPIClient(t, func(w http.ResponseWriter, _ *http.Request) { writeInventoryJSON(w, test.body) })
			if failure := test.call(client); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
		})
	}
}

func TestFinalFix2BRejectsInvalidUTF8InConsumedResponseText(t *testing.T) {
	payload := bytes.Replace([]byte(validDetailJSON()), []byte("Ethiopia Guji"), append([]byte{0xff}, []byte("thiopia Guji")...), 1)
	if err := decodeOneJSON(payload, &BeanLotDetail{}); err == nil {
		t.Fatal("accepted invalid UTF-8 in consumed response text")
	}
}

func TestFinalFix2BConsumedProjectionInvariants(t *testing.T) {
	secondImage := strings.ReplaceAll(validImageJSON(), inventoryImageID, otherInventoryImageID)
	secondImage = strings.Replace(secondImage, `"position":0`, `"position":1`, 1)
	secondImage = strings.Replace(secondImage, `"is_cover":true`, `"is_cover":false`, 1)
	validTwoImages := strings.Replace(validDetailJSON(), `"images":[`+validImageJSON()+`]`, `"images":[`+validImageJSON()+`,`+secondImage+`]`, 1)
	invalid := []struct{ name, payload string }{
		{name: "summary normalized name", payload: strings.Replace(validDetailJSON(), `"name":"Ethiopia Guji"`, `"name":" Ethiopia Guji "`, 1)},
		{name: "summary normalized origin", payload: strings.Replace(validDetailJSON(), `"origin":"Ethiopia"`, `"origin":" Ethiopia "`, 1)},
		{name: "producer bound", payload: strings.Replace(validDetailJSON(), `"producer":null`, `"producer":"`+strings.Repeat("x", 201)+`"`, 1)},
		{name: "varietal duplicate", payload: strings.Replace(validDetailJSON(), `"varietals":["Heirloom"]`, `"varietals":["Heirloom","Heirloom"]`, 1)},
		{name: "processing other detail", payload: strings.Replace(strings.Replace(validDetailJSON(), `"processing_method":"washed"`, `"processing_method":"other"`, 1), `"processing_detail":null`, `"processing_detail":null`, 1)},
		{name: "altitude order", payload: strings.Replace(validDetailJSON(), `"altitude_min_metres":1900,"altitude_max_metres":2100`, `"altitude_min_metres":2101,"altitude_max_metres":2100`, 1)},
		{name: "state archive coherence", payload: strings.Replace(validDetailJSON(), `"state":"active"`, `"state":"archived"`, 1)},
		{name: "too many images", payload: strings.Replace(validDetailJSON(), `"images":[`+validImageJSON()+`]`, `"images":[`+strings.TrimSuffix(strings.Repeat(validImageJSON()+",", 9), ",")+`]`, 1)},
		{name: "image slice position order", payload: strings.Replace(validTwoImages, `"position":1`, `"position":2`, 1)},
		{name: "duplicate image id", payload: strings.Replace(validTwoImages, otherInventoryImageID, inventoryImageID, 1)},
		{name: "image caption normalization", payload: strings.Replace(validDetailJSON(), `"caption":null`, `"caption":" padded "`, 1)},
		{name: "image alt bound", payload: strings.Replace(validDetailJSON(), `"alt_text":"cover"`, `"alt_text":"`+strings.Repeat("x", 301)+`"`, 1)},
		{name: "created after updated", payload: strings.Replace(validDetailJSON(), `"created_at":"`+inventoryTimestamp+`"`, `"created_at":"2026-08-04T12:00:01.000000Z"`, 1)},
		{name: "ledger reason normalization", payload: strings.Replace(validLedgerJSON(), `"reason":"count"`, `"reason":" count "`, 1)},
		{name: "ledger operation shape", payload: strings.Replace(validLedgerJSON(), `"reserved_delta":0`, `"reserved_delta":1`, 1)},
		{name: "reservation timestamp order", payload: strings.Replace(validReservationJSON(), `"updated_at":"`+inventoryTimestamp+`"`, `"updated_at":"2026-08-04T11:59:59.000000Z"`, 1)},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			var destination any
			switch {
			case strings.HasPrefix(test.name, "ledger"):
				destination = &InventoryLedgerEntry{}
			case strings.HasPrefix(test.name, "reservation"):
				destination = &InventoryReservation{}
			default:
				destination = &BeanLotDetail{}
			}
			if err := decodeOneJSON([]byte(test.payload), destination); err == nil {
				t.Fatal("accepted invalid projection")
			}
		})
	}

	boundary := strings.Repeat("😀", 200)
	payload := strings.Replace(validDetailJSON(), `"name":"Ethiopia Guji"`, `"name":"`+boundary+`"`, 1)
	payload = strings.TrimSuffix(payload, "}") + `,"future":{"safe":true}}`
	if err := decodeOneJSON([]byte(payload), &BeanLotDetail{}); err != nil {
		t.Fatalf("valid normalized boundary/additive field rejected: %v", err)
	}
}

func TestFinalFix2BLedgerOperationAccountingShapes(t *testing.T) {
	tests := []struct {
		name, operation  string
		onHand, reserved int
		targets, reason  bool
		valid            bool
	}{
		{name: "opening", operation: "opening_balance", onHand: 1, reason: true, valid: true},
		{name: "opening zero", operation: "opening_balance", onHand: 0, reason: true},
		{name: "manual", operation: "manual_adjustment", onHand: -1, reason: true, valid: true},
		{name: "manual missing reason", operation: "manual_adjustment", onHand: 1},
		{name: "reservation", operation: "reservation", reserved: 1, targets: true, valid: true},
		{name: "reservation missing targets", operation: "reservation", reserved: 1},
		{name: "release", operation: "reservation_release", reserved: -1, targets: true, valid: true},
		{name: "release wrong sign", operation: "reservation_release", reserved: 1, targets: true},
		{name: "consumption", operation: "consumption", onHand: -1, reserved: -1, targets: true, valid: true},
		{name: "consumption wrong sign", operation: "consumption", onHand: 1, reserved: -1, targets: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reason, roast, reservation := "null", "null", "null"
			if test.reason {
				reason = `"count"`
			}
			if test.targets {
				roast, reservation = `"`+inventoryImageID+`"`, `"`+inventoryReservationID+`"`
			}
			payload := fmt.Sprintf(`{"entry_id":"%s","operation":"%s","lot_id":"%s","roast_uuid":%s,"reservation_id":%s,"on_hand_delta":%d,"reserved_delta":%d,"resulting_on_hand_grams":0,"resulting_reserved_grams":0,"resulting_available_grams":0,"reason":%s,"reference":null,"actor_kind":"desktop","occurred_at":"%s","created_at":"%s"}`,
				inventoryEntryID, test.operation, inventoryLotID, roast, reservation, test.onHand, test.reserved, reason, inventoryTimestamp, inventoryTimestamp)
			err := decodeOneJSON([]byte(payload), &InventoryLedgerEntry{})
			if (err == nil) != test.valid {
				t.Fatalf("error = %v, valid = %t", err, test.valid)
			}
		})
	}
}

func TestFinalFix2BValidProjectionBoundaries(t *testing.T) {
	var lot map[string]any
	if err := json.Unmarshal([]byte(validDetailJSON()), &lot); err != nil {
		t.Fatal(err)
	}
	lot["name"] = strings.Repeat("😀", 200)
	lot["origin"] = strings.Repeat("😀", 100)
	lot["producer"] = strings.Repeat("😀", 200)
	lot["supplier"] = strings.Repeat("😀", 200)
	lot["external_reference"] = strings.Repeat("😀", 200)
	lot["processing_method"] = "other"
	lot["processing_detail"] = strings.Repeat("😀", 200)
	lot["notes"] = strings.Repeat("😀", 10000)
	lot["altitude_min_metres"] = float64(9000)
	lot["altitude_max_metres"] = float64(9000)
	varietals := make([]any, 16)
	for index := range varietals {
		varietals[index] = fmt.Sprintf("%02d%s", index, strings.Repeat("x", 98))
	}
	lot["varietals"] = varietals
	images := make([]any, MaxInventoryImages)
	for index := range images {
		imageID := fmt.Sprintf("%032x", index+16)
		var image map[string]any
		if err := json.Unmarshal([]byte(imageJSONFor(inventoryLotID, imageID, index, index == 0, `null`, `null`)), &image); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			image["caption"] = strings.Repeat("😀", 500)
			image["alt_text"] = strings.Repeat("😀", 300)
			lot["cover_image"] = image
		}
		images[index] = image
	}
	lot["images"] = images
	lot["future"] = map[string]any{"safe": true}
	payload, err := json.Marshal(lot)
	if err != nil {
		t.Fatal(err)
	}
	if err := decodeOneJSON(payload, &BeanLotDetail{}); err != nil {
		t.Fatalf("valid detail boundaries rejected: %v", err)
	}

	ledger := strings.Replace(validLedgerJSON(), `"reason":"count"`, `"reason":"`+strings.Repeat("😀", 2000)+`"`, 1)
	ledger = strings.Replace(ledger, `"reference":null`, `"reference":"`+strings.Repeat("😀", 200)+`"`, 1)
	if err := decodeOneJSON([]byte(ledger), &InventoryLedgerEntry{}); err != nil {
		t.Fatalf("valid ledger text boundaries rejected: %v", err)
	}
}

func TestFinalFix2BReservationNestedConflictRoastIsBound(t *testing.T) {
	payload := strings.Replace(reservationMutationJSON("reserved", false), `"roast_uuid":"`+reservationRoastID+`"`, `"roast_uuid":"`+inventoryImageID+`"`, 2)
	// Restore the reservation roast, leaving only the nested conflict hostile.
	payload = strings.Replace(payload, `"roast_uuid":"`+inventoryImageID+`"`, `"roast_uuid":"`+reservationRoastID+`"`, 1)
	if err := decodeOneJSON([]byte(payload), &ReservationMutationResponse{}); err == nil {
		t.Fatal("accepted nested conflict for another roast")
	}
}

func TestFinalFix2BAdmin404ClassificationIsCentralized(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "image.jpg")
	if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, _ := NewBeanLotPatch(map[string]any{"notes": nil})
	imagePatch, _ := NewInventoryImagePatch(map[string]any{"caption": nil})
	calls := []struct {
		name string
		call func(*Client, string) *output.Error
	}{
		{name: "read", call: func(c *Client, _ string) *output.Error {
			_, f := c.BeanLot(context.Background(), mutationLotID)
			return f
		}},
		{name: "create", call: func(c *Client, _ string) *output.Error {
			_, f := c.CreateBeanLot(context.Background(), BeanLotCreateManifest{Fields: BeanLotFields{Name: "Lot", Varietals: []string{}}, Images: []ImageUploadManifest{}}, "key")
			return f
		}},
		{name: "patch", call: func(c *Client, _ string) *output.Error {
			_, f := c.PatchBeanLot(context.Background(), mutationLotID, patch, "key")
			return f
		}},
		{name: "image", call: func(c *Client, _ string) *output.Error {
			_, f := c.PatchInventoryImage(context.Background(), mutationLotID, commandAPIImageID, imagePatch, "key")
			return f
		}},
		{name: "resolve", call: func(c *Client, _ string) *output.Error {
			_, f := c.ResolveInventoryConflict(context.Background(), inventoryConflictID, InventoryConflictResolutionWrite{ResolutionNote: "counted"}, "key")
			return f
		}},
		{name: "download", call: func(c *Client, destination string) *output.Error {
			_, f := c.DownloadInventoryImage(context.Background(), mutationLotID, commandAPIImageID, "display", destination, false)
			return f
		}},
	}
	for _, body := range []struct{ name, value, want string }{
		{name: "namespace", value: `{"detail":"Not Found"}`, want: "server_upgrade_required"},
		{name: "pinned entity", value: `{"error":{"code":"bean_lot_not_found","message":"Bean lot not found","details":null}}`, want: "bean_lot_not_found"},
	} {
		for _, call := range calls {
			if call.name == "create" && body.name == "pinned entity" {
				continue
			}
			t.Run(call.name+"/"+body.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, body.value)
				}))
				defer server.Close()
				client, _ := NewClient(server.URL, "classification-secret", time.Second)
				failure := call.call(client, filepath.Join(t.TempDir(), "download.webp"))
				if failure == nil || failure.Code != body.want || failure.ExitCode != map[string]int{"server_upgrade_required": 9, "bean_lot_not_found": 6}[body.want] {
					t.Fatalf("failure = %#v", failure)
				}
			})
		}
	}
}
