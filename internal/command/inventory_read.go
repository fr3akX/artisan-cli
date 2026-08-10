package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

func runInventoryLot(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "An inventory lot command is required")
	}
	switch args[0] {
	case "list":
		return runInventoryLotList(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "show":
		if len(args) != 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory lot show requires one LOT_ID")
		}
		return runInventoryLotShow(ctx, args[1], runtime, jsonMode, serverOverride, timeout)
	case "create":
		return runInventoryLotCreate(ctx, args[1:], runtime, jsonMode, serverOverride, timeout)
	case "update":
		if len(args) < 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory lot update requires one LOT_ID")
		}
		return runInventoryLotUpdate(ctx, args[1], args[2:], runtime, jsonMode, serverOverride, timeout)
	case "archive", "restore":
		if len(args) < 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory lot "+args[0]+" requires one LOT_ID")
		}
		return runInventoryLotState(ctx, args[0], args[1], args[2:], runtime, jsonMode, serverOverride, timeout)
	case "ledger", "reservations", "conflicts":
		return runInventoryLotHistory(ctx, args[0], args[1:], runtime, jsonMode, serverOverride, timeout)
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory lot command")
	}
}

func runInventoryConflict(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "An inventory conflict command is required")
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("artisan inventory conflict list", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		lotID := flags.String("lot", "", "lot UUID")
		limit := flags.Int("limit", 0, "page size")
		cursor := flags.String("cursor", "", "continuation cursor")
		all := flags.Bool("all", false, "read all pages")
		if err := flags.Parse(args[1:]); err != nil || len(flags.Args()) != 0 {
			return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory conflict list option")
		}
		if *lotID == "" {
			return inventoryUsageFailure(runtime, jsonMode, "inventory conflict list requires --lot LOT_ID")
		}
		return executeInventoryConflicts(ctx, *lotID, api.PageOptions{Limit: *limit, Cursor: *cursor}, *all, runtime, jsonMode, serverOverride, timeout)
	case "show":
		if len(args) != 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory conflict show requires one CONFLICT_ID")
		}
		if _, failure := api.NormalizeInventoryUUID(args[1]); failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
		if client == nil {
			return code
		}
		conflict, failure := client.InventoryConflict(ctx, args[1])
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		return writeInventorySuccess(runtime, jsonMode, conflict, func(w io.Writer) error { return writeConflictDetail(w, conflict) })
	case "resolve":
		if len(args) < 2 {
			return inventoryUsageFailure(runtime, jsonMode, "inventory conflict resolve requires one CONFLICT_ID")
		}
		return runInventoryConflictResolve(ctx, args[1], args[2:], runtime, jsonMode, serverOverride, timeout)
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory conflict command")
	}
}

func runInventoryLotList(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory lot list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := api.LotListOptions{}
	flags.IntVar(&options.Limit, "limit", 0, "page size")
	flags.StringVar(&options.Cursor, "cursor", "", "continuation cursor")
	flags.StringVar(&options.Query, "q", "", "search text")
	flags.StringVar(&options.State, "state", "", "lot state")
	flags.StringVar(&options.Availability, "availability", "", "availability filter")
	flags.StringVar(&options.Conflict, "conflict", "", "conflict filter")
	flags.StringVar(&options.RoastUUID, "roast-uuid", "", "roast UUID")
	all := flags.Bool("all", false, "read all pages")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory lot list option")
	}
	if failure := api.ValidateLotListOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var page api.BeanLotPage
	var failure *output.Error
	if *all {
		page, failure = client.ListAllBeanLots(ctx, options)
	} else {
		page, failure = client.ListBeanLots(ctx, options)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeInventorySuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeLotTable(w, page) })
}

func runInventoryTotals(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan inventory totals", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := api.InventoryTotalsOptions{}
	flags.StringVar(&options.Query, "q", "", "search text")
	flags.StringVar(&options.State, "state", "", "lot state")
	flags.StringVar(&options.Availability, "availability", "", "availability filter")
	flags.StringVar(&options.Conflict, "conflict", "", "conflict filter")
	flags.StringVar(&options.RoastUUID, "roast-uuid", "", "roast UUID")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory totals option")
	}
	if failure := api.ValidateInventoryTotalsOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	totals, failure := client.InventoryTotals(ctx, options)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeInventorySuccess(runtime, jsonMode, totals, func(w io.Writer) error { return writeInventoryTotals(w, totals) })
}

func runInventoryLotShow(ctx context.Context, lotID string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	lot, failure := client.BeanLot(ctx, lotID)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeInventorySuccess(runtime, jsonMode, lot, func(w io.Writer) error { return writeLotDetail(w, lot) })
}

func runInventoryLotHistory(ctx context.Context, kind string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if len(args) == 0 {
		return inventoryUsageFailure(runtime, jsonMode, "inventory lot "+kind+" requires one LOT_ID")
	}
	lotID := args[0]
	flags := flag.NewFlagSet("artisan inventory lot "+kind, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limit := flags.Int("limit", 0, "page size")
	cursor := flags.String("cursor", "", "continuation cursor")
	all := flags.Bool("all", false, "read all pages")
	if err := flags.Parse(args[1:]); err != nil || len(flags.Args()) != 0 {
		return inventoryUsageFailure(runtime, jsonMode, "Invalid inventory lot "+kind+" option")
	}
	options := api.PageOptions{Limit: *limit, Cursor: *cursor}
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if failure := api.ValidatePageOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if kind == "conflicts" {
		return executeInventoryConflicts(ctx, lotID, options, *all, runtime, jsonMode, serverOverride, timeout)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	switch kind {
	case "ledger":
		var page api.InventoryLedgerEntryPage
		var failure *output.Error
		if *all {
			page, failure = client.AllBeanLotLedger(ctx, lotID, options)
		} else {
			page, failure = client.BeanLotLedger(ctx, lotID, options)
		}
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		return writeInventorySuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeLedgerTable(w, page) })
	case "reservations":
		var page api.InventoryReservationPage
		var failure *output.Error
		if *all {
			page, failure = client.AllBeanLotReservations(ctx, lotID, options)
		} else {
			page, failure = client.BeanLotReservations(ctx, lotID, options)
		}
		if failure != nil {
			return writeFailure(runtime, jsonMode, *failure)
		}
		return writeInventorySuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeReservationTable(w, page) })
	default:
		return inventoryUsageFailure(runtime, jsonMode, "Unknown inventory history command")
	}
}

func executeInventoryConflicts(ctx context.Context, lotID string, options api.PageOptions, all bool, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeInventoryUUID(lotID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if failure := api.ValidatePageOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := inventoryReadClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var page api.InventoryConflictPage
	var failure *output.Error
	if all {
		page, failure = client.AllBeanLotConflicts(ctx, lotID, options)
	} else {
		page, failure = client.BeanLotConflicts(ctx, lotID, options)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeInventorySuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeConflictTable(w, page) })
}

func inventoryReadClient(ctx context.Context, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) (*api.Client, int) {
	release, lockFailure := acquireAuthStateLock(ctx, runtime.ConfigDir)
	if lockFailure != nil {
		return nil, writeFailure(runtime, jsonMode, *lockFailure)
	}
	defer release()
	if err := recoverLoginTransaction(runtime.ConfigDir); err != nil {
		return nil, writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}
	values, err := loadEffectiveConfiguration(runtime, serverOverride)
	if err != nil {
		return nil, writeFailure(runtime, jsonMode, configurationLoadFailure(err))
	}
	client, err := api.NewClient(values.ServerURL, values.Token, timeout)
	if err != nil {
		return nil, writeFailure(runtime, jsonMode, clientConfigurationFailure(err))
	}
	return client, 0
}

func writeInventorySuccess(runtime Runtime, jsonMode bool, data any, human func(io.Writer) error) int {
	if err := output.WriteSuccess(runtime.Out, jsonMode, data, human); err != nil {
		return reportWriteError(runtime.Err, err)
	}
	return 0
}

func writeDesktopLotTable(w io.Writer, page api.DesktopBeanLotPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, lot := range page.Items {
		rows = append(rows, []string{lot.LotID, lot.Name, optionalString(lot.Origin), strings.Join(lot.Varietals, ", "), optionalString(lot.ProcessingMethod), optionalInt(lot.CropYear), strconv.FormatInt(lot.OnHandGrams, 10), strconv.FormatInt(lot.ReservedGrams, 10), strconv.FormatInt(lot.AvailableGrams, 10), strconv.FormatInt(lot.UnresolvedConflictCount, 10)})
	}
	if err := output.WriteTable(w, []string{"LOT ID", "NAME", "ORIGIN", "VARIETALS", "PROCESSING METHOD", "CROP YEAR", "ON HAND GRAMS", "RESERVED GRAMS", "AVAILABLE GRAMS", "CONFLICTS"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeLotTable(w io.Writer, page api.BeanLotPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, lot := range page.Items {
		rows = append(rows, []string{lot.LotID, lot.Name, lot.State, optionalEURPerKg(lot.PricePerKgEURCents), strconv.FormatInt(lot.OnHandGrams, 10), strconv.FormatInt(lot.ReservedGrams, 10), strconv.FormatInt(lot.AvailableGrams, 10), strconv.FormatInt(lot.UnresolvedConflictCount, 10)})
	}
	if err := output.WriteTable(w, []string{"LOT ID", "NAME", "STATE", "PRICE/KG", "ON HAND GRAMS", "RESERVED GRAMS", "AVAILABLE GRAMS", "CONFLICTS"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeLedgerTable(w io.Writer, page api.InventoryLedgerEntryPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, entry := range page.Items {
		rows = append(rows, []string{entry.EntryID, entry.LotID, optionalString(entry.RoastUUID), optionalString(entry.ReservationID), entry.Operation, strconv.FormatInt(entry.OnHandDelta, 10), strconv.FormatInt(entry.ReservedDelta, 10), strconv.FormatInt(entry.ResultingOnHandGrams, 10), strconv.FormatInt(entry.ResultingReservedGrams, 10), strconv.FormatInt(entry.ResultingAvailableGrams, 10), entry.ActorKind, entry.OccurredAt})
	}
	if err := output.WriteTable(w, []string{"ENTRY ID", "LOT ID", "ROAST UUID", "RESERVATION ID", "OPERATION", "ON HAND DELTA", "RESERVED DELTA", "ON HAND GRAMS", "RESERVED GRAMS", "AVAILABLE GRAMS", "ACTOR", "OCCURRED AT"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeReservationTable(w io.Writer, page api.InventoryReservationPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, reservation := range page.Items {
		rows = append(rows, []string{reservation.ReservationID, reservation.ClientReservationUUID, reservation.LotID, reservation.RoastUUID, reservation.ClientInstanceUUID, reservation.State, strconv.FormatInt(reservation.PlannedGrams, 10), optionalInt(reservation.ActualGrams), optionalEURCents(reservation.RoastCostEURCents), optionalString(reservation.OpenConflictID), reservation.ReservedAt, optionalString(reservation.CompletedAt), reservation.UpdatedAt})
	}
	if err := output.WriteTable(w, []string{"RESERVATION ID", "CLIENT RESERVATION UUID", "LOT ID", "ROAST UUID", "CLIENT INSTANCE UUID", "STATE", "PLANNED GRAMS", "ACTUAL GRAMS", "ROAST COST", "OPEN CONFLICT ID", "RESERVED AT", "COMPLETED AT", "UPDATED AT"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeConflictTable(w io.Writer, page api.InventoryConflictPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, conflict := range page.Items {
		rows = append(rows, []string{conflict.ConflictID, conflict.LotID, conflict.SourceLedgerEntryID, optionalString(conflict.RoastUUID), optionalString(conflict.ReservationID), conflict.TriggerOperation, strconv.FormatInt(conflict.AvailableGramsSnapshot, 10), conflict.State, optionalString(conflict.ResolutionNote), optionalString(conflict.ResolvedByUserID), optionalString(conflict.ResolvedAt), conflict.CreatedAt})
	}
	if err := output.WriteTable(w, []string{"CONFLICT ID", "LOT ID", "SOURCE ENTRY ID", "ROAST UUID", "RESERVATION ID", "TRIGGER", "AVAILABLE GRAMS", "STATE", "RESOLUTION NOTE", "RESOLVED BY USER ID", "RESOLVED AT", "CREATED AT"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeNextCursor(w io.Writer, cursor *string) error {
	if cursor == nil {
		return nil
	}
	_, err := fmt.Fprintf(w, "Next cursor: %s\n", output.EscapeVisible(*cursor))
	return err
}

func writeLotDetail(w io.Writer, lot api.BeanLotDetail) error {
	fields := []output.DetailField{
		{Label: "Lot ID", Value: lot.LotID}, {Label: "Name", Value: lot.Name}, {Label: "Origin", Value: optionalString(lot.Origin)},
		{Label: "Producer", Value: optionalString(lot.Producer)}, {Label: "Supplier", Value: optionalString(lot.Supplier)}, {Label: "External reference", Value: optionalString(lot.ExternalReference)},
		{Label: "Received date", Value: optionalString(lot.ReceivedDate)}, {Label: "Crop year", Value: optionalInt(lot.CropYear)}, {Label: "Varietals", Value: strings.Join(lot.Varietals, ", ")},
		{Label: "SCA score", Value: optionalString(lot.SCAScore)}, {Label: "Processing method", Value: optionalString(lot.ProcessingMethod)}, {Label: "Processing detail", Value: optionalString(lot.ProcessingDetail)},
		{Label: "Altitude minimum metres", Value: optionalInt(lot.AltitudeMinMetres)}, {Label: "Altitude maximum metres", Value: optionalInt(lot.AltitudeMaxMetres)},
		{Label: "Public description", Value: optionalString(lot.Description)}, {Label: "Notes", Value: optionalString(lot.Notes)},
		{Label: "State", Value: lot.State}, {Label: "Price per kg", Value: optionalEURPerKg(lot.PricePerKgEURCents)}, {Label: "On hand grams", Value: strconv.FormatInt(lot.OnHandGrams, 10)}, {Label: "Reserved grams", Value: strconv.FormatInt(lot.ReservedGrams, 10)},
		{Label: "Available grams", Value: strconv.FormatInt(lot.AvailableGrams, 10)}, {Label: "Unresolved conflicts", Value: strconv.FormatInt(lot.UnresolvedConflictCount, 10)},
		{Label: "Created at", Value: lot.CreatedAt}, {Label: "Updated at", Value: lot.UpdatedAt}, {Label: "Archived at", Value: optionalString(lot.ArchivedAt)},
		{Label: "Images", Value: strconv.Itoa(len(lot.Images))}, {Label: "Cover image ID", Value: coverImageID(lot.CoverImage)}, {Label: "Self link", Value: lot.Links.Self}, {Label: "Ledger link", Value: lot.Links.Ledger}, {Label: "Reservations link", Value: lot.Links.Reservations},
	}
	if err := output.WriteDetails(w, fields); err != nil {
		return err
	}
	if len(lot.Images) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(lot.Images))
	for _, image := range lot.Images {
		rows = append(rows, []string{image.ImageID, strconv.FormatInt(image.Position, 10), strconv.FormatBool(image.IsCover), optionalString(image.Caption), optionalString(image.AltText), strconv.FormatInt(image.DisplayWidth, 10), strconv.FormatInt(image.DisplayHeight, 10), strconv.FormatInt(image.ThumbnailWidth, 10), strconv.FormatInt(image.ThumbnailHeight, 10), image.DisplayURL, image.ThumbnailURL})
	}
	return output.WriteTable(w, []string{"IMAGE ID", "POSITION", "COVER", "CAPTION", "ALT TEXT", "DISPLAY WIDTH", "DISPLAY HEIGHT", "THUMBNAIL WIDTH", "THUMBNAIL HEIGHT", "DISPLAY URL", "THUMBNAIL URL"}, rows)
}

func writeConflictDetail(w io.Writer, conflict api.InventoryConflict) error {
	return output.WriteDetails(w, []output.DetailField{
		{Label: "Conflict ID", Value: conflict.ConflictID}, {Label: "Lot ID", Value: conflict.LotID}, {Label: "Source ledger entry ID", Value: conflict.SourceLedgerEntryID},
		{Label: "Roast UUID", Value: optionalString(conflict.RoastUUID)}, {Label: "Reservation ID", Value: optionalString(conflict.ReservationID)}, {Label: "Trigger operation", Value: conflict.TriggerOperation},
		{Label: "Available grams snapshot", Value: strconv.FormatInt(conflict.AvailableGramsSnapshot, 10)}, {Label: "State", Value: conflict.State}, {Label: "Resolution note", Value: optionalString(conflict.ResolutionNote)},
		{Label: "Resolved by user ID", Value: optionalString(conflict.ResolvedByUserID)}, {Label: "Resolved at", Value: optionalString(conflict.ResolvedAt)}, {Label: "Created at", Value: conflict.CreatedAt},
	})
}

func writeInventoryTotals(w io.Writer, totals api.InventoryTotals) error {
	value := "-"
	if totals.OnHandValueEURCents != nil {
		value = formatSignedEURCents(*totals.OnHandValueEURCents)
	}
	return output.WriteDetails(w, []output.DetailField{
		{Label: "Matching lots", Value: strconv.FormatInt(totals.LotCount, 10)},
		{Label: "On-hand grams", Value: strconv.FormatInt(totals.OnHandGrams, 10)},
		{Label: "Reserved grams", Value: strconv.FormatInt(totals.ReservedGrams, 10)},
		{Label: "Available grams", Value: strconv.FormatInt(totals.AvailableGrams, 10)},
		{Label: "On-hand EUR value", Value: value},
		{Label: "Priced lots", Value: strconv.FormatInt(totals.PricedLotCount, 10)},
		{Label: "Unpriced lots", Value: strconv.FormatInt(totals.UnpricedLotCount, 10)},
	})
}

func optionalEURPerKg(cents *int64) string {
	if cents == nil {
		return "-"
	}
	return formatEURCents(*cents) + "/kg"
}

func coverImageID(value *api.InventoryImage) string {
	if value == nil {
		return "-"
	}
	return value.ImageID
}

func optionalString(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}
func optionalInt(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}
func inventoryUsageError(message string) output.Error {
	return output.Error{ExitCode: usageExitCode, Code: "usage", Message: message}
}
