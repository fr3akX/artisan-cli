package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

const validChartJSON = `{
  "control": {"markers": [], "steps": []},
  "core": {"bt": [100.0], "bt_ror": [null], "et": [120.0], "et_ror": [null], "time_seconds": [0.0]},
  "events": {"milestones": [], "special": []},
  "extra": {"series": []},
  "parser_version": "artisan-4-v1",
  "schema_version": 1,
  "source_temperature_unit": "C",
  "summary": {"duration_seconds": 0.0, "extra_series_count": 0, "sample_count": 1, "special_event_count": 0}
}`

func TestDownloadRoastChartVerifiesWireAndInstallsExactJSON(t *testing.T) {
	raw := []byte(validChartJSON)
	compressed := deterministicGzip(t, raw)
	destination := filepath.Join(t.TempDir(), "chosen-chart.json")
	var paths []string
	client := chartClient(t, compressed, nil)
	var protectedFiles atomic.Int32
	protect := client.downloadOps.protect
	client.downloadOps.protect = func(file *os.File) error {
		protectedFiles.Add(1)
		return protect(file)
	}
	baseTransport := client.httpClient.Transport
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.RequestURI())
		if request.Header.Get("Authorization") != "Bearer chart-secret" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.URL.Path == "/api/v1/roasts/"+roastUUID+"/chart" {
			if got := request.Header.Values("Accept-Encoding"); len(got) != 1 || got[0] != "gzip" {
				t.Errorf("Accept-Encoding = %v", got)
			}
		}
		return baseTransport.RoundTrip(request)
	})

	result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(contents, raw) {
		t.Fatalf("contents differ: bytes=%d err=%v", len(contents), err)
	}
	if result != (RoastChartDownload{
		Path: destination, RoastUUID: roastUUID, RevisionNumber: 1, RevisionSHA256: roastSHA256,
		ParserVersion: "artisan-4-v1", ChartSchemaVersion: 1,
		CompressedBytes: int64(len(compressed)), CompressedSHA256: chartSHA(compressed),
		FileBytes: int64(len(raw)), FileSHA256: chartSHA(raw),
	}) {
		t.Fatalf("result = %#v", result)
	}
	wantPaths := []string{
		"/api/v1/roasts/" + roastUUID,
		"/api/v1/roasts/" + roastUUID + "/chart",
		"/api/v1/roasts/" + roastUUID,
	}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("paths = %v", paths)
	}
	file, err := securefile.OpenPrivate(destination)
	if err != nil {
		t.Fatalf("installed private contract: %v", err)
	}
	_ = file.Close()
	if protectedFiles.Load() != 2 {
		t.Fatalf("protected staging files = %d, want decompressed and compressed", protectedFiles.Load())
	}
	assertNoDownloadTemps(t, destination)
}

func TestRoastChartDownloadJSONContract(t *testing.T) {
	value := RoastChartDownload{
		Path: "chart.json", RoastUUID: roastUUID, RevisionNumber: 3, RevisionSHA256: roastSHA256,
		ParserVersion: "artisan-4-v1", ChartSchemaVersion: 1,
		CompressedBytes: 12, CompressedSHA256: strings.Repeat("a", 64),
		FileBytes: 34, FileSHA256: strings.Repeat("b", 64),
	}
	encoded, err := json.Marshal(value)
	want := `{"path":"chart.json","roast_uuid":"` + roastUUID + `","revision_number":3,"revision_sha256":"` + roastSHA256 + `","parser_version":"artisan-4-v1","chart_schema_version":1,"compressed_bytes":12,"compressed_sha256":"` + strings.Repeat("a", 64) + `","file_bytes":34,"file_sha256":"` + strings.Repeat("b", 64) + `"}`
	if err != nil || string(encoded) != want {
		t.Fatalf("encoded = %s, %v", encoded, err)
	}
}

func TestDownloadRoastChartRejectsHostileSecurityHeaders(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	for _, test := range []struct {
		name   string
		mutate func(*http.Response)
	}{
		{name: "auto decompressed", mutate: func(r *http.Response) { r.Uncompressed = true }},
		{name: "missing content type", mutate: func(r *http.Response) { r.Header.Del("Content-Type") }},
		{name: "content type parameter", mutate: func(r *http.Response) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }},
		{name: "duplicate content type", mutate: func(r *http.Response) { r.Header.Add("Content-Type", "application/json") }},
		{name: "missing encoding", mutate: func(r *http.Response) { r.Header.Del("Content-Encoding") }},
		{name: "transfer encoding", mutate: func(r *http.Response) { r.TransferEncoding = []string{"chunked"} }},
		{name: "multiple encodings", mutate: func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip, br") }},
		{name: "duplicate encoding", mutate: func(r *http.Response) { r.Header.Add("Content-Encoding", "gzip") }},
		{name: "missing length", mutate: func(r *http.Response) { r.Header.Del("Content-Length"); r.ContentLength = -1 }},
		{name: "invalid length", mutate: func(r *http.Response) { r.Header.Set("Content-Length", "01") }},
		{name: "duplicate length", mutate: func(r *http.Response) { r.Header.Add("Content-Length", strconv.Itoa(len(compressed))) }},
		{name: "response length mismatch", mutate: func(r *http.Response) { r.ContentLength++ }},
		{name: "missing etag", mutate: func(r *http.Response) { r.Header.Del("ETag") }},
		{name: "unquoted etag", mutate: func(r *http.Response) { r.Header.Set("ETag", chartSHA(compressed)) }},
		{name: "duplicate etag", mutate: func(r *http.Response) { r.Header.Add("ETag", `"`+chartSHA(compressed)+`"`) }},
		{name: "missing content checksum", mutate: func(r *http.Response) { r.Header.Del("X-Content-SHA256") }},
		{name: "duplicate content checksum", mutate: func(r *http.Response) { r.Header.Add("X-Content-SHA256", chartSHA(compressed)) }},
		{name: "missing checksum", mutate: func(r *http.Response) { r.Header.Del("X-Checksum-SHA256") }},
		{name: "conflicting checksum", mutate: func(r *http.Response) { r.Header.Set("X-Checksum-SHA256", strings.Repeat("a", 64)) }},
		{name: "missing parser", mutate: func(r *http.Response) { r.Header.Del("X-Parser-Version") }},
		{name: "parser mismatch", mutate: func(r *http.Response) { r.Header.Set("X-Parser-Version", "artisan-5-v1") }},
		{name: "duplicate parser", mutate: func(r *http.Response) { r.Header.Add("X-Parser-Version", "artisan-4-v1") }},
		{name: "missing schema", mutate: func(r *http.Response) { r.Header.Del("X-Chart-Schema-Version") }},
		{name: "unsupported schema", mutate: func(r *http.Response) { r.Header.Set("X-Chart-Schema-Version", "2") }},
		{name: "duplicate schema", mutate: func(r *http.Response) { r.Header.Add("X-Chart-Schema-Version", "1") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "chart.json")
			client := chartClient(t, compressed, test.mutate)
			if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
			assertMissingFileAndTemps(t, destination)
		})
	}
}

func TestRoastChartCompressedStageRemainsBoundToVerifiedWireBytes(t *testing.T) {
	original := deterministicGzip(t, []byte(validChartJSON))
	mutated := append([]byte{}, original...)
	// The gzip OS metadata byte is checksum-independent. Changing it retains a
	// valid single member with identical JSON while changing the staged wire
	// identity that must remain bound to the server-verified transfer.
	mutated[9] ^= 1

	destination := filepath.Join(t.TempDir(), "chart.json")
	operations := defaultDownloadOperations()
	target, err := newDownloadTarget(destination, false, operations)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Abort()
	compressedTarget, err := newDownloadTarget(destination, true, operations)
	if err != nil {
		t.Fatal(err)
	}
	defer compressedTarget.Abort()
	if _, err := compressedTarget.Writer().Write(original); err != nil {
		t.Fatal(err)
	}
	if _, err := compressedTarget.heldSourceFile().WriteAt(mutated, 0); err != nil {
		t.Fatal(err)
	}

	if _, _, failure := decompressAndValidateChart(context.Background(), target, compressedTarget, int64(len(original)), "artisan-4-v1"); failure == nil || failure.Code != "invalid_server_response" {
		t.Fatalf("failure = %#v", failure)
	}
	compressedTarget.Abort()
	target.Abort()
	assertMissingFileAndTemps(t, destination)
}

func TestDownloadRoastChartRejectsWrongWireCountChecksumAndGzipFraming(t *testing.T) {
	valid := deterministicGzip(t, []byte(validChartJSON))
	otherMember := deterministicGzip(t, []byte(`{}`))
	for _, test := range []struct {
		name     string
		declared []byte
		body     []byte
		mutate   func(*http.Response)
	}{
		{name: "short wire body", declared: valid, body: valid[:len(valid)-1]},
		{name: "long wire body", declared: valid, body: append(append([]byte{}, valid...), 0)},
		{name: "wire checksum mismatch", declared: valid, body: append([]byte{}, valid...), mutate: func(r *http.Response) { r.Header.Set("X-Checksum-SHA256", strings.Repeat("a", 64)) }},
		{name: "malformed gzip", declared: []byte("not gzip"), body: []byte("not gzip")},
		{name: "trailing bytes", declared: append(append([]byte{}, valid...), 'x'), body: append(append([]byte{}, valid...), 'x')},
		{name: "second gzip member", declared: append(append([]byte{}, valid...), otherMember...), body: append(append([]byte{}, valid...), otherMember...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "chart.json")
			client := chartClient(t, test.declared, func(response *http.Response) {
				response.Body = io.NopCloser(bytes.NewReader(test.body))
				response.ContentLength = int64(len(test.declared))
				if test.mutate != nil {
					test.mutate(response)
				}
			})
			if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
			assertMissingFileAndTemps(t, destination)
		})
	}
}

func TestDownloadRoastChartEnforcesCompressedAndExpandedCeilings(t *testing.T) {
	t.Run("compressed declared over ceiling", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "chart.json")
		valid := deterministicGzip(t, []byte(validChartJSON))
		client := chartClient(t, valid, func(response *http.Response) {
			response.Header.Set("Content-Length", strconv.FormatInt(maxRoastChartBytes+1, 10))
			response.ContentLength = maxRoastChartBytes + 1
		})
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
			t.Fatalf("failure = %#v", failure)
		}
		assertMissingFileAndTemps(t, destination)
	})

	t.Run("wire body exceeds declared count", func(t *testing.T) {
		if testing.Short() {
			t.Skip("64 MiB bounded streaming check")
		}
		destination := filepath.Join(t.TempDir(), "chart.json")
		client := chartClient(t, []byte("placeholder"), func(response *http.Response) {
			response.Header.Set("Content-Length", strconv.FormatInt(maxRoastChartBytes, 10))
			response.Header.Set("ETag", `"`+strings.Repeat("a", 64)+`"`)
			response.Header.Set("X-Content-SHA256", strings.Repeat("a", 64))
			response.Header.Set("X-Checksum-SHA256", strings.Repeat("a", 64))
			response.ContentLength = maxRoastChartBytes
			response.Body = io.NopCloser(io.LimitReader(zeroReader{}, maxRoastChartBytes+1))
		})
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
			t.Fatalf("failure = %#v", failure)
		}
		assertMissingFileAndTemps(t, destination)
	})

	t.Run("gzip expansion", func(t *testing.T) {
		if testing.Short() {
			t.Skip("64 MiB bounded expansion check")
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := io.CopyN(writer, zeroReader{}, maxRoastChartBytes+1); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "chart.json")
		client := chartClient(t, compressed.Bytes(), nil)
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
			t.Fatalf("failure = %#v", failure)
		}
		assertMissingFileAndTemps(t, destination)
	})
}

func TestDownloadRoastChartStrictJSONSchemaValidation(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "invalid utf8", raw: append([]byte(validChartJSON), 0xff)},
		{name: "invalid surrogate", raw: []byte(strings.Replace(validChartJSON, `"control":`, `"future":"\ud800","control":`, 1))},
		{name: "multiple documents", raw: []byte(validChartJSON + `{}`)},
		{name: "array root", raw: []byte(`[]`)},
		{name: "missing control", raw: []byte(strings.Replace(validChartJSON, `"control": {"markers": [], "steps": []},`, ``, 1))},
		{name: "null control", raw: []byte(strings.Replace(validChartJSON, `"control": {"markers": [], "steps": []}`, `"control": null`, 1))},
		{name: "wrong markers", raw: []byte(strings.Replace(validChartJSON, `"markers": []`, `"markers": {}`, 1))},
		{name: "missing core field", raw: []byte(strings.Replace(validChartJSON, `"bt_ror": [null], `, ``, 1))},
		{name: "wrong core sample", raw: []byte(strings.Replace(validChartJSON, `"bt": [100.0]`, `"bt": ["100"]`, 1))},
		{name: "null time sample", raw: []byte(strings.Replace(validChartJSON, `"time_seconds": [0.0]`, `"time_seconds": [null]`, 1))},
		{name: "core length mismatch", raw: []byte(strings.Replace(validChartJSON, `"et_ror": [null]`, `"et_ror": []`, 1))},
		{name: "sample count mismatch", raw: []byte(strings.Replace(validChartJSON, `"sample_count": 1`, `"sample_count": 2`, 1))},
		{name: "extra count mismatch", raw: []byte(strings.Replace(validChartJSON, `"extra_series_count": 0`, `"extra_series_count": 1`, 1))},
		{name: "special count mismatch", raw: []byte(strings.Replace(validChartJSON, `"special_event_count": 0`, `"special_event_count": 1`, 1))},
		{name: "parser body mismatch", raw: []byte(strings.Replace(validChartJSON, `"parser_version": "artisan-4-v1"`, `"parser_version": "other"`, 1))},
		{name: "schema body mismatch", raw: []byte(strings.Replace(validChartJSON, `"schema_version": 1`, `"schema_version": 2`, 1))},
		{name: "fractional schema", raw: []byte(strings.Replace(validChartJSON, `"schema_version": 1`, `"schema_version": 1.0`, 1))},
		{name: "unsupported unit", raw: []byte(strings.Replace(validChartJSON, `"source_temperature_unit": "C"`, `"source_temperature_unit": "K"`, 1))},
		{name: "duplicate key", raw: []byte(strings.Replace(validChartJSON, `"schema_version": 1`, `"schema_version": 1, "schema_version": 1`, 1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "chart.json")
			client := chartClient(t, deterministicGzip(t, test.raw), nil)
			if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "invalid_server_response" {
				t.Fatalf("failure = %#v", failure)
			}
			assertMissingFileAndTemps(t, destination)
		})
	}

	for _, unit := range []string{`"F"`, `null`} {
		raw := []byte(strings.Replace(validChartJSON, `"source_temperature_unit": "C"`, `"source_temperature_unit": `+unit, 1))
		destination := filepath.Join(t.TempDir(), "valid-unit.json")
		client := chartClient(t, deterministicGzip(t, raw), nil)
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure != nil {
			t.Fatalf("unit %s: %v", unit, failure)
		}
	}
}

func TestDownloadRoastChartRevisionFencePreservesForcedDestination(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	changedSHA := strings.Repeat("e", 64)
	for _, detail := range []string{
		strings.Replace(validRoastDetailJSON(), roastSHA256, changedSHA, 1),
		strings.Replace(strings.Replace(validRoastDetailJSON(), `"state":"parsed"`, `"state":"parse_failed"`, 1), `"parse_state":"parsed"`, `"parse_state":"failed"`, 1),
	} {
		destination := filepath.Join(t.TempDir(), "existing.json")
		if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		client := chartClientWithDetails(t, compressed, []string{validRoastDetailJSON(), detail}, nil)
		result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, true)
		if failure == nil || failure.Code != "roast_revision_changed" || failure.ExitCode != 7 || failure.HTTPStatus != nil || result != (RoastChartDownload{}) {
			t.Fatalf("result=%#v failure=%#v", result, failure)
		}
		if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing" {
			t.Fatalf("destination = %q, %v", contents, err)
		}
		assertNoDownloadTemps(t, destination)
	}
}

func TestDownloadRoastChartRequiresCurrentParsedRevision(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	for _, detail := range []string{
		strings.Replace(strings.Replace(strings.Replace(validRoastDetailJSON(), `"state":"parsed"`, `"state":"awaiting_profile"`, 1), `"revision_count":1`, `"revision_count":0`, 1), `"current_revision":`+validRoastRevisionJSON(), `"current_revision":null`, 1),
		strings.Replace(strings.Replace(validRoastDetailJSON(), `"state":"parsed"`, `"state":"parse_failed"`, 1), `"parse_state":"parsed"`, `"parse_state":"failed"`, 1),
	} {
		var chartRequests atomic.Int32
		client := chartClientWithDetails(t, compressed, []string{detail}, nil)
		base := client.httpClient.Transport
		client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/chart") {
				chartRequests.Add(1)
			}
			return base.RoundTrip(request)
		})
		destination := filepath.Join(t.TempDir(), "chart.json")
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "chart_unavailable" {
			t.Fatalf("failure = %#v", failure)
		}
		if chartRequests.Load() != 0 {
			t.Fatalf("chart requests = %d", chartRequests.Load())
		}
		assertMissingFileAndTemps(t, destination)
	}
}

func TestDownloadRoastChartRetriesReadFailuresAndResetsCompressedTemporary(t *testing.T) {
	raw := []byte(validChartJSON)
	compressed := deterministicGzip(t, raw)
	destination := filepath.Join(t.TempDir(), "retry.json")
	var attempts atomic.Int32
	var closes atomic.Int32
	client := chartClient(t, compressed, func(response *http.Response) {
		if attempts.Add(1) == 1 {
			response.Body = &failingDownloadReadCloser{data: append([]byte{}, compressed[:len(compressed)/2]...), err: errors.New("temporary read failure"), closes: &closes}
		} else {
			response.Body = &failingDownloadReadCloser{data: append([]byte{}, compressed...), closes: &closes}
		}
	})
	result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false)
	if failure != nil {
		t.Fatal(failure)
	}
	if attempts.Load() != 2 || closes.Load() != 2 || result.FileSHA256 != chartSHA(raw) {
		t.Fatalf("attempts=%d closes=%d result=%#v", attempts.Load(), closes.Load(), result)
	}
	if contents, err := os.ReadFile(destination); err != nil || !bytes.Equal(contents, raw) {
		t.Fatalf("contents = %d bytes, %v", len(contents), err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastChartLocalAndPreInstallFailuresPreserveForcedDestination(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	for _, test := range []struct {
		name   string
		inject func(*Client)
	}{
		{name: "compressed write", inject: func(client *Client) {
			client.downloadOps.writer = func(*os.File) io.Writer { return failingDownloadWriter{err: errors.New("write")} }
		}},
		{name: "decompressed short write", inject: func(client *Client) {
			var calls atomic.Int32
			client.downloadOps.writer = func(file *os.File) io.Writer {
				if calls.Add(1) == 1 {
					return shortDownloadWriter{}
				}
				return file
			}
		}},
		{name: "compressed cleanup close", inject: func(client *Client) {
			var closes atomic.Int32
			client.downloadOps.closeFile = func(file *os.File) error {
				err := file.Close()
				if closes.Add(1) == 1 {
					return errors.Join(err, errors.New("compressed close"))
				}
				return err
			}
		}},
		{name: "sync before install", inject: func(client *Client) {
			client.downloadOps.syncFile = func(*os.File) error { return errors.New("sync") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "chart.json")
			if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			client := chartClient(t, compressed, nil)
			test.inject(client)
			if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, true); failure == nil || failure.Code != "local_storage_error" {
				t.Fatalf("failure = %#v", failure)
			}
			if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing" {
				t.Fatalf("destination = %q, %v", contents, err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadRoastChartReportsInstalledDurabilityUncertainty(t *testing.T) {
	raw := []byte(validChartJSON)
	compressed := deterministicGzip(t, raw)
	destination := filepath.Join(t.TempDir(), "chart.json")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := chartClient(t, compressed, nil)
	injectDownloadDurabilityFailure(&client.downloadOps)
	result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, true)
	if failure == nil || failure.Code != "local_storage_error" || result.Path != destination || result.FileSHA256 != chartSHA(raw) {
		t.Fatalf("result=%#v failure=%#v", result, failure)
	}
	if contents, err := os.ReadFile(destination); err != nil || !bytes.Equal(contents, raw) {
		t.Fatalf("contents = %d bytes, %v", len(contents), err)
	}
}

func TestDownloadRoastChartRefusesRedirectAndCancellationWithoutVisibility(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		var targetRequests atomic.Int32
		target := httptestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetRequests.Add(1) }))
		source := httptestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/chart") {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, validRoastDetailJSON())
		}))
		client, _ := NewClient(source.URL, "secret", time.Second)
		destination := filepath.Join(t.TempDir(), "chart.json")
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "redirect_refused" {
			t.Fatalf("failure = %#v", failure)
		}
		if targetRequests.Load() != 0 {
			t.Fatalf("redirect target requests = %d", targetRequests.Load())
		}
		assertMissingFileAndTemps(t, destination)
	})

	t.Run("body cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		blocked := &cancelReadCloser{ctx: ctx}
		compressed := deterministicGzip(t, []byte(validChartJSON))
		client := chartClient(t, compressed, func(response *http.Response) { response.Body = blocked })
		go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		destination := filepath.Join(t.TempDir(), "chart.json")
		if _, failure := client.DownloadRoastChart(ctx, roastUUID, destination, false); failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
			t.Fatalf("failure = %#v", failure)
		}
		if !blocked.closed.Load() {
			t.Fatal("body not closed")
		}
		assertMissingFileAndTemps(t, destination)
	})
}

func TestDownloadRoastChartCancellationStopsDecompressionBeforeVisibility(t *testing.T) {
	raw := []byte(strings.TrimSuffix(validChartJSON, "}") + `,"padding":"` + strings.Repeat("a", 1<<20) + `"}`)
	compressed := deterministicGzip(t, raw)
	destination := filepath.Join(t.TempDir(), "cancel-decompression.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := chartClient(t, compressed, nil)
	var writerCalls atomic.Int32
	var written atomic.Int64
	client.downloadOps.writer = func(file *os.File) io.Writer {
		if writerCalls.Add(1) != 1 {
			return file
		}
		return writerFunc(func(buffer []byte) (int, error) {
			count, err := file.Write(buffer)
			written.Add(int64(count))
			cancel()
			return count, err
		})
	}
	if _, failure := client.DownloadRoastChart(ctx, roastUUID, destination, false); failure == nil || failure.Code != "interrupted" || failure.ExitCode != 130 {
		t.Fatalf("failure = %#v", failure)
	}
	if written.Load() >= int64(len(raw)) {
		t.Fatalf("decompression ignored cancellation and wrote %d bytes", written.Load())
	}
	assertMissingFileAndTemps(t, destination)
}

func TestDownloadRoastChartRetriesTransientStatusAndRefusesTimeout(t *testing.T) {
	t.Run("transient status", func(t *testing.T) {
		raw := []byte(validChartJSON)
		compressed := deterministicGzip(t, raw)
		destination := filepath.Join(t.TempDir(), "retry-status.json")
		client := chartClient(t, compressed, nil)
		base := client.httpClient.Transport
		var attempts atomic.Int32
		client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/chart") && attempts.Add(1) == 1 {
				return jsonHTTPResponse(http.StatusServiceUnavailable, `{"error":{"code":"temporary","message":"Temporary","details":null}}`), nil
			}
			return base.RoundTrip(request)
		})
		result, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false)
		if failure != nil || attempts.Load() != 2 || result.FileSHA256 != chartSHA(raw) {
			t.Fatalf("attempts=%d result=%#v failure=%#v", attempts.Load(), result, failure)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		compressed := deterministicGzip(t, []byte(validChartJSON))
		server := httptestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/chart") {
				time.Sleep(80 * time.Millisecond)
				response := chartHTTPResponse(compressed)
				for name, values := range response.Header {
					w.Header()[name] = values
				}
				_, _ = w.Write(compressed)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, validRoastDetailJSON())
		}))
		client, _ := NewClient(server.URL, "secret", 10*time.Millisecond)
		destination := filepath.Join(t.TempDir(), "timeout.json")
		if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Code != "network_error" {
			t.Fatalf("failure = %#v", failure)
		}
		assertMissingFileAndTemps(t, destination)
	})
}

func TestDownloadRoastChartValidationFailuresPreserveForcedDestination(t *testing.T) {
	for _, test := range []struct {
		name     string
		raw      []byte
		mutate   func(*http.Response)
		wantCode string
	}{
		{name: "hostile header", raw: []byte(validChartJSON), mutate: func(response *http.Response) { response.Header.Set("X-Checksum-SHA256", strings.Repeat("a", 64)) }, wantCode: "invalid_server_response"},
		{name: "invalid json", raw: []byte(`{"core":`), wantCode: "invalid_server_response"},
		{name: "server failure", raw: []byte(validChartJSON), mutate: func(response *http.Response) {
			response.StatusCode = http.StatusForbidden
			response.Header = http.Header{"Content-Type": []string{"application/json"}}
			response.Body = io.NopCloser(strings.NewReader(`{"error":{"code":"permission_denied","message":"Denied","details":null}}`))
			response.ContentLength = -1
		}, wantCode: "permission_denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "existing.json")
			if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			client := chartClient(t, deterministicGzip(t, test.raw), test.mutate)
			if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, true); failure == nil || failure.Code != test.wantCode {
				t.Fatalf("failure = %#v", failure)
			}
			if contents, err := os.ReadFile(destination); err != nil || string(contents) != "existing" {
				t.Fatalf("destination = %q, %v", contents, err)
			}
			assertNoDownloadTemps(t, destination)
		})
	}
}

func TestDownloadRoastChartInstallRacePreservesCompetitor(t *testing.T) {
	compressed := deterministicGzip(t, []byte(validChartJSON))
	destination := filepath.Join(t.TempDir(), "race.json")
	client := chartClient(t, compressed, nil)
	client.downloadOps.nativeOperation = func(operation func() error) error {
		if err := os.WriteFile(destination, []byte("competitor"), 0o600); err != nil {
			return err
		}
		return operation()
	}
	if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Message != "Destination already exists; use --force to replace it" {
		t.Fatalf("failure = %#v", failure)
	}
	if contents, err := os.ReadFile(destination); err != nil || string(contents) != "competitor" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	assertNoDownloadTemps(t, destination)
}

func TestDownloadRoastChartNoForcePreservesExistingWithoutNetwork(t *testing.T) {
	var requests atomic.Int32
	client, _ := NewClient("http://127.0.0.1", "secret", time.Second)
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("must not request")
	})
	destination := filepath.Join(t.TempDir(), "chart.json")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, failure := client.DownloadRoastChart(context.Background(), roastUUID, destination, false); failure == nil || failure.Message != "Destination already exists; use --force to replace it" {
		t.Fatalf("failure = %#v", failure)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d", requests.Load())
	}
	if contents, _ := os.ReadFile(destination); string(contents) != "existing" {
		t.Fatalf("destination = %q", contents)
	}
}

func chartClient(t *testing.T, compressed []byte, mutate func(*http.Response)) *Client {
	t.Helper()
	return chartClientWithDetails(t, compressed, []string{validRoastDetailJSON(), validRoastDetailJSON()}, mutate)
}

func chartClientWithDetails(t *testing.T, compressed []byte, details []string, mutate func(*http.Response)) *Client {
	t.Helper()
	client, _ := NewClient("http://127.0.0.1", "chart-secret", time.Minute)
	var detailIndex atomic.Int32
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/roasts/" + roastUUID:
			index := int(detailIndex.Add(1)) - 1
			if index >= len(details) {
				return nil, fmt.Errorf("unexpected detail request %d", index+1)
			}
			return jsonHTTPResponse(http.StatusOK, details[index]), nil
		case "/api/v1/roasts/" + roastUUID + "/chart":
			response := chartHTTPResponse(compressed)
			if mutate != nil {
				mutate(response)
			}
			return response, nil
		default:
			return nil, fmt.Errorf("unexpected request path")
		}
	})
	return client
}

func chartHTTPResponse(compressed []byte) *http.Response {
	sha := chartSHA(compressed)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Content-Encoding", "gzip")
	header.Set("Content-Length", strconv.Itoa(len(compressed)))
	header.Set("ETag", `"`+sha+`"`)
	header.Set("X-Content-SHA256", sha)
	header.Set("X-Checksum-SHA256", sha)
	header.Set("X-Parser-Version", "artisan-4-v1")
	header.Set("X-Chart-Schema-Version", "1")
	return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: int64(len(compressed)), Body: io.NopCloser(bytes.NewReader(compressed))}
}

func deterministicGzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	writer.Header.ModTime = time.Unix(0, 0).UTC()
	writer.Header.OS = 255
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func chartSHA(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func assertMissingFileAndTemps(t *testing.T, destination string) {
	t.Helper()
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists: %v", err)
	}
	assertNoDownloadTemps(t, destination)
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(buffer []byte) (int, error) { return write(buffer) }

func httptestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}
