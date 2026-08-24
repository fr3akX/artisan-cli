package command

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/fr3akX/artisan-cli/internal/api"
	"github.com/fr3akX/artisan-cli/internal/output"
)

func roastUsageError(message string) output.Error {
	return output.Error{ExitCode: usageExitCode, Code: "usage", Message: message}
}

func roastUsageFailure(runtime Runtime, jsonMode bool, message string) int {
	return writeFailure(runtime, jsonMode, roastUsageError(message))
}

func runRoastList(ctx context.Context, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	flags := flag.NewFlagSet("artisan roast list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := api.RoastListOptions{}
	flags.IntVar(&options.Limit, "limit", 0, "page size")
	flags.StringVar(&options.Cursor, "cursor", "", "continuation cursor")
	all := flags.Bool("all", false, "read all pages")
	flags.StringVar(&options.Search, "search", "", "search text")
	flags.StringVar(&options.RoastAtFrom, "roast-at-from", "", "earliest roast time")
	flags.StringVar(&options.RoastAtTo, "roast-at-to", "", "latest roast time")
	flags.StringVar(&options.Machine, "machine", "", "machine text")
	flags.StringVar(&options.State, "state", "", "roast state")
	flags.StringVar(&options.LabelID, "label-id", "", "label UUID")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return roastUsageFailure(runtime, jsonMode, "Invalid roast list option")
	}
	if failure := api.ValidateRoastListOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var page api.RoastPage
	var failure *output.Error
	if *all {
		page, failure = client.ListAllRoasts(ctx, options)
	} else {
		page, failure = client.ListRoasts(ctx, options)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeRoastTable(w, page) })
}

func runRoastShow(ctx context.Context, roastUUID string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	roast, failure := client.Roast(ctx, roastUUID)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, roast, func(w io.Writer) error { return writeRoastDetail(w, roast) })
}

func runRoastRevisions(ctx context.Context, roastUUID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	options, all, code := parseRoastPageArgs(args, runtime, jsonMode, "Invalid roast revisions option")
	if code != 0 {
		return code
	}
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if failure := validateLocalRoastPageOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var page api.RoastRevisionPage
	var failure *output.Error
	if all {
		page, failure = client.AllRoastRevisions(ctx, roastUUID, options)
	} else {
		page, failure = client.RoastRevisions(ctx, roastUUID, options)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeRoastRevisionTable(w, page) })
}

func runRoastComments(ctx context.Context, roastUUID string, args []string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	options, all, code := parseRoastPageArgs(args, runtime, jsonMode, "Invalid roast comment list option")
	if code != 0 {
		return code
	}
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	if failure := validateLocalRoastPageOptions(options); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	var page api.CommentPage
	var failure *output.Error
	if all {
		page, failure = client.AllRoastComments(ctx, roastUUID, options)
	} else {
		page, failure = client.RoastComments(ctx, roastUUID, options)
	}
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, page, func(w io.Writer) error { return writeRoastCommentTable(w, page) })
}

func parseRoastPageArgs(args []string, runtime Runtime, jsonMode bool, message string) (api.PageOptions, bool, int) {
	flags := flag.NewFlagSet("roast page", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := api.PageOptions{}
	flags.IntVar(&options.Limit, "limit", 0, "page size")
	flags.StringVar(&options.Cursor, "cursor", "", "continuation cursor")
	all := flags.Bool("all", false, "read all pages")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 {
		return options, false, roastUsageFailure(runtime, jsonMode, message)
	}
	return options, *all, 0
}

func validateLocalRoastPageOptions(options api.PageOptions) *output.Error {
	if options.Limit < 0 || options.Limit > 100 || len(options.Cursor) > 512 {
		return &output.Error{ExitCode: usageExitCode, Code: "invalid_roast_filter", Message: "Roast filters are invalid"}
	}
	return nil
}

func runRoastChartDownload(ctx context.Context, roastUUID, destination string, force bool, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	result, failure := client.DownloadRoastChart(ctx, roastUUID, destination, force)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, result, func(w io.Writer) error { return writeRoastChartDownload(w, result) })
}

func runRoastProfileDownload(ctx context.Context, roastUUID, rawRevision, destination string, force bool, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	revision, err := strconv.ParseInt(rawRevision, 10, 64)
	if err != nil || revision < 1 || revision > 2_147_483_647 || strconv.FormatInt(revision, 10) != rawRevision {
		return writeFailure(runtime, jsonMode, output.Error{ExitCode: usageExitCode, Code: "invalid_revision_number", Message: "Roast revision number must be between 1 and 2147483647"})
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	result, failure := client.DownloadRoastProfile(ctx, roastUUID, revision, destination, force)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, result, func(w io.Writer) error { return writeRoastProfileDownload(w, result) })
}

func runRoastReviewPost(ctx context.Context, roastUUID, revisionSHA, template, bodyFile string, runtime Runtime, jsonMode bool, serverOverride string, timeout time.Duration) int {
	if _, failure := api.NormalizeRoastUUID(roastUUID); failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	request, failure := api.ReadRoastReviewFile(bodyFile, revisionSHA, template)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	client, code := authenticatedClient(ctx, runtime, jsonMode, serverOverride, timeout)
	if client == nil {
		return code
	}
	result, failure := client.PostRoastReview(ctx, roastUUID, request)
	if failure != nil {
		return writeFailure(runtime, jsonMode, *failure)
	}
	return writeAPISuccess(runtime, jsonMode, result, func(w io.Writer) error { return writeRoastReviewResult(w, result) })
}

func writeRoastTable(w io.Writer, page api.RoastPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, roast := range page.Items {
		rows = append(rows, []string{
			roast.RoastUUID, optionalString(roast.RoastAt), optionalString(roast.Title), roast.State,
			strconv.FormatInt(roast.RevisionCount, 10), optionalString(roast.Machine), optionalFloat64(roast.DurationSeconds),
			optionalFloat64(roast.GreenWeightKG), optionalString(roast.TemperatureUnit), roast.UpdatedAt,
		})
	}
	if err := output.WriteTable(w, []string{"ROAST UUID", "ROAST AT", "TITLE", "STATE", "REVISIONS", "MACHINE", "DURATION SECONDS", "GREEN WEIGHT KG", "TEMPERATURE UNIT", "UPDATED AT"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeRoastDetail(w io.Writer, roast api.RoastDetail) error {
	fields := []output.DetailField{
		{Label: "Roast UUID", Value: roast.RoastUUID}, {Label: "State", Value: roast.State}, {Label: "Roast at", Value: optionalString(roast.RoastAt)},
		{Label: "Title", Value: optionalString(roast.Title)}, {Label: "Batch prefix", Value: optionalString(roast.BatchPrefix)}, {Label: "Batch number", Value: optionalInt(roast.BatchNumber)},
		{Label: "Batch position", Value: optionalInt(roast.BatchPosition)}, {Label: "Operator", Value: optionalString(roast.Operator)}, {Label: "Machine", Value: optionalString(roast.Machine)},
		{Label: "Machine setup", Value: optionalString(roast.MachineSetup)}, {Label: "Temperature unit", Value: optionalString(roast.TemperatureUnit)}, {Label: "Duration seconds", Value: optionalFloat64(roast.DurationSeconds)},
		{Label: "Green weight kg", Value: optionalFloat64(roast.GreenWeightKG)}, {Label: "Roasted weight kg", Value: optionalFloat64(roast.RoastedWeightKG)},
		{Label: "Revision count", Value: strconv.FormatInt(roast.RevisionCount, 10)}, {Label: "Updated at", Value: roast.UpdatedAt},
		{Label: "Current metadata", Value: string(roast.CurrentMetadata)}, {Label: "Self link", Value: roast.Links.Self}, {Label: "Chart link", Value: roast.Links.Chart}, {Label: "Revisions link", Value: roast.Links.Revisions},
	}
	if roast.CurrentRevision != nil {
		fields = append(fields,
			output.DetailField{Label: "Current revision", Value: strconv.FormatInt(roast.CurrentRevision.RevisionNumber, 10)},
			output.DetailField{Label: "Current revision SHA-256", Value: roast.CurrentRevision.SHA256},
			output.DetailField{Label: "Current revision bytes", Value: strconv.FormatInt(roast.CurrentRevision.ByteSize, 10)},
			output.DetailField{Label: "Current parser", Value: roast.CurrentRevision.ParserVersion},
			output.DetailField{Label: "Current parse state", Value: roast.CurrentRevision.ParseState},
			output.DetailField{Label: "Current parse diagnostic code", Value: optionalString(roast.CurrentRevision.ParseDiagnosticCode)},
			output.DetailField{Label: "Current parse diagnostic message", Value: optionalString(roast.CurrentRevision.ParseDiagnosticMessage)},
			output.DetailField{Label: "Current revision uploaded at", Value: roast.CurrentRevision.UploadedAt},
			output.DetailField{Label: "Current revision metadata", Value: string(roast.CurrentRevision.Metadata)},
			output.DetailField{Label: "Current reparse recommended", Value: strconv.FormatBool(roast.CurrentRevision.ReparseRecommended)},
		)
	}
	if err := output.WriteDetails(w, fields); err != nil {
		return err
	}
	if len(roast.Labels) == 0 {
		return nil
	}
	rows := make([][]string, 0, len(roast.Labels))
	for _, label := range roast.Labels {
		rows = append(rows, []string{label.LabelUUID, label.Name, label.Color, strconv.FormatBool(label.Archived)})
	}
	return output.WriteTable(w, []string{"LABEL UUID", "NAME", "COLOR", "ARCHIVED"}, rows)
}

func writeRoastRevisionTable(w io.Writer, page api.RoastRevisionPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, revision := range page.Items {
		rows = append(rows, []string{
			strconv.FormatInt(revision.RevisionNumber, 10), revision.SHA256, strconv.FormatInt(revision.ByteSize, 10), revision.ParserVersion,
			revision.ParseState, optionalString(revision.ParseDiagnosticCode), optionalString(revision.ParseDiagnosticMessage), revision.UploadedAt,
			strconv.FormatBool(revision.ReparseRecommended), string(revision.Metadata),
		})
	}
	if err := output.WriteTable(w, []string{"REVISION", "SHA256", "BYTES", "PARSER", "PARSE STATE", "DIAGNOSTIC CODE", "DIAGNOSTIC MESSAGE", "UPLOADED AT", "REPARSE RECOMMENDED", "METADATA"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeRoastCommentTable(w io.Writer, page api.CommentPage) error {
	rows := make([][]string, 0, len(page.Items))
	for _, comment := range page.Items {
		rows = append(rows, []string{
			comment.CommentUUID, comment.AuthorNickname, optionalString(comment.Body), comment.CreatedAt, optionalString(comment.EditedAt),
			optionalString(comment.DeletedAt), strconv.FormatBool(comment.IsDeleted), strconv.FormatBool(comment.CanEdit), strconv.FormatBool(comment.CanDelete),
		})
	}
	if err := output.WriteTable(w, []string{"COMMENT UUID", "AUTHOR", "BODY", "CREATED AT", "EDITED AT", "DELETED AT", "DELETED", "CAN EDIT", "CAN DELETE"}, rows); err != nil {
		return err
	}
	return writeNextCursor(w, page.NextCursor)
}

func writeRoastChartDownload(w io.Writer, result api.RoastChartDownload) error {
	if _, err := fmt.Fprintf(w, "Downloaded %d bytes to %s\n", result.FileBytes, output.EscapeVisible(result.Path)); err != nil {
		return err
	}
	return output.WriteDetails(w, []output.DetailField{
		{Label: "Roast UUID", Value: result.RoastUUID}, {Label: "Revision", Value: strconv.FormatInt(result.RevisionNumber, 10)},
		{Label: "Revision SHA-256", Value: result.RevisionSHA256}, {Label: "Parser", Value: result.ParserVersion},
		{Label: "Chart schema version", Value: strconv.FormatInt(result.ChartSchemaVersion, 10)}, {Label: "Compressed bytes", Value: strconv.FormatInt(result.CompressedBytes, 10)},
		{Label: "Compressed SHA-256", Value: result.CompressedSHA256}, {Label: "File SHA-256", Value: result.FileSHA256},
	})
}

func writeRoastProfileDownload(w io.Writer, result api.RoastProfileDownload) error {
	if _, err := fmt.Fprintf(w, "Downloaded %d bytes to %s\n", result.Bytes, output.EscapeVisible(result.Path)); err != nil {
		return err
	}
	return output.WriteDetails(w, []output.DetailField{
		{Label: "Roast UUID", Value: result.RoastUUID}, {Label: "Revision", Value: strconv.FormatInt(result.RevisionNumber, 10)}, {Label: "SHA-256", Value: result.SHA256},
	})
}

func writeRoastReviewResult(w io.Writer, result api.RoastReviewResult) error {
	outcome := "Created"
	if result.IdempotentReplay {
		outcome = "Existing review"
	}
	return output.WriteDetails(w, []output.DetailField{
		{Label: "Comment UUID", Value: result.Comment.CommentUUID}, {Label: "Roast UUID", Value: result.Comment.RoastUUID}, {Label: "Author", Value: result.Comment.AuthorNickname},
		{Label: "Revision SHA-256", Value: result.RevisionSHA256}, {Label: "Template", Value: result.TemplateVersion},
		{Label: "Result", Value: outcome}, {Label: "Body", Value: optionalString(result.Comment.Body)},
		{Label: "Created at", Value: result.Comment.CreatedAt}, {Label: "Edited at", Value: optionalString(result.Comment.EditedAt)},
		{Label: "Deleted at", Value: optionalString(result.Comment.DeletedAt)}, {Label: "Deleted", Value: strconv.FormatBool(result.Comment.IsDeleted)},
		{Label: "Can edit", Value: strconv.FormatBool(result.Comment.CanEdit)}, {Label: "Can delete", Value: strconv.FormatBool(result.Comment.CanDelete)},
	})
}

func optionalFloat64(value *float64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}
