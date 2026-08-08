package contracts

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Cell states in a submission grid. Three distinct kinds of empty, because
// collapsing them makes every completion statistic on a branching form wrong.
const (
	CellNotAsked = "not_asked" // the respondent's version did not contain the field
	CellHidden   = "hidden"    // the field existed but a branch hid it
	CellBlank    = "blank"     // shown, and left empty
	CellAnswered = "answered"
)

// ExportColumn describes one column spanning every version of a form.
type ExportColumn struct {
	FieldID      string
	Label        string
	Type         string
	Sensitive    bool
	InVersions   []int
	RetiredAfter int
	TypeVariant  string
	Options      []ExportOption
}

// ExportOption is one choice, kept so a report can show labels for stored ids.
type ExportOption struct {
	ID    string
	Label string
}

// ExportCell is one answer plus why it may be absent.
type ExportCell struct {
	State string
	Value any
}

// ExportRow is one submission ready for a report.
type ExportRow struct {
	ID          uuid.UUID
	VersionNo   int
	SubmittedAt time.Time
	Status      string
	SourceLink  string
	Country     string
	Device      string
	Consents    map[string]bool
	Cells       map[string]ExportCell

	// SubjectID and SensitiveBlob carry the encrypted answers. They travel with
	// the row so the decision to decrypt is made where the capability is
	// checked, rather than in the store that merely reads bytes.
	SubjectID     *uuid.UUID
	SensitiveBlob []byte
}

// ExportFilter narrows what a report covers.
type ExportFilter struct {
	From time.Time
	To   time.Time
	// IncludeSensitive is honoured only when the requester holds the capability;
	// the service decides, not the caller.
	IncludeSensitive bool
}

// SubmissionSource feeds reports without the reporting code knowing anything
// about how forms are stored.
type SubmissionSource interface {
	FormTitle(ctx context.Context, tenantID, formID uuid.UUID) (string, error)
	Columns(ctx context.Context, tenantID, formID uuid.UUID) ([]ExportColumn, error)
	// EachSubmission streams rows to fn. Streaming rather than returning a slice:
	// a year of responses does not fit comfortably in memory, and the writer on
	// the other end is streaming too.
	EachSubmission(ctx context.Context, tenantID, formID uuid.UUID, f ExportFilter, fn func(ExportRow) error) (excluded int, err error)
}

// FunnelPoint is one bucket of the conversion funnel.
type FunnelPoint struct {
	Bucket  time.Time
	Clicks  int
	Views   int
	Starts  int
	Submits int
}

// FunnelSummary is the whole funnel over a period.
type FunnelSummary struct {
	Clicks  int
	Views   int
	Starts  int
	Submits int
	Points  []FunnelPoint
}

// CompletionRate is submits over views: the single number that says whether a
// form is working.
func (f FunnelSummary) CompletionRate() float64 {
	if f.Views == 0 {
		return 0
	}
	return float64(f.Submits) / float64(f.Views)
}

// AbandonRate is the share of people who began filling in and did not finish.
//
// Kept separate from the completion rate because "never started" and "started
// and gave up" are different problems with different fixes.
func (f FunnelSummary) AbandonRate() float64 {
	if f.Starts == 0 {
		return 0
	}
	return 1 - float64(f.Submits)/float64(f.Starts)
}

// PageDropOff is how many respondents left on one page.
type PageDropOff struct {
	PageID  string
	Entered int
	Left    int
}

// Rate is the share of arrivals who went no further.
func (p PageDropOff) Rate() float64 {
	if p.Entered == 0 {
		return 0
	}
	return float64(p.Left) / float64(p.Entered)
}

// SensitiveOpener decrypts the sealed answers of one submission.
//
// Implemented by the consent module, which owns the data subject keys. Returning
// ErrSubjectErased-equivalent behaviour as an error is deliberate: an erased
// subject's answers are gone, and that must be visible as such rather than as an
// empty cell.
type SensitiveOpener interface {
	OpenSensitive(ctx context.Context, tenantID uuid.UUID, subjectID uuid.UUID, submissionID uuid.UUID, blob []byte) (map[string]any, error)
}

// ReportSource supplies the measured side of a report.
type ReportSource interface {
	Funnel(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time, bucket time.Duration) (FunnelSummary, error)
	PageDropOff(ctx context.Context, tenantID, formID uuid.UUID, from, to time.Time) ([]PageDropOff, error)
}

// ClickPoint is one bucket of a link's click history.
type ClickPoint struct {
	Bucket time.Time
	Clicks int
}

// Breakdown is one slice of a dimension: how many clicks came from a referrer,
// a browser family, or a source.
type Breakdown struct {
	Key    string
	Clicks int
	// Networks is how many distinct /24 blocks the clicks came from. Not people:
	// see LinkStats.Networks.
	Networks int
}

// LinkStats is what a short link reports about itself.
//
// The two halves come from different places and that difference is visible to
// the caller on purpose. Points comes from the rollups, which survive the raw
// events; the breakdowns and Visitors are computed from raw events and so reach
// back only as far as RAW_EVENT_RETENTION_DAYS. A chart covering a year above a
// referrer table covering ninety days would otherwise read as "nobody linked to
// us before March".
type LinkStats struct {
	Clicks int
	Points []ClickPoint

	// BreakdownClicks counts the same clicks the breakdowns and Visitors are
	// drawn from. It is not Clicks: rollups reach further back, and the two
	// disagree by exactly the history the raw events no longer hold. Every ratio
	// below is taken against this, never against Clicks -- dividing a raw count
	// by a rollup total produced a repeat rate of -3.1 before this existed.
	BreakdownClicks int

	// Networks is the count of distinct /24 blocks seen in the window.
	//
	// Not unique visitors, and deliberately not called that. Nothing here can
	// identify a returning person: the visit id is minted fresh on every
	// redirect, so counting distinct visit ids would return exactly the click
	// count and any "repeat rate" built on it would be structurally zero.
	// Recognising a person across visits would take a cookie or a fingerprint,
	// which is tracking, needs a lawful basis, and is the thing this product
	// exists to avoid. A network count still answers the question worth asking:
	// 900 clicks from 4 networks is a script or one office, 900 from 300 is
	// reach.
	Networks int

	Sources   []Breakdown // qr or direct
	Referrers []Breakdown
	Browsers  []Breakdown

	// Campaign dimensions, read from the utm_* parameters on the short link.
	// Clicks arriving without them are grouped under a single "no campaign" key
	// rather than dropped, so these columns still add up to the total.
	UTMSources   []Breakdown
	UTMMediums   []Breakdown
	UTMCampaigns []Breakdown

	FirstClick *time.Time
	LastClick  *time.Time

	// BreakdownFrom is the earliest moment the breakdowns can speak for. Older
	// clicks are counted in Clicks and Points but appear in no breakdown.
	BreakdownFrom time.Time
}

// QRShare is the fraction of clicks that came from a scan.
//
// Worth separating: a scan happens in front of whatever the code is printed on,
// so it says something about the poster that a click on a shared link does not.
func (s LinkStats) QRShare() float64 {
	if s.BreakdownClicks == 0 {
		return 0
	}
	for _, b := range s.Sources {
		if b.Key == "qr" {
			return float64(b.Clicks) / float64(s.BreakdownClicks)
		}
	}
	return 0
}

// ClicksPerNetwork is how concentrated the traffic is.
//
// A shortener's clicks are not people, and a total on its own cannot tell reach
// from repetition. A high value means few sources clicking many times -- usually
// a script, a preview bot, or one office behind NAT -- and it is the cheapest
// signal that a click count is not what it looks like.
func (s LinkStats) ClicksPerNetwork() float64 {
	if s.Networks == 0 {
		return 0
	}
	return float64(s.BreakdownClicks) / float64(s.Networks)
}

// LinkSummary is one row of the per-project link leaderboard.
type LinkSummary struct {
	LinkID   uuid.UUID
	Clicks   int
	Submits  int
	LastSeen *time.Time
}

// LinkReporter reads a link's traffic.
//
// Declared here rather than imported from the analytics module: the links module
// owns the endpoint and the analytics module owns the data, and a direct import
// either way would make one unbuildable without the other.
type LinkReporter interface {
	LinkStats(ctx context.Context, tenantID, linkID uuid.UUID, from, to time.Time, bucket, rawRetention time.Duration) (LinkStats, error)
	TopLinks(ctx context.Context, tenantID, projectID uuid.UUID, from, to time.Time, limit int) ([]LinkSummary, error)
}

// LinkReportRow is one link's line in a project's link report.
type LinkReportRow struct {
	LinkID    uuid.UUID
	Code      string
	Host      string
	Target    string
	Status    string
	CreatedAt time.Time

	// Clicks and Submits come from the rollups and cover the whole period.
	Clicks  int
	Submits int

	// QRClicks and Networks come from raw events and therefore cover only as far
	// back as the retention allows. Kept on the same row because that is where a
	// reader wants them, and labelled in the sheet header rather than silently.
	QRClicks int
	Networks int

	LastClick *time.Time
}

// ConversionRate is submits over clicks. Zero for a link with no form behind it,
// where the ratio would be against something that cannot happen.
func (r LinkReportRow) ConversionRate() float64 {
	if r.Clicks == 0 {
		return 0
	}
	return float64(r.Submits) / float64(r.Clicks)
}

// LinkReport is everything a project's link report holds.
type LinkReport struct {
	ProjectName string
	From, To    time.Time
	// BreakdownFrom is the earliest moment the raw-event columns speak for.
	BreakdownFrom time.Time

	Rows  []LinkReportRow
	ByDay []ClickPoint

	Sources      []Breakdown
	Referrers    []Breakdown
	UTMSources   []Breakdown
	UTMMediums   []Breakdown
	UTMCampaigns []Breakdown
}

// Totals sums the rows.
func (r LinkReport) Totals() (clicks, submits int) {
	for _, row := range r.Rows {
		clicks += row.Clicks
		submits += row.Submits
	}
	return clicks, submits
}

// LinkReportSource builds a project's link report.
type LinkReportSource interface {
	LinkReport(ctx context.Context, tenantID, projectID uuid.UUID, from, to time.Time, rawRetention time.Duration) (LinkReport, error)
}

// Directory resolves the names a report header needs.
type Directory interface {
	ProjectName(ctx context.Context, tenantID, projectID uuid.UUID) (string, error)
	// UserEmail identifies who asked for an extract.
	//
	// Looked up from the requesting user id rather than taken from the request
	// body: the provenance sheet exists to say who pulled the data, and a field
	// the caller fills in is not evidence of anything.
	UserEmail(ctx context.Context, tenantID, userID uuid.UUID) (string, error)
}
