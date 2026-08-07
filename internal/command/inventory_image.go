package command

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

const imageAddUsage = `Usage: artisan inventory image add [OPTIONS] LOT_ID FILE...

Per-image metadata uses an explicit zero-based file INDEX:
  --caption INDEX=TEXT    caption for one file (repeatable)
  --alt-text INDEX=TEXT   alt text for one file (repeatable)
  --cover INDEX           mark one file as the cover
`

const lotCreateImageUsage = `Usage: artisan inventory lot create [OPTIONS]

Lot fields:
  --name TEXT                         lot name (required unless --from-json)
  --origin TEXT                       origin
  --producer TEXT                     producer
  --supplier TEXT                     supplier
  --external-reference TEXT           external reference
  --received-date YYYY-MM-DD           received date
  --crop-year YEAR                    crop year
  --varietal TEXT                     varietal (repeatable)
  --sca-score SCORE                   SCA score
  --processing-method TEXT            processing method
  --processing-detail TEXT            processing detail
  --altitude-min-metres METRES        minimum altitude
  --altitude-max-metres METRES        maximum altitude
  --notes TEXT                        notes

Opening inventory:
  --opening-grams GRAMS               opening grams
  --opening-reason TEXT               opening reason
  --opening-reference TEXT            opening reference

Input and replay:
  --from-json FILE|-                  strict request JSON (lot fields and image metadata)
  --idempotency-key KEY               advanced idempotency key

Images are declared in order with repeatable flags. Metadata uses an explicit zero-based declaration INDEX:
  --image FILE                        JPEG/PNG image file (repeatable, maximum eight)
  --image-caption INDEX=TEXT          caption for one image (repeatable)
  --image-alt-text INDEX=TEXT         alt text for one image (repeatable)
  --image-cover INDEX                 mark one image as the cover
`

type indexedImageText struct {
	index int
	text  string
}

type indexedImageTextFlag []indexedImageText

func (values *indexedImageTextFlag) String() string { return "" }
func (values *indexedImageTextFlag) Set(value string) error {
	indexText, text, ok := strings.Cut(value, "=")
	if !ok || indexText == "" {
		return errors.New("metadata must use INDEX=TEXT")
	}
	index, err := strconv.Atoi(indexText)
	if err != nil || strconv.Itoa(index) != indexText || index < 0 || index >= api.MaxInventoryImages {
		return errors.New("metadata index is invalid")
	}
	*values = append(*values, indexedImageText{index: index, text: text})
	return nil
}

type singleImageIndexFlag struct {
	index int
	set   bool
}

func (value *singleImageIndexFlag) String() string {
	if !value.set {
		return ""
	}
	return strconv.Itoa(value.index)
}
func (value *singleImageIndexFlag) Set(raw string) error {
	if value.set {
		return errors.New("cover index may be supplied only once")
	}
	index, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(index) != raw || index < 0 || index >= api.MaxInventoryImages {
		return errors.New("cover index is invalid")
	}
	value.index = index
	value.set = true
	return nil
}

type imageMetadataFlags struct {
	captions indexedImageTextFlag
	altTexts indexedImageTextFlag
	cover    singleImageIndexFlag
}

func addImageMetadataFlags(flags *flag.FlagSet, prefix string, metadata *imageMetadataFlags) {
	flags.Var(&metadata.captions, prefix+"caption", "per-image caption as zero-based INDEX=TEXT (repeatable)")
	flags.Var(&metadata.altTexts, prefix+"alt-text", "per-image alt text as zero-based INDEX=TEXT (repeatable)")
	flags.Var(&metadata.cover, prefix+"cover", "zero-based cover image INDEX")
}

func imageManifestFromFlags(count int, metadata imageMetadataFlags) ([]api.ImageUploadManifest, *output.Error) {
	if count == 0 || count > api.MaxInventoryImages {
		return nil, &output.Error{ExitCode: 2, Code: "invalid_images", Message: "Image upload requires between one and eight files"}
	}
	images := make([]api.ImageUploadManifest, count)
	for index := range images {
		images[index].UploadIndex = int64(index)
	}
	captions := make(map[int]bool)
	for _, declaration := range metadata.captions {
		if declaration.index >= count || captions[declaration.index] {
			return nil, &output.Error{ExitCode: 2, Code: "invalid_image_metadata", Message: "Each caption index must identify one file exactly once"}
		}
		captions[declaration.index] = true
		text := declaration.text
		images[declaration.index].Caption = &text
	}
	altTexts := make(map[int]bool)
	for _, declaration := range metadata.altTexts {
		if declaration.index >= count || altTexts[declaration.index] {
			return nil, &output.Error{ExitCode: 2, Code: "invalid_image_metadata", Message: "Each alt-text index must identify one file exactly once"}
		}
		altTexts[declaration.index] = true
		text := declaration.text
		images[declaration.index].AltText = &text
	}
	if metadata.cover.set {
		if metadata.cover.index >= count {
			return nil, &output.Error{ExitCode: 2, Code: "invalid_image_metadata", Message: "Cover index must identify an uploaded file"}
		}
		images[metadata.cover.index].IsCover = true
	}
	return images, nil
}

func runInventoryImage(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "An inventory image command is required")
	}
	if (args[0] == "--help" || args[0] == "-h") && len(args) == 1 {
		return writeCommandHelp(runtime, jsonMode, "Usage: artisan inventory image add|update|reorder|delete|download\n")
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		switch args[0] {
		case "add":
			return writeCommandHelp(runtime, jsonMode, imageAddUsage)
		case "update":
			return writeCommandHelp(runtime, jsonMode, "Usage: artisan inventory image update [OPTIONS] LOT_ID IMAGE_ID\n")
		case "reorder":
			return writeCommandHelp(runtime, jsonMode, "Usage: artisan inventory image reorder [OPTIONS] LOT_ID IMAGE_ID...\n")
		case "delete":
			return writeCommandHelp(runtime, jsonMode, "Usage: artisan inventory image delete [OPTIONS] LOT_ID IMAGE_ID\n")
		case "download":
			return writeCommandHelp(runtime, jsonMode, "Usage: artisan inventory image download [--variant display|thumbnail] [--force] LOT_ID IMAGE_ID DESTINATION\n")
		}
	}
	switch args[0] {
	case "add":
		return runInventoryImageAdd(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "update":
		return runInventoryImageUpdate(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "reorder":
		return runInventoryImageReorder(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "delete":
		return runInventoryImageDelete(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "download":
		return runInventoryImageDownload(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory image command")
	}
}

func runInventoryImageAdd(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory image add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var metadata imageMetadataFlags
	addImageMetadataFlags(flags, "", &metadata)
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) < 2 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory image add option; use image add [OPTIONS] LOT_ID FILE...")
	}
	lotID := flags.Args()[0]
	paths := append([]string(nil), flags.Args()[1:]...)
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	images, failure := imageManifestFromFlags(len(paths), metadata)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if failure = api.ValidateImageUploadFilesContext(ctx, paths); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	visited := visitedFlagNames(flags)
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.AddInventoryImages(ctx, lotID, images, paths, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryImageUpdate(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory image update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	caption := flags.String("caption", "", "caption")
	altText := flags.String("alt-text", "", "alt text")
	clearCaption := flags.Bool("clear-caption", false, "clear caption")
	clearAltText := flags.Bool("clear-alt-text", false, "clear alt text")
	cover := flags.Bool("cover", false, "cover state")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 2 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory image update option")
	}
	lotID, imageID := flags.Args()[0], flags.Args()[1]
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if _, failure := api.NormalizeInventoryUUID(imageID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	visited := visitedFlagNames(flags)
	if (visited["caption"] && *clearCaption) || (visited["alt-text"] && *clearAltText) {
		return inventoryUsageFailure(runtime, jsonMode, "Image metadata cannot be set and cleared together")
	}
	fields := make(map[string]any)
	if visited["caption"] {
		fields["caption"] = *caption
	} else if *clearCaption {
		fields["caption"] = nil
	}
	if visited["alt-text"] {
		fields["alt_text"] = *altText
	} else if *clearAltText {
		fields["alt_text"] = nil
	}
	if visited["cover"] {
		fields["is_cover"] = *cover
	}
	patch, failure := api.NewInventoryImagePatch(fields)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.PatchInventoryImage(ctx, lotID, imageID, patch, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryImageReorder(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory image reorder", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) < 2 {
		return inventoryUsageFailure(runtime, jsonMode, "inventory image reorder requires LOT_ID and the complete IMAGE_ID list")
	}
	lotID := flags.Args()[0]
	imageIDs := append([]string(nil), flags.Args()[1:]...)
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if len(imageIDs) > api.MaxInventoryImages {
		return inventoryUsageFailure(runtime, jsonMode, "Image reorder accepts at most eight image IDs")
	}
	seen := make(map[string]bool, len(imageIDs))
	for _, rawImageID := range imageIDs {
		imageID, failure := api.NormalizeInventoryUUID(rawImageID)
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		if seen[imageID] {
			return inventoryUsageFailure(runtime, jsonMode, "Image reorder must not contain duplicate IDs")
		}
		seen[imageID] = true
	}
	visited := visitedFlagNames(flags)
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.ReorderInventoryImages(ctx, lotID, imageIDs, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryImageDelete(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory image delete", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	yes := flags.Bool("yes", false, "skip interactive confirmation")
	idempotencyKey := flags.String("idempotency-key", "", "advanced idempotency key")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 2 {
		return inventoryUsageFailure(runtime, jsonMode, "inventory image delete requires LOT_ID and IMAGE_ID")
	}
	lotID, imageID := flags.Args()[0], flags.Args()[1]
	canonicalLotID, failure := api.NormalizeInventoryUUID(lotID)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	canonicalImageID, failure := api.NormalizeInventoryUUID(imageID)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	visited := visitedFlagNames(flags)
	if visited["idempotency-key"] {
		if err := api.ValidateIdempotencyKey(*idempotencyKey); err != nil {
			return inventoryUsageFailure(runtime, jsonMode, "Idempotency key is invalid")
		}
	}
	approved, code := confirmMutation(runtime, jsonMode, *yes, "Delete image "+canonicalImageID+" from lot "+canonicalLotID+"?")
	if !approved {
		return code
	}
	key, failure := mutationKey(*idempotencyKey, visited["idempotency-key"])
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, apiFailure := client.DeleteInventoryImage(ctx, lotID, imageID, key)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryImageDownload(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory image download", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	variant := flags.String("variant", "display", "display or thumbnail")
	force := flags.Bool("force", false, "replace an existing destination")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 3 {
		return inventoryUsageFailure(runtime, jsonMode, "inventory image download requires LOT_ID IMAGE_ID DESTINATION")
	}
	lotID, imageID, destination := flags.Args()[0], flags.Args()[1], flags.Args()[2]
	if *variant != "display" && *variant != "thumbnail" {
		return inventoryUsageFailure(runtime, jsonMode, "Image variant must be display or thumbnail")
	}
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if _, failure := api.NormalizeInventoryUUID(imageID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	result, apiFailure := client.DownloadInventoryImage(ctx, lotID, imageID, *variant, destination, *force)
	if apiFailure != nil {
		return writeFailure(runtime, jsonMode, *apiFailure)
	}
	return writeInventorySuccess(runtime, jsonMode, result, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "Downloaded %s image to %s (%d bytes)\n", result.Variant, output.EscapeVisible(result.Path), result.Bytes)
		return err
	})
}

func writeCommandHelp(runtime Runtime, jsonMode bool, text string) int {
	if err := output.WriteSuccess(runtime.Out, jsonMode, map[string]string{"usage": text}, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	}); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}
