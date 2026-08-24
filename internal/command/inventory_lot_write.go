package command

import (
	"context"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/confirm"
	"github.com/fr3akX/artisan-cli/internal/output"
)

const maxMutationJSONBytes = 1 << 20

type repeatedStringFlag []string

func (values *repeatedStringFlag) String() string { return strings.Join(*values, ",") }
func (values *repeatedStringFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type lotFieldFlags struct {
	name, origin, producer, supplier, externalReference, pricePerKgEUR string
	receivedDate, scaScore, processingMethod, processingDetail         string
	description, notes                                                 string
	cropYear, altitudeMin, altitudeMax                                 int64
	varietals                                                          repeatedStringFlag
}

func addLotFieldFlags(flags *flag.FlagSet, values *lotFieldFlags) {
	flags.StringVar(&values.name, "name", "", "lot name")
	flags.StringVar(&values.origin, "origin", "", "origin")
	flags.StringVar(&values.producer, "producer", "", "producer")
	flags.StringVar(&values.supplier, "supplier", "", "supplier")
	flags.StringVar(&values.externalReference, "external-reference", "", "external reference")
	flags.StringVar(&values.receivedDate, "received-date", "", "received date")
	flags.Int64Var(&values.cropYear, "crop-year", 0, "crop year")
	flags.StringVar(&values.pricePerKgEUR, "price-per-kg-eur", "", "EUR price per kilogram")
	flags.Var(&values.varietals, "varietal", "varietal (repeatable)")
	flags.StringVar(&values.scaScore, "sca-score", "", "SCA score")
	flags.StringVar(&values.processingMethod, "processing-method", "", "processing method")
	flags.StringVar(&values.processingDetail, "processing-detail", "", "processing detail")
	flags.Int64Var(&values.altitudeMin, "altitude-min-metres", 0, "minimum altitude")
	flags.Int64Var(&values.altitudeMax, "altitude-max-metres", 0, "maximum altitude")
	flags.StringVar(&values.description, "description", "", "public description")
	flags.StringVar(&values.notes, "notes", "", "notes")
}

func runInventoryLotCreate(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory lot create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var values lotFieldFlags
	addLotFieldFlags(flags, &values)
	var imagePaths repeatedStringFlag
	var imageMetadata imageMetadataFlags
	flags.Var(&imagePaths, "image", "JPEG/PNG image file (repeatable)")
	addImageMetadataFlags(flags, "image-", &imageMetadata)
	openingGrams := flags.Int64("opening-grams", 0, "opening grams")
	openingReason := flags.String("opening-reason", "", "opening reason")
	openingReference := flags.String("opening-reference", "", "opening reference")
	fromJSON := flags.String("from-json", "", "strict request JSON file or -")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory lot create option")
	}
	visited := visitedFlagNames(flags)
	if visited["from-json"] && hasAnyOther(visited, "from-json", "idempotency-key", "image") {
		return inventoryUsageFailure(runtime, jsonMode, "--from-json cannot be combined with lot field or image metadata flags")
	}
	if visited["from-json"] && *fromJSON == "" {
		return inventoryUsageFailure(runtime, jsonMode, "--from-json requires a file path or -")
	}
	var manifest api.BeanLotCreateManifest
	if visited["from-json"] {
		contents, failure := readMutationJSON(runtime.In, *fromJSON)
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		manifest, failure = api.DecodeBeanLotCreateManifest(contents)
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		if len(manifest.Images) != len(imagePaths) {
			return inventoryUsageFailure(runtime, jsonMode, "--from-json image metadata must match repeated --image files")
		}
	} else {
		var fieldFailure *output.Error
		manifest, fieldFailure = createManifestFromFlags(values, visited, *openingGrams, *openingReason, *openingReference)
		if fieldFailure != nil {
			return writeFailure(runtime, jsonMode, *fieldFailure)
		}
		if len(imagePaths) != 0 || visited["image-caption"] || visited["image-alt-text"] || visited["image-cover"] {
			images, imageFailure := imageManifestFromFlags(len(imagePaths), imageMetadata)
			if imageFailure != nil {
				return writeFailure(runtime, jsonMode, *imageFailure)
			}
			manifest.Images = images
		}
		if failure := api.ValidateBeanLotCreateManifest(manifest); failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
	}
	if len(imagePaths) != 0 {
		if failure := api.ValidateImageUploadFilesContext(ctx, imagePaths); failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.CreateBeanLotWithImages(ctx, manifest, imagePaths, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeAPISuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryLotUpdate(ctx context.Context, lotID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	flags := flag.NewFlagSet("artisan inventory lot update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var values lotFieldFlags
	addLotFieldFlags(flags, &values)
	var clears repeatedStringFlag
	flags.Var(&clears, "clear", "nullable field to clear (repeatable)")
	fromJSON := flags.String("from-json", "", "strict request JSON file or -")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory lot update option")
	}
	visited := visitedFlagNames(flags)
	if visited["from-json"] && hasAnyOther(visited, "from-json", "idempotency-key") {
		return inventoryUsageFailure(runtime, jsonMode, "--from-json cannot be combined with lot field flags")
	}
	if visited["from-json"] && *fromJSON == "" {
		return inventoryUsageFailure(runtime, jsonMode, "--from-json requires a file path or -")
	}
	var patch api.BeanLotPatch
	var failure *output.Error
	if visited["from-json"] {
		contents, readFailure := readMutationJSON(runtime.In, *fromJSON)
		if readFailure != nil {
			return writeFailure(runtime, jsonMode, *readFailure)
		}
		patch, failure = api.DecodeBeanLotPatch(contents)
		if failure == nil && patch.HasField("state") {
			failure = &output.Error{ExitCode: 2, Code: "invalid_patch_field", Message: "Use lot archive or restore to change state"}
		}
	} else {
		fields, fieldFailure := patchFieldsFromFlags(values, clears, visited)
		if fieldFailure != nil {
			return writeFailure(runtime, jsonMode, *fieldFailure)
		}
		patch, failure = api.NewBeanLotPatch(fields)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.PatchBeanLot(ctx, lotID, patch, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeAPISuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryLotState(ctx context.Context, state, lotID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	canonicalLotID, failure := api.NormalizeInventoryUUID(lotID)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	flags := flag.NewFlagSet("artisan inventory lot "+state, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	yes := false
	if state == "archive" {
		flags.BoolVar(&yes, "yes", false, "skip interactive confirmation")
	}
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory lot "+state+" option")
	}
	visited := visitedFlagNames(flags)
	if visited["idempotency-key"] {
		if err := api.ValidateIdempotencyKey(*idempotencyKey); err != nil {
			return inventoryUsageFailure(runtime, jsonMode, "Idempotency key is invalid")
		}
	}
	if state == "archive" {
		approved, code := confirmMutation(runtime, jsonMode, yes, "Archive lot "+canonicalLotID+"?")
		if !approved {
			return code
		}
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	patchState := "active"
	if state == "archive" {
		patchState = "archived"
	}
	patch, _ := api.NewBeanLotPatch(map[string]any{"state": patchState})
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.PatchBeanLot(ctx, canonicalLotID, patch, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeAPISuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func createManifestFromFlags(values lotFieldFlags, visited map[string]bool, openingGrams int64, openingReason, openingReference string) (api.BeanLotCreateManifest, *output.Error) {
	fields := api.BeanLotFields{Name: values.name, Varietals: append([]string(nil), values.varietals...)}
	if fields.Varietals == nil {
		fields.Varietals = []string{}
	}
	setOptionalLotFields(&fields, values, visited)
	if visited["price-per-kg-eur"] {
		price, failure := parsePricePerKgEUR(values.pricePerKgEUR)
		if failure != nil {
			return api.BeanLotCreateManifest{}, failure
		}
		fields.PricePerKgEURCents = &price
	}
	manifest := api.BeanLotCreateManifest{Fields: fields, OpeningGrams: openingGrams, Images: []api.ImageUploadManifest{}}
	if visited["opening-reason"] {
		manifest.OpeningReason = &openingReason
	}
	if visited["opening-reference"] {
		manifest.OpeningReference = &openingReference
	}
	return manifest, nil
}

func setOptionalLotFields(fields *api.BeanLotFields, values lotFieldFlags, visited map[string]bool) {
	if visited["origin"] {
		fields.Origin = &values.origin
	}
	if visited["producer"] {
		fields.Producer = &values.producer
	}
	if visited["supplier"] {
		fields.Supplier = &values.supplier
	}
	if visited["external-reference"] {
		fields.ExternalReference = &values.externalReference
	}
	if visited["received-date"] {
		fields.ReceivedDate = &values.receivedDate
	}
	if visited["crop-year"] {
		fields.CropYear = &values.cropYear
	}
	if visited["sca-score"] {
		fields.SCAScore = &values.scaScore
	}
	if visited["processing-method"] {
		fields.ProcessingMethod = &values.processingMethod
	}
	if visited["processing-detail"] {
		fields.ProcessingDetail = &values.processingDetail
	}
	if visited["altitude-min-metres"] {
		fields.AltitudeMinMetres = &values.altitudeMin
	}
	if visited["altitude-max-metres"] {
		fields.AltitudeMaxMetres = &values.altitudeMax
	}
	if visited["description"] {
		fields.Description = &values.description
	}
	if visited["notes"] {
		fields.Notes = &values.notes
	}
}

func patchFieldsFromFlags(values lotFieldFlags, clears []string, visited map[string]bool) (map[string]any, *output.Error) {
	fields := make(map[string]any)
	stringValues := map[string]string{"name": values.name, "origin": values.origin, "producer": values.producer, "supplier": values.supplier, "external-reference": values.externalReference, "received-date": values.receivedDate, "sca-score": values.scaScore, "processing-method": values.processingMethod, "processing-detail": values.processingDetail, "description": values.description, "notes": values.notes}
	for flagName, value := range stringValues {
		if visited[flagName] {
			fields[strings.ReplaceAll(flagName, "-", "_")] = value
		}
	}
	for flagName, value := range map[string]int64{"crop-year": values.cropYear, "altitude-min-metres": values.altitudeMin, "altitude-max-metres": values.altitudeMax} {
		if visited[flagName] {
			fields[strings.ReplaceAll(flagName, "-", "_")] = value
		}
	}
	if visited["varietal"] {
		fields["varietals"] = append([]string(nil), values.varietals...)
	}
	if visited["price-per-kg-eur"] {
		price, failure := parsePricePerKgEUR(values.pricePerKgEUR)
		if failure != nil {
			return nil, failure
		}
		fields["price_per_kg_eur_cents"] = price
	}
	clearable := map[string]string{
		"origin": "origin", "producer": "producer", "supplier": "supplier", "description": "description", "notes": "notes", "varietals": "varietals",
		"external-reference": "external_reference", "external_reference": "external_reference",
		"received-date": "received_date", "received_date": "received_date",
		"crop-year": "crop_year", "crop_year": "crop_year",
		"price-per-kg-eur": "price_per_kg_eur_cents", "price_per_kg_eur": "price_per_kg_eur_cents",
		"sca-score": "sca_score", "sca_score": "sca_score",
		"processing-method": "processing_method", "processing_method": "processing_method",
		"processing-detail": "processing_detail", "processing_detail": "processing_detail",
		"altitude-min-metres": "altitude_min_metres", "altitude_min_metres": "altitude_min_metres",
		"altitude-max-metres": "altitude_max_metres", "altitude_max_metres": "altitude_max_metres",
	}
	for _, clear := range clears {
		field, ok := clearable[clear]
		if !ok {
			return nil, &output.Error{ExitCode: 2, Code: "invalid_clear_field", Message: "Unknown or non-nullable --clear field"}
		}
		if _, already := fields[field]; already {
			return nil, &output.Error{ExitCode: 2, Code: "conflicting_field", Message: "A field cannot be set and cleared together"}
		}
		fields[field] = nil
	}
	return fields, nil
}

func visitedFlagNames(flags *flag.FlagSet) map[string]bool {
	visited := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	return visited
}

func hasAnyOther(visited map[string]bool, exceptions ...string) bool {
	excluded := make(map[string]bool, len(exceptions))
	for _, name := range exceptions {
		excluded[name] = true
	}
	for name := range visited {
		if !excluded[name] {
			return true
		}
	}
	return false
}

func readMutationJSON(stdin io.Reader, source string) ([]byte, *output.Error) {
	var reader io.Reader
	var file *os.File
	if source == "-" {
		reader = stdin
	} else {
		opened, err := os.Open(source)
		if err != nil {
			return nil, &output.Error{ExitCode: 2, Code: "json_read_failed", Message: "Unable to read mutation JSON"}
		}
		file = opened
		defer file.Close()
		reader = file
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maxMutationJSONBytes+1))
	if err != nil || len(contents) > maxMutationJSONBytes {
		return nil, &output.Error{ExitCode: 2, Code: "json_read_failed", Message: "Mutation JSON is unreadable or exceeds the 1 MiB limit"}
	}
	return contents, nil
}

func mutationKey(provided string, supplied bool) (string, *output.Error) {
	if supplied {
		if err := api.ValidateIdempotencyKey(provided); err != nil {
			return "", &output.Error{ExitCode: 2, Code: "invalid_idempotency_key", Message: "Idempotency key is invalid"}
		}
		return provided, nil
	}
	key, err := api.NewIdempotencyKey()
	if err != nil {
		return "", &output.Error{ExitCode: 1, Code: "idempotency_key_failed", Message: "Unable to generate an idempotency key"}
	}
	return key, nil
}

func confirmMutation(runtime Runtime, jsonMode, yes bool, prompt string) (bool, int) {
	terminal := runtime.IsTerminal != nil && runtime.IsTerminal(0)
	approved, err := confirm.Ask(runtime.In, runtime.Err, terminal, yes, prompt)
	if err != nil {
		return false, writeFailure(runtime, jsonMode, output.Error{ExitCode: 10, Code: "confirmation_declined", Message: "Confirmation was not provided"})
	}
	if approved {
		return true, 0
	}
	code := "confirmation_declined"
	message := "Confirmation declined"
	if !terminal {
		code = "confirmation_required"
		message = "Noninteractive mutation requires --yes"
	}
	return false, writeFailure(runtime, jsonMode, output.Error{ExitCode: 10, Code: code, Message: message})
}
